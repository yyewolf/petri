package petri

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
)

const (
	// DefaultWaitTimeout bounds a wait whose context has no deadline.
	DefaultWaitTimeout = 30 * time.Second
	// defaultWaitInterval spaces out re-gets after a watch closes and spaces
	// out RetryUntil polls.
	defaultWaitInterval = 100 * time.Millisecond
)

// waitConfig is the resolved options of a wait or a retry.
type waitConfig struct {
	timeout  time.Duration
	interval time.Duration
}

// WaitOption adjusts a wait or a retry.
type WaitOption func(*waitConfig)

// WithTimeout overrides how long the wait or retry may take. A context with a
// shorter deadline still wins.
func WithTimeout(d time.Duration) WaitOption {
	return func(cfg *waitConfig) { cfg.timeout = d }
}

func newWaitConfig(opts []WaitOption) waitConfig {
	cfg := waitConfig{timeout: DefaultWaitTimeout, interval: defaultWaitInterval}
	for _, opt := range opts {
		opt(&cfg)
	}

	return cfg
}

// ObjectClient is the part of a client-go resource interface that the wait
// helpers need. Every typed resource client satisfies it as-is, for example
// cs.AppsV1().Deployments(ns) or an apiextensions CRD client. For the dynamic
// client, wrap its ResourceInterface with DynamicResource.
type ObjectClient[O runtime.Object] interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (O, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

// dynamicObjects adapts a dynamic.ResourceInterface, whose Get takes optional
// subresource paths, to ObjectClient.
type dynamicObjects struct {
	ri dynamic.ResourceInterface
}

// DynamicResource adapts a dynamic.ResourceInterface so waits can follow
// resources that have no typed client, CRDs included.
func DynamicResource(ri dynamic.ResourceInterface) ObjectClient[*unstructured.Unstructured] {
	return dynamicObjects{ri: ri}
}

func (d dynamicObjects) Get(
	ctx context.Context, name string, opts metav1.GetOptions,
) (*unstructured.Unstructured, error) {
	return d.ri.Get(ctx, name, opts)
}

func (d dynamicObjects) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return d.ri.Watch(ctx, opts)
}

// WaitUntil waits until cond holds for the object named name, watched through
// client, and returns the last observed object (nil if it never existed).
//
// The wait reads the object first and starts the watch from that
// resourceVersion, so a change made just before the wait is not missed. When
// the initial get finds nothing, the watch starts from "now" and a confirming
// re-get closes the window in which the object could have appeared. Watch
// expiry and disconnects are handled by re-getting and re-watching, so the
// wait only ends when the condition holds, the context is done, or the timeout
// expires.
//
// The condition receives nil only for an object that is not there, which is
// what the Deleted condition waits for.
func WaitUntil[O runtime.Object](
	ctx context.Context, client ObjectClient[O], name string, cond Condition[O], opts ...WaitOption,
) (O, error) {
	cfg := newWaitConfig(opts)

	// A parent deadline shorter than the timeout still ends the wait, so
	// wrapping unconditionally is enough.
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	return waitUntil(ctx, client, name, cond, cfg)
}

// WaitFor is WaitUntil for tests: it fails the test when the condition never
// holds or errors, and returns the last observed object.
func WaitFor[O runtime.Object](
	t testing.TB, client ObjectClient[O], name string, cond Condition[O], opts ...WaitOption,
) O {
	t.Helper()

	obj, err := WaitUntil(t.Context(), client, name, cond, opts...)
	if err != nil {
		t.Fatal(err)
	}

	return obj
}

