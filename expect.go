package petri

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// errPlaneStopped ends the Pod waits of a control plane that stopped.
var errPlaneStopped = errors.New("petri: control plane is stopped")

// PodMatcher selects Pods for [ControlPlane.ExpectPod] and
// [ControlPlane.ExpectPods]. A Pod matches a list of matchers when it matches
// every one. Build one with [InNamespace], [Labeled], [OwnedBy] or
// [PodMatching].
type PodMatcher struct {
	desc  string
	match func(*corev1.Pod) bool
}

// PodMatching returns a matcher from a predicate. desc describes it in
// failure messages, as in "expected 3 Pods <desc>".
func PodMatching(desc string, match func(*corev1.Pod) bool) PodMatcher {
	return PodMatcher{desc: desc, match: match}
}

// String returns the matcher's description.
func (m PodMatcher) String() string {
	return m.desc
}

// InNamespace matches the Pods of one namespace.
func InNamespace(namespace string) PodMatcher {
	return PodMatching("in namespace "+namespace, func(p *corev1.Pod) bool { return p.Namespace == namespace })
}

// Labeled matches the Pods carrying a label with the given value.
func Labeled(key, value string) PodMatcher {
	return PodMatching(fmt.Sprintf("labeled %s=%s", key, value), func(p *corev1.Pod) bool {
		v, ok := p.Labels[key]

		return ok && v == value
	})
}

// OwnedBy matches the Pods with an owner reference to owner, such as the
// ReplicaSet, Job or StatefulSet that created them.
func OwnedBy(owner metav1.Object) PodMatcher {
	uid := owner.GetUID()

	return PodMatching("owned by "+owner.GetName(), func(p *corev1.Pod) bool {
		return slices.ContainsFunc(p.OwnerReferences, func(ref metav1.OwnerReference) bool { return ref.UID == uid })
	})
}

func matchesAll(matchers []PodMatcher, pod *corev1.Pod) bool {
	for _, m := range matchers {
		if !m.match(pod) {
			return false
		}
	}

	return true
}

func describeMatchers(matchers []PodMatcher) string {
	if len(matchers) == 0 {
		return "of any kind"
	}

	descs := make([]string, 0, len(matchers))
	for _, m := range matchers {
		descs = append(descs, m.desc)
	}

	return strings.Join(descs, ", ")
}

// ExpectPod waits for a Pending Pod that no node was picked for yet, matching
// every matcher, and returns it. Each Pod is handed out once: the next call
// waits for another one, so a test expects three replicas with three calls.
// Pods the declarative expectations of [ControlPlane.ExpectPods] claim are
// never returned. The test then decides the Pod's fate, typically with
// [ControlPlane.Bind]; until then it stays Pending.
//
// It needs a dedicated control plane without [SchedulerController], and
// fails the test after [DefaultWaitTimeout].
func (cp *ControlPlane) ExpectPod(t testing.TB, matchers ...PodMatcher) *corev1.Pod {
	t.Helper()

	pod, err := cp.ExpectPodCtx(t.Context(), matchers...)
	if err != nil {
		t.Fatal(err)
	}

	return pod
}

// ExpectPodCtx is ExpectPod returning the error. It waits until ctx's
// deadline, or [DefaultWaitTimeout] when ctx has none.
func (cp *ControlPlane) ExpectPodCtx(ctx context.Context, matchers ...PodMatcher) (*corev1.Pod, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, DefaultWaitTimeout)
		defer cancel()
	}

	if err := cp.claimScheduling(schedulingManual); err != nil {
		return nil, err
	}

	d, err := cp.podDispatcher(ctx)
	if err != nil {
		return nil, err
	}

	w := &podWaiter{matchers: matchers, ch: make(chan *corev1.Pod, 1)}
	d.addWaiter(w)

	var cause error

	select {
	case pod := <-w.ch:
		return pod, nil
	case <-ctx.Done():
		cause = ctx.Err()
	case <-d.ctx.Done():
		cause = errPlaneStopped
	}

	if d.removeWaiter(w) {
		return nil, fmt.Errorf("petri: no Pending Pod %s arrived: %w; unclaimed Pending Pods: %s",
			describeMatchers(matchers), cause, d.unclaimedNames())
	}

	// The dispatcher handed a Pod over while the wait was timing out.
	return <-w.ch, nil
}

