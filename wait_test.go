package petri_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/yyewolf/petri"
)

const waitTestTimeout = 10 * time.Second

// configMapHook wraps a ConfigMap client with hooks at the seams the wait
// engine uses, to force the race windows a real server rarely exposes.
type configMapHook struct {
	inner corev1client.ConfigMapInterface

	// beforeWatch runs after the engine's initial get, just before the watch
	// request goes out.
	beforeWatch func()
	// firstWatchCloses makes the first watch return an already-closed channel,
	// as if the server had expired it.
	firstWatchCloses bool
}

func (h *configMapHook) Get(
	ctx context.Context, name string, opts metav1.GetOptions,
) (*corev1.ConfigMap, error) {
	return h.inner.Get(ctx, name, opts)
}

func (h *configMapHook) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	if h.beforeWatch != nil {
		h.beforeWatch()
	}

	w, err := h.inner.Watch(ctx, opts)
	if err != nil {
		return nil, err
	}

	if h.firstWatchCloses {
		h.firstWatchCloses = false
		closed := make(chan watch.Event)
		close(closed)

		return &closedWatch{ch: closed}, nil
	}

	return w, nil
}

// closedWatch is a watch whose result channel is already closed.
type closedWatch struct {
	ch chan watch.Event
}

func (w *closedWatch) ResultChan() <-chan watch.Event { return w.ch }
func (w *closedWatch) Stop()                          {}

func createConfigMap(t *testing.T, cs *kubernetes.Clientset, ns, name string, data map[string]string) {
	t.Helper()

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name}, Data: data}
	if _, err := cs.CoreV1().ConfigMaps(ns).Create(t.Context(), cm, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create configmap %s: %v", name, err)
	}
}

func TestWaitUntilReturnsObjectCreatedAfterWaitStarted(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())
	ns := env.Namespace(t)

	ctx, cancel := context.WithTimeout(t.Context(), waitTestTimeout)
	defer cancel()

	type result struct {
		cm  *corev1.ConfigMap
		err error
	}
	res := make(chan result, 1)
	go func() {
		cm, err := petri.WaitUntil(ctx, cs.CoreV1().ConfigMaps(ns), "later", petri.Exists[*corev1.ConfigMap]())
		res <- result{cm, err}
	}()

	time.Sleep(200 * time.Millisecond) // the wait must be watching before the create
	createConfigMap(t, cs, ns, "later", map[string]string{"k": "v"})

	select {
	case got := <-res:
		if got.err != nil {
			t.Fatalf("WaitUntil: %v", got.err)
		}
		if got.cm == nil || got.cm.Data["k"] != "v" {
			t.Errorf("WaitUntil returned %+v, want the created configmap", got.cm)
		}
	case <-time.After(waitTestTimeout):
		t.Fatal("WaitUntil did not return after the object was created")
	}
}

func TestWaitUntilCatchesObjectCreatedBetweenGetAndWatch(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())
	ns := env.Namespace(t)

	// The hook creates the object after the engine's initial get found
	// nothing and before its watch is established. A watch started from
	// "now" replays nothing, so the engine must re-get to see this object.
	created := make(chan struct{})
	hook := &configMapHook{inner: cs.CoreV1().ConfigMaps(ns)}
	hook.beforeWatch = func() {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "raced"}}
		if _, err := cs.CoreV1().ConfigMaps(ns).Create(t.Context(), cm, metav1.CreateOptions{}); err != nil {
			t.Errorf("create in watch hook: %v", err)
		}
		close(created)
	}

	ctx, cancel := context.WithTimeout(t.Context(), waitTestTimeout)
	defer cancel()

	cm, err := petri.WaitUntil(ctx, hook, "raced", petri.Exists[*corev1.ConfigMap]())
	if err != nil {
		t.Fatalf("WaitUntil: %v", err)
	}
	if cm == nil {
		t.Fatal("WaitUntil returned a nil object")
	}
	<-created
}

func TestWaitUntilReconnectsAfterWatchCloses(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())
	ns := env.Namespace(t)

	// The first watch is closed before it delivers anything, as an expired
	// watch looks to a client. The wait must reconnect and still see the
	// change made afterwards.
	hook := &configMapHook{inner: cs.CoreV1().ConfigMaps(ns), firstWatchCloses: true}

	ctx, cancel := context.WithTimeout(t.Context(), waitTestTimeout)
	defer cancel()

	type result struct {
		cm  *corev1.ConfigMap
		err error
	}
	res := make(chan result, 1)
	go func() {
		cm, err := petri.WaitUntil(ctx, hook, "reborn", petri.Exists[*corev1.ConfigMap]())
		res <- result{cm, err}
	}()

	time.Sleep(200 * time.Millisecond) // let the first watch close and the reconnect start
	createConfigMap(t, cs, ns, "reborn", nil)

	select {
	case got := <-res:
		if got.err != nil {
			t.Fatalf("WaitUntil: %v", got.err)
		}
		if got.cm == nil {
			t.Error("WaitUntil returned a nil object")
		}
	case <-time.After(waitTestTimeout):
		t.Fatal("WaitUntil did not return after the watch closed and the object was created")
	}
}