func waitUntil[O runtime.Object](
	ctx context.Context, client ObjectClient[O], name string, cond Condition[O], cfg waitConfig,
) (O, error) {
	var last O

	for {
		obj, err := getOne(ctx, client, name)
		if err != nil {
			return last, err
		}

		listOptions, startMissing, err := watchOptions(obj, name)
		if err != nil {
			return last, err
		}

		last = obj

		done, err := check(name, cond, obj)
		if err != nil || done {
			return obj, err
		}

		w, err := client.Watch(ctx, listOptions)
		if err != nil {
			if ctx.Err() != nil {
				return last, timeoutError(ctx, name, last)
			}

			// Starting the watch failed; the next round re-gets and retries.
			sleepInterval(ctx, cfg.interval)

			continue
		}

		obj, done, err = followWatch(ctx, client, w, startMissing, name, cond, last)
		w.Stop()
		if err != nil || done {
			return obj, err
		}

		if ctx.Err() != nil {
			return last, timeoutError(ctx, name, last)
		}

		// The watch closed (expired, 410 Gone, server gone): breathe, re-get,
		// re-watch.
		sleepInterval(ctx, cfg.interval)
	}
}

// watchOptions builds the watch request of one round: from the object's
// resourceVersion, never from "now", so events between the get and the watch
// are replayed rather than skipped. When the object is missing, the watch
// starts from "now" and the round confirms with a re-get instead.
func watchOptions[O runtime.Object](obj O, name string) (metav1.ListOptions, bool, error) {
	opts := metav1.ListOptions{AllowWatchBookmarks: true}

	if isNil(obj) {
		return opts, true, nil
	}

	accessor, err := meta.Accessor(obj)
	if err != nil {
		return opts, false, fmt.Errorf("petri: get %q: %w", name, err)
	}

	opts.ResourceVersion = accessor.GetResourceVersion()

	return opts, false, nil
}

// followWatch applies the condition to the object's events until it holds, the
// context is done, or the watch closes. When startMissing is set, the watch
// began from "now" and a confirming re-get closes the window in which the
// object could have appeared. done with a nil error means the condition holds.
func followWatch[O runtime.Object](
	ctx context.Context, client ObjectClient[O], w watch.Interface, startMissing bool,
	name string, cond Condition[O], last O,
) (O, bool, error) {
	if startMissing {
		obj, done, err := confirmStart(ctx, client, name, cond, last)
		if err != nil || done {
			return obj, done, err
		}

		last = obj
	}

	for {
		select {
		case <-ctx.Done():
			return last, false, nil
		case ev, ok := <-w.ResultChan():
			if !ok {
				// The watch closed; reconnect from a fresh get.
				return last, false, nil
			}

			obj, done, reconnect, err := applyWatchEvent(ev, name, cond, last)
			if err != nil {
				return last, false, err
			}
			if reconnect {
				// Gone and expired end the watch, not the wait: reconnect.
				return last, false, nil
			}

			last = obj

			if done {
				return obj, true, nil
			}
		}
	}
}

// confirmStart re-reads the object right after a watch from "now" began, so an
// object created between the get and the watch is not missed. It returns the
// last observed object and whether the wait is over.
func confirmStart[O runtime.Object](
	ctx context.Context, client ObjectClient[O], name string, cond Condition[O], last O,
) (O, bool, error) {
	obj, err := getOne(ctx, client, name)
	if err != nil {
		return last, false, err
	}

	if isNil(obj) {
		return last, false, nil
	}

	done, err := check(name, cond, obj)

	return obj, done, err
}

// applyWatchEvent decides what one watch event means for the wait. It returns
// the object to remember as last observed (zero for a deletion, unchanged for
// events that do not concern the object), whether the condition holds, whether
// the watch should reconnect, and any condition error.
func applyWatchEvent[O runtime.Object](
	ev watch.Event, name string, cond Condition[O], last O,
) (O, bool, bool, error) {
	switch ev.Type {
	case watch.Added, watch.Modified, watch.Deleted:
	case watch.Bookmark:
		// Bookmarks carry no state for a condition to read.
		return last, false, false, nil
	case watch.Error:
		if werr := watchError(ev.Object); werr != nil {
			return last, false, false, werr
		}

		return last, false, true, nil
	default:
		return last, false, false, nil
	}

	accessor, err := meta.Accessor(ev.Object)
	if err != nil {
		return last, false, false, fmt.Errorf("petri: watch event for %q: %w", name, err)
	}

	if accessor.GetName() != name {
		return last, false, false, nil
	}

	obj, ok := ev.Object.(O)
	if !ok {
		return last, false, false, fmt.Errorf("petri: watch event for %q has unexpected type %T", name, ev.Object)
	}

	if ev.Type == watch.Deleted {
		var zero O
		obj = zero
	}

	done, err := check(name, cond, obj)

	return obj, done, false, err
}