// PodExpectation is a declarative expectation from [ControlPlane.ExpectPods]:
// it claims matching Pending Pods as they arrive, up to its count, and applies
// its action to each. Its methods configure it and return it, for chaining;
// they are safe to call while Pods arrive, and an action set late still
// applies to the Pods already claimed.
type PodExpectation struct {
	d        *podDispatcher
	matchers []PodMatcher

	// Guarded by d.mu.
	times   int // -1 for any number
	action  func(context.Context, *corev1.Pod) error
	matched []*corev1.Pod
	acted   int // matched Pods whose action started
	running int // actions in flight
	errs    []error
	changed chan struct{} // closed and replaced on every change
}

// ExpectPods registers a declarative expectation, in the style of gomock: it
// claims Pending Pods matching every matcher as they arrive, one by default
// (see [PodExpectation.Times]), and applies its action to each (see
// [PodExpectation.BindTo] and [PodExpectation.Do]); without one, claimed Pods
// stay Pending. When the test finishes, an expectation that claimed fewer
// Pods than its count, or whose action failed, fails the test.
//
// Expectations claim Pods before [ControlPlane.ExpectPod] does, in the order
// they were registered. ExpectPods needs a dedicated control plane without
// [SchedulerController].
func (cp *ControlPlane) ExpectPods(t testing.TB, matchers ...PodMatcher) *PodExpectation {
	t.Helper()

	e, err := cp.ExpectPodsCtx(t.Context(), matchers...)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()

		// Let actions in flight finish before judging them.
		e.settle(ctx)

		for _, err := range e.verify() {
			t.Error(err)
		}
	})

	return e
}

// Times sets how many Pods the expectation claims and requires, 1 by
// default. Times(0) claims none, so with [ControlPlane.StrictPods] a matching
// Pod fails the test.
func (e *PodExpectation) Times(n int) *PodExpectation {
	e.d.mu.Lock()
	defer e.d.mu.Unlock()

	e.times = max(n, 0)
	e.d.rescanLocked()

	return e
}

// AnyTimes makes the expectation claim every matching Pod and require none.
func (e *PodExpectation) AnyTimes() *PodExpectation {
	e.d.mu.Lock()
	defer e.d.mu.Unlock()

	e.times = -1
	e.d.rescanLocked()

	return e
}

// BindTo binds every claimed Pod to node, as [ControlPlane.Bind] does.
func (e *PodExpectation) BindTo(node *Node) *PodExpectation {
	name := node.name

	return e.Do(func(ctx context.Context, pod *corev1.Pod) error {
		return node.cp.bind(ctx, pod, name)
	})
}

// Do runs action on every claimed Pod, once per Pod, in a goroutine of its
// own; an error fails the test when it finishes. It replaces any earlier
// action for the Pods not acted on yet.
func (e *PodExpectation) Do(action func(ctx context.Context, pod *corev1.Pod) error) *PodExpectation {
	e.d.mu.Lock()
	defer e.d.mu.Unlock()

	e.action = action
	e.d.runActionsLocked(e)

	return e
}

// Pods returns copies of the Pods the expectation claimed so far, in arrival
// order, as they were when claimed.
func (e *PodExpectation) Pods() []*corev1.Pod {
	e.d.mu.Lock()
	defer e.d.mu.Unlock()

	pods := make([]*corev1.Pod, 0, len(e.matched))
	for _, p := range e.matched {
		pods = append(pods, p.DeepCopy())
	}

	return pods
}

// Wait waits until the expectation claimed its count of Pods and their
// actions finished, failing the test on a timeout or a failed action.
func (e *PodExpectation) Wait(t testing.TB, opts ...WaitOption) {
	t.Helper()

	if err := e.WaitCtx(t.Context(), opts...); err != nil {
		t.Fatal(err)
	}
}