func TestWaitUntilDeleted(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())
	ns := env.Namespace(t)

	ctx, cancel := context.WithTimeout(t.Context(), waitTestTimeout)
	defer cancel()

	// An object that never existed counts as deleted.
	ghost, err := petri.WaitUntil(ctx, cs.CoreV1().ConfigMaps(ns), "ghost", petri.Deleted[*corev1.ConfigMap]())
	if err != nil {
		t.Fatalf("WaitUntil on a never-created object: %v", err)
	}
	if ghost != nil {
		t.Errorf("deleted wait returned %+v, want nil", ghost)
	}

	createConfigMap(t, cs, ns, "doomed", nil)

	type result struct {
		cm  *corev1.ConfigMap
		err error
	}
	res := make(chan result, 1)
	go func() {
		gone, werr := petri.WaitUntil(ctx, cs.CoreV1().ConfigMaps(ns), "doomed", petri.Deleted[*corev1.ConfigMap]())
		res <- result{cm: gone, err: werr}
	}()

	time.Sleep(200 * time.Millisecond)
	background := metav1.DeletePropagationBackground
	if delErr := cs.CoreV1().ConfigMaps(ns).Delete(
		t.Context(), "doomed", metav1.DeleteOptions{PropagationPolicy: &background},
	); delErr != nil {
		t.Fatalf("delete: %v", delErr)
	}

	select {
	case got := <-res:
		if got.err != nil {
			t.Fatalf("WaitUntil: %v", got.err)
		}
		if got.cm != nil {
			t.Errorf("deleted wait returned %+v, want nil", got.cm)
		}
	case <-time.After(waitTestTimeout):
		t.Fatal("WaitUntil did not return after the object was deleted")
	}
}

func TestWaitUntilTimeoutShowsLastObservedState(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())
	ns := env.Namespace(t)

	createConfigMap(t, cs, ns, "stuck", map[string]string{"present": "yes"})

	_, err := petri.WaitUntil(
		t.Context(),
		cs.CoreV1().ConfigMaps(ns),
		"stuck",
		func(cm *corev1.ConfigMap) (bool, error) { return cm.Data["want"] == "x", nil },
		petri.WithTimeout(300*time.Millisecond),
	)
	if err == nil {
		t.Fatal("WaitUntil returned without error, want a timeout")
	}
	for _, want := range []string{"stuck", "last observed", "present"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("timeout error %q does not mention %q", err, want)
		}
	}

	// An object never observed says so instead of printing an empty state.
	_, err = petri.WaitUntil(
		t.Context(),
		cs.CoreV1().ConfigMaps(ns),
		"absent",
		petri.Exists[*corev1.ConfigMap](),
		petri.WithTimeout(200*time.Millisecond),
	)
	if err == nil {
		t.Fatal("WaitUntil returned without error, want a timeout")
	}
	for _, want := range []string{"absent", "never observed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("timeout error %q does not mention %q", err, want)
		}
	}
}

func TestWaitUntilReturnsConditionError(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())
	ns := env.Namespace(t)

	boom := errors.New("boom")
	_, err := petri.WaitUntil(
		t.Context(),
		cs.CoreV1().ConfigMaps(ns),
		"whatever",
		func(*corev1.ConfigMap) (bool, error) { return false, boom },
	)
	if !errors.Is(err, boom) {
		t.Fatalf("WaitUntil error = %v, want boom", err)
	}
}