// getOne reads the object, turning NotFound into a nil result so conditions
// can wait for it to appear or disappear.
func getOne[O runtime.Object](ctx context.Context, client ObjectClient[O], name string) (O, error) {
	var zero O

	obj, err := client.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return zero, nil
		}

		return zero, fmt.Errorf("petri: get %q: %w", name, err)
	}

	return obj, nil
}

// check runs the condition, naming the object when it errors.
func check[O runtime.Object](name string, cond Condition[O], obj O) (bool, error) {
	ok, err := cond(obj)
	if err != nil {
		return false, fmt.Errorf("petri: condition for %q: %w", name, err)
	}

	return ok, nil
}

func timeoutError(ctx context.Context, name string, last any) error {
	if err := ctx.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("petri: wait for %q: %w", name, err)
	}

	if isNil(last) {
		return fmt.Errorf("petri: wait for %q: timed out; object never observed", name)
	}

	state, err := json.MarshalIndent(last, "", "  ")
	if err != nil {
		state = []byte(fmt.Sprintf("%+v", last))
	}

	return fmt.Errorf("petri: wait for %q: timed out; last observed state:\n%s", name, state)
}

// watchError turns a watch.Error event into an error, unless it only says the
// watch expired, in which case a nil error sends the wait into a reconnect.
func watchError(obj runtime.Object) error {
	status, ok := obj.(*metav1.Status)
	if !ok {
		return fmt.Errorf("petri: watch failed with unexpected event object %T", obj)
	}

	if status.Code == http.StatusGone || status.Reason == metav1.StatusReasonExpired {
		return nil
	}

	return fmt.Errorf("petri: watch failed: %s", status.Message)
}

// sleepInterval waits one interval, ending early when the context is done.
func sleepInterval(ctx context.Context, interval time.Duration) {
	timer := time.NewTimer(interval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// isNil reports whether obj is nil, including a typed nil such as a nil
// *appsv1.Deployment stored in O.
func isNil(obj any) bool {
	if obj == nil {
		return true
	}

	v := reflect.ValueOf(obj)
	//nolint:exhaustive // Only nilable kinds can hold nil; every other kind is a value.
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	case reflect.Invalid: // a nil interface boxed in any
		return true
	default:
		return false
	}
}

// RetryUntil runs cond until it holds, it errors, or the timeout expires.
// It is the escape hatch for waiting on something that is not a single
// object: discovery, an informer, a side effect.
func RetryUntil(ctx context.Context, cond func(context.Context) (bool, error), opts ...WaitOption) error {
	cfg := newWaitConfig(opts)

	// A parent deadline shorter than the timeout still ends the retry, so
	// wrapping unconditionally is enough.
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	for {
		ok, err := cond(ctx)
		if err != nil {
			return fmt.Errorf("petri: retry condition: %w", err)
		}
		if ok {
			return nil
		}

		if ctx.Err() != nil {
			return fmt.Errorf("petri: retry condition not met after %s", cfg.timeout)
		}

		sleepInterval(ctx, cfg.interval)
	}
}

// Retry is RetryUntil for tests: it fails the test when the condition never
// holds or errors.
func Retry(t testing.TB, cond func(context.Context) (bool, error), opts ...WaitOption) {
	t.Helper()

	if err := RetryUntil(t.Context(), cond, opts...); err != nil {
		t.Fatal(err)
	}
}