// WaitCtx is Wait returning the error.
func (e *PodExpectation) WaitCtx(ctx context.Context, opts ...WaitOption) error {
	cfg := newWaitConfig(opts)

	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	for {
		e.d.mu.Lock()
		done := e.satisfiedLocked() && e.settledLocked()
		errs := slices.Clone(e.errs)
		claimed, times := len(e.matched), e.times
		changed := e.changed
		e.d.mu.Unlock()

		if done {
			return errors.Join(errs...)
		}

		var cause error

		select {
		case <-changed:
			continue
		case <-ctx.Done():
			cause = ctx.Err()
		case <-e.d.ctx.Done():
			cause = errPlaneStopped
		}

		return fmt.Errorf("petri: expected %d Pods %s, claimed %d: %w; unclaimed Pending Pods: %s",
			times, describeMatchers(e.matchers), claimed, cause, e.d.unclaimedNames())
	}
}

// settle waits for the actions in flight, bounded by ctx.
func (e *PodExpectation) settle(ctx context.Context) {
	for {
		e.d.mu.Lock()
		settled := e.settledLocked()
		changed := e.changed
		e.d.mu.Unlock()

		if settled {
			return
		}

		select {
		case <-changed:
		case <-ctx.Done():
			return
		case <-e.d.ctx.Done():
			return
		}
	}
}

// verify returns the expectation's failures once the test is over.
func (e *PodExpectation) verify() []error {
	e.d.mu.Lock()
	defer e.d.mu.Unlock()

	errs := slices.Clone(e.errs)
	if !e.satisfiedLocked() {
		errs = append(errs, fmt.Errorf("petri: expected %d Pods %s, claimed %d",
			e.times, describeMatchers(e.matchers), len(e.matched)))
	}

	if !e.settledLocked() {
		errs = append(
			errs,
			fmt.Errorf("petri: Pods %s: %d actions still running", describeMatchers(e.matchers), e.running),
		)
	}

	return errs
}

func (e *PodExpectation) satisfiedLocked() bool {
	return e.times < 0 || len(e.matched) >= e.times
}

func (e *PodExpectation) settledLocked() bool {
	return e.running == 0 && (e.action == nil || e.acted == len(e.matched))
}

func (e *PodExpectation) acceptsLocked(pod *corev1.Pod) bool {
	return (e.times < 0 || len(e.matched) < e.times) && matchesAll(e.matchers, pod)
}

func (e *PodExpectation) notifyLocked() {
	close(e.changed)
	e.changed = make(chan struct{})
}

// ExpectPodsCtx is ExpectPods for suites without a [testing.TB], such as
// Ginkgo's: nothing is checked when the test finishes, so call
// [PodExpectation.Verify] at the end. ctx bounds the start of the Pod watch
// only.
func (cp *ControlPlane) ExpectPodsCtx(ctx context.Context, matchers ...PodMatcher) (*PodExpectation, error) {
	if err := cp.claimScheduling(schedulingManual); err != nil {
		return nil, err
	}

	d, err := cp.podDispatcher(ctx)
	if err != nil {
		return nil, err
	}

	e := &PodExpectation{d: d, matchers: matchers, times: 1, changed: make(chan struct{})}
	d.addExpectation(e)

	return e, nil
}

// Verify waits, bounded by ctx, for the actions in flight and returns what
// ExpectPods would fail the test with: too few Pods claimed, or failed
// actions.
func (e *PodExpectation) Verify(ctx context.Context) error {
	e.settle(ctx)

	return errors.Join(e.verify()...)
}

// StrictPods makes the control plane fail the test, when it finishes, for
// every Pending Pod no expectation claimed: a Pod nobody expected, in the
// style of a strict mock. Pods bound with [ControlPlane.Bind] count as
// expected. It needs a dedicated control plane without [SchedulerController].
func (cp *ControlPlane) StrictPods(t testing.TB) {
	t.Helper()

	if err := cp.StrictPodsCtx(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if names := cp.UnexpectedPods(); len(names) > 0 {
			t.Errorf("petri: unexpected Pods: %s", strings.Join(names, ", "))
		}
	})
}