// TestStatusConditionOnCRD waits for a real CRD to become Established, both
// through the typed apiextensions client and through the dynamic client, and
// starts the wait before the CRD exists so the condition arrives by watch.
func TestStatusConditionOnCRD(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)

	crdClient, err := apiextensionsclient.NewForConfig(env.Config())
	if err != nil {
		t.Fatalf("build apiextensions client: %v", err)
	}

	crdName := "gizmos.statuscond.example.com"
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: crdName},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "statuscond.example.com",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "gizmos", Singular: "gizmo", Kind: "Gizmo", ListKind: "GizmoList",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name:    "v1",
				Served:  true,
				Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type:                   "object",
						XPreserveUnknownFields: new(true),
					},
				},
			}},
		},
	}

	type result struct {
		obj any
		err error
	}
	typedRes := make(chan result, 1)
	go func() {
		established, werr := petri.WaitUntil(
			t.Context(),
			crdClient.ApiextensionsV1().CustomResourceDefinitions(),
			crdName,
			petri.StatusCondition[*apiextensionsv1.CustomResourceDefinition]("Established", metav1.ConditionTrue),
		)
		typedRes <- result{obj: established, err: werr}
	}()

	dyn, err := dynamic.NewForConfig(env.Config())
	if err != nil {
		t.Fatalf("build dynamic client: %v", err)
	}
	gvr := schema.GroupVersionResource{
		Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions",
	}
	dynRes := make(chan result, 1)
	go func() {
		established, werr := petri.WaitUntil(
			t.Context(),
			petri.DynamicResource(dyn.Resource(gvr)),
			crdName,
			petri.StatusCondition[*unstructured.Unstructured]("Established", metav1.ConditionTrue),
		)
		dynRes <- result{obj: established, err: werr}
	}()

	time.Sleep(200 * time.Millisecond) // let both waits start watching
	if _, err = crdClient.ApiextensionsV1().CustomResourceDefinitions().
		Create(t.Context(), crd, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create crd: %v", err)
	}

	for _, tc := range []struct {
		name string
		res  chan result
	}{
		{name: "typed", res: typedRes},
		{name: "dynamic", res: dynRes},
	} {
		select {
		case got := <-tc.res:
			if got.err != nil {
				t.Fatalf("%s wait: %v", tc.name, got.err)
			}
			if !hasConditionTrue(got.obj, "Established") {
				t.Errorf("%s wait returned an object without Established=True: %+v", tc.name, got.obj)
			}
		case <-time.After(waitTestTimeout):
			t.Fatalf("%s wait did not return after the CRD was created", tc.name)
		}
	}
}

func hasConditionTrue(obj any, condType string) bool {
	switch o := obj.(type) {
	case *apiextensionsv1.CustomResourceDefinition:
		for _, c := range o.Status.Conditions {
			if string(c.Type) == condType && c.Status == apiextensionsv1.ConditionTrue {
				return true
			}
		}
	case *unstructured.Unstructured:
		conds, found, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
		if !found {
			return false
		}
		for _, c := range conds {
			m, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := m["type"].(string); t == condType {
				s, _ := m["status"].(string)

				return s == "True"
			}
		}
	}

	return false
}

func TestWaitForReturnsObject(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())
	ns := env.Namespace(t)

	createConfigMap(t, cs, ns, "here", map[string]string{"k": "v"})

	cm := petri.WaitFor(t, cs.CoreV1().ConfigMaps(ns), "here", petri.Exists[*corev1.ConfigMap]())
	if cm == nil || cm.Data["k"] != "v" {
		t.Errorf("WaitFor returned %+v, want the configmap", cm)
	}
}

func TestRetryUntil(t *testing.T) {
	t.Parallel()

	n := 0
	err := petri.RetryUntil(t.Context(), func(context.Context) (bool, error) {
		n++

		return n >= 3, nil
	})
	if err != nil {
		t.Fatalf("RetryUntil: %v", err)
	}
	if n < 3 {
		t.Errorf("condition ran %d times, want at least 3", n)
	}

	boom := errors.New("boom")
	err = petri.RetryUntil(t.Context(), func(context.Context) (bool, error) { return false, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("RetryUntil error = %v, want boom", err)
	}
}

func TestRetryUntilTimesOut(t *testing.T) {
	t.Parallel()

	start := time.Now()
	err := petri.RetryUntil(
		context.Background(),
		func(context.Context) (bool, error) { return false, nil },
		petri.WithTimeout(200*time.Millisecond),
	)
	if err == nil {
		t.Fatal("RetryUntil returned without error, want a timeout")
	}
	if !strings.Contains(err.Error(), "200ms") {
		t.Errorf("timeout error %q does not mention the deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("RetryUntil took %v, want it to stop at the deadline", elapsed)
	}
}

func TestRetryTBVariant(t *testing.T) {
	t.Parallel()

	n := 0
	petri.Retry(t, func(context.Context) (bool, error) {
		n++

		return n >= 2, nil
	})
	if n < 2 {
		t.Errorf("condition ran %d times, want at least 2", n)
	}
}