// StrictPodsCtx is StrictPods for suites without a [testing.TB], such as
// Ginkgo's: it starts tracking Pods, and the suite checks
// [ControlPlane.UnexpectedPods] at the end. ctx bounds the start of the Pod
// watch only.
func (cp *ControlPlane) StrictPodsCtx(ctx context.Context) error {
	if err := cp.claimScheduling(schedulingManual); err != nil {
		return err
	}

	_, err := cp.podDispatcher(ctx)

	return err
}

// UnexpectedPods returns, as namespace/name and sorted, every Pending Pod the
// control plane observed since Pod expectations started that nobody claimed:
// what [ControlPlane.StrictPods] fails the test with. Pods bound with
// [ControlPlane.Bind] count as claimed.
func (cp *ControlPlane) UnexpectedPods() []string {
	d := cp.pods.Load()
	if d == nil {
		return nil
	}

	return d.unclaimed()
}

// podWaiter is one ExpectPod call waiting for a Pod.
type podWaiter struct {
	matchers []PodMatcher
	ch       chan *corev1.Pod // buffered: the dispatcher never blocks on it
}

// podDispatcher watches the plane's unscheduled Pods and hands each to the
// first expectation, then the first ExpectPod call, that wants it. A Pod is
// claimed at most once.
type podDispatcher struct {
	ctx      context.Context
	cancel   context.CancelFunc
	informer cache.SharedIndexInformer
	wg       sync.WaitGroup // the informer and the actions

	mu           sync.Mutex
	stopped      bool
	claimed      map[types.UID]struct{}
	seen         map[types.UID]string // every unscheduled Pod observed, by namespace/name
	expectations []*PodExpectation
	waiters      []*podWaiter
}

// podDispatcher returns the plane's dispatcher, starting it on first use, once
// its informer synced, bounded by ctx.
func (cp *ControlPlane) podDispatcher(ctx context.Context) (*podDispatcher, error) {
	cp.podsOnce.Do(func() {
		if cp.stoppedAlready() {
			return
		}

		cp.pods.Store(newPodDispatcher(cp))
	})

	d := cp.pods.Load()
	if d == nil {
		return nil, errPlaneStopped
	}

	// The sync wait ends early when the plane stops under it.
	syncCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	stop := context.AfterFunc(d.ctx, func() { cancel(errPlaneStopped) })
	defer stop()

	if !cache.WaitForCacheSync(syncCtx.Done(), d.informer.HasSynced) {
		return nil, fmt.Errorf("petri: watch unscheduled pods: %w", context.Cause(syncCtx))
	}

	return d, nil
}

func newPodDispatcher(cp *ControlPlane) *podDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	d := &podDispatcher{
		ctx:     ctx,
		cancel:  cancel,
		claimed: map[types.UID]struct{}{},
		seen:    map[types.UID]string{},
	}

	unscheduled := fields.OneTermEqualSelector("spec.nodeName", "").String()
	d.informer = coreinformers.NewFilteredPodInformer(cp.Client(), metav1.NamespaceAll, 0, cache.Indexers{},
		func(opts *metav1.ListOptions) { opts.FieldSelector = unscheduled })

	// Registering a handler on an informer that has not started cannot fail.
	_, _ = d.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    d.offer,
		UpdateFunc: func(_, obj any) { d.offer(obj) },
	})

	d.wg.Go(func() { d.informer.RunWithContext(ctx) })

	return d
}

// stop releases the informer and waits for the actions in flight, whose API
// calls fail fast once ctx is cancelled.
func (d *podDispatcher) stop() {
	d.mu.Lock()
	d.stopped = true
	d.mu.Unlock()

	d.cancel()
	d.wg.Wait()
}

func (d *podDispatcher) offer(obj any) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.dispatchLocked(pod)
}

// dispatchLocked hands an unclaimed unscheduled Pod to the first taker.
func (d *podDispatcher) dispatchLocked(pod *corev1.Pod) {
	if d.stopped || pod.Spec.NodeName != "" || pod.DeletionTimestamp != nil {
		return
	}

	if _, done := d.claimed[pod.UID]; done {
		return
	}

	d.seen[pod.UID] = pod.Namespace + "/" + pod.Name

	for _, e := range d.expectations {
		if e.acceptsLocked(pod) {
			d.claimed[pod.UID] = struct{}{}
			e.matched = append(e.matched, pod.DeepCopy())
			d.runActionsLocked(e)
			e.notifyLocked()

			return
		}
	}

	for i, w := range d.waiters {
		if matchesAll(w.matchers, pod) {
			d.claimed[pod.UID] = struct{}{}
			d.waiters = slices.Delete(d.waiters, i, i+1)
			w.ch <- pod.DeepCopy()

			return
		}
	}
}

// rescanLocked offers every Pod already in the informer again, oldest first,
// after a new taker appeared.
func (d *podDispatcher) rescanLocked() {
	var pods []*corev1.Pod

	for _, obj := range d.informer.GetStore().List() {
		if pod, ok := obj.(*corev1.Pod); ok {
			pods = append(pods, pod)
		}
	}

	slices.SortFunc(pods, func(a, b *corev1.Pod) int {
		if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
			return c
		}

		return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name)
	})

	for _, pod := range pods {
		d.dispatchLocked(pod)
	}
}

// runActionsLocked starts e's action for the Pods it claimed and has not
// acted on yet.
func (d *podDispatcher) runActionsLocked(e *PodExpectation) {
	if e.action == nil || d.stopped {
		return
	}

	for ; e.acted < len(e.matched); e.acted++ {
		pod, action := e.matched[e.acted], e.action
		e.running++

		d.wg.Go(func() {
			err := action(d.ctx, pod.DeepCopy())

			d.mu.Lock()
			defer d.mu.Unlock()

			e.running--
			if err != nil {
				e.errs = append(e.errs, fmt.Errorf("petri: Pod %s/%s %s: %w",
					pod.Namespace, pod.Name, describeMatchers(e.matchers), err))
			}

			e.notifyLocked()
		})
	}
}

func (d *podDispatcher) addExpectation(e *PodExpectation) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.expectations = append(d.expectations, e)
	d.rescanLocked()
}

func (d *podDispatcher) addWaiter(w *podWaiter) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.waiters = append(d.waiters, w)
	d.rescanLocked()
}

// removeWaiter withdraws a waiter, reporting false when it was already served.
func (d *podDispatcher) removeWaiter(w *podWaiter) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	i := slices.Index(d.waiters, w)
	if i < 0 {
		return false
	}

	d.waiters = slices.Delete(d.waiters, i, i+1)

	return true
}

func (d *podDispatcher) markClaimed(uid types.UID) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.claimed[uid] = struct{}{}
}

// unclaimed returns the namespace/name of every unscheduled Pod observed and
// never claimed, sorted.
func (d *podDispatcher) unclaimed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	var names []string

	for uid, name := range d.seen {
		if _, ok := d.claimed[uid]; !ok {
			names = append(names, name)
		}
	}

	slices.Sort(names)

	return names
}

// unclaimedNames lists the unscheduled Pods that exist and nobody claimed,
// for failure messages.
func (d *podDispatcher) unclaimedNames() string {
	d.mu.Lock()
	defer d.mu.Unlock()

	var names []string

	for _, obj := range d.informer.GetStore().List() {
		pod, ok := obj.(*corev1.Pod)
		if !ok || pod.DeletionTimestamp != nil {
			continue
		}

		if _, claimed := d.claimed[pod.UID]; !claimed {
			names = append(names, pod.Namespace+"/"+pod.Name)
		}
	}

	if len(names) == 0 {
		return "none"
	}

	slices.Sort(names)

	return strings.Join(names, ", ")
}
