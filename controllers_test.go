package petri_test

import (
	"context"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/yyewolf/petri"
)

// envtestSemanticsWindow is how long the opt-out test waits to confirm that
// nothing happens. The namespace controller delays its first pass by a 5s
// grace period, so a window longer than that proves the controllers are not
// running, not merely slow.
const envtestSemanticsWindow = 6 * time.Second

// widgetGVR is the main resource of the widgets.example.com CRD from
// testdata/crds, which the custom-kind tests install after the control plane
// started.
func widgetGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
}

func dynamicFor(t *testing.T, cfg *rest.Config) *dynamic.DynamicClient {
	t.Helper()

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("build dynamic client: %v", err)
	}

	return dyn
}

// newNamespace creates a namespace on a dedicated plane, for tests that
// manage the namespace's lifecycle themselves.
func newNamespace(ctx context.Context, t *testing.T, cp *petri.ControlPlane) string {
	t.Helper()

	name, err := cp.NewNamespace(ctx)
	if err != nil {
		t.Fatalf("NewNamespace: %v", err)
	}

	return name
}

// ownerRef builds an owner reference to owner, which must have been read from
// the API server so its UID is set.
func ownerRef(apiVersion, kind string, owner metav1.Object) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: owner.GetName(), UID: owner.GetUID()}
}

func configMapOwnerRef(owner *corev1.ConfigMap) metav1.OwnerReference {
	return ownerRef("v1", "ConfigMap", owner)
}

func widgetOwnerRef(owner *unstructured.Unstructured) metav1.OwnerReference {
	return ownerRef("example.com/v1", "Widget", owner)
}

// blocking marks the reference so the owner waits for this dependent's
// deletion under foreground propagation; without it the owner may be finalized
// before its dependents are collected.
func blocking(ref metav1.OwnerReference) metav1.OwnerReference {
	ref.BlockOwnerDeletion = new(true)

	return ref
}

func mustCreateConfigMap(
	ctx context.Context, t *testing.T, cs kubernetes.Interface, ns, name string, owner *metav1.OwnerReference,
) *corev1.ConfigMap {
	t.Helper()

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if owner != nil {
		cm.OwnerReferences = []metav1.OwnerReference{*owner}
	}

	created, err := cs.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create configmap %s/%s: %v", ns, name, err)
	}

	return created
}

// mustCreateWidget creates a custom resource of the post-startup CRD,
// optionally owned by another object.
func mustCreateWidget(
	ctx context.Context, t *testing.T, dyn dynamic.Interface, ns, name string, owner *metav1.OwnerReference,
) *unstructured.Unstructured {
	t.Helper()

	widget := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{},
	}}
	if owner != nil {
		widget.SetOwnerReferences([]metav1.OwnerReference{*owner})
	}

	created, err := dyn.Resource(widgetGVR()).Namespace(ns).Create(ctx, widget, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create widget %s/%s: %v", ns, name, err)
	}

	return created
}

// deleteNamespace deletes a namespace with background propagation, the way
// Namespace's cleanup does.
func deleteNamespace(ctx context.Context, cs kubernetes.Interface, name string) error {
	background := metav1.DeletePropagationBackground

	return cs.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &background})
}

// deleteConfigMap deletes a ConfigMap with the given propagation policy.
func deleteConfigMap(
	ctx context.Context, t *testing.T, cs kubernetes.Interface, ns, name string, policy metav1.DeletionPropagation,
) {
	t.Helper()

	err := cs.CoreV1().ConfigMaps(ns).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &policy})
	if err != nil {
		t.Fatalf("delete configmap %s/%s: %v", ns, name, err)
	}
}

// deleteWidget deletes a custom resource with the given propagation policy.
func deleteWidget(
	ctx context.Context, t *testing.T, dyn dynamic.Interface, ns, name string, policy metav1.DeletionPropagation,
) {
	t.Helper()

	err := dyn.Resource(widgetGVR()).Namespace(ns).Delete(ctx, name,
		metav1.DeleteOptions{PropagationPolicy: &policy})
	if err != nil {
		t.Fatalf("delete widget %s/%s: %v", ns, name, err)
	}
}

func namespacePhase(want corev1.NamespacePhase) petri.Condition[*corev1.Namespace] {
	return func(ns *corev1.Namespace) (bool, error) {
		if ns == nil {
			return false, nil
		}

		return ns.Status.Phase == want, nil
	}
}

// TestDeletedNamespaceFullyDisappears proves the mechanic Namespace's cleanup
// relies on: a deleted namespace is not merely Terminating but is removed once
// its contents are gone.
func TestDeletedNamespaceFullyDisappears(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	env := petri.Start(t)
	cs := clientFor(t, env.Config())
	ns := env.Namespace(t)

	mustCreateConfigMap(ctx, t, cs, ns, "in-ns", nil)

	if err := deleteNamespace(ctx, cs, ns); err != nil {
		t.Fatalf("delete namespace: %v", err)
	}

	petri.WaitFor(t, cs.CoreV1().Namespaces(), ns, petri.Deleted[*corev1.Namespace]())
	petri.WaitFor(t, cs.CoreV1().ConfigMaps(ns), "in-ns", petri.Deleted[*corev1.ConfigMap]())
}

// TestNamespaceDeletionRemovesPostStartupCustomResources installs a CRD on a
// plane that is already running, then checks that deleting a namespace also
// removes its custom resources: the namespace controller rediscovers the
// served resources on every deletion.
func TestNamespaceDeletionRemovesPostStartupCustomResources(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cp.InstallCRDs(t, "testdata/crds")

	ns := newNamespace(ctx, t, cp)
	cs := clientFor(t, cp.Config())
	dyn := dynamicFor(t, cp.Config())

	mustCreateWidget(ctx, t, dyn, ns, "swept", nil)

	err = deleteNamespace(ctx, cs, ns)
	if err != nil {
		t.Fatalf("delete namespace: %v", err)
	}

	petri.WaitFor(t, petri.DynamicResource(dyn.Resource(widgetGVR()).Namespace(ns)), "swept",
		petri.Deleted[*unstructured.Unstructured]())
	petri.WaitFor(t, cs.CoreV1().Namespaces(), ns, petri.Deleted[*corev1.Namespace]())
}

// TestGarbageCollectorPropagatesForBuiltInKinds runs the three propagation
// policies against ConfigMaps owned by other ConfigMaps.
func TestGarbageCollectorPropagatesForBuiltInKinds(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	for _, tt := range []struct {
		name   string
		policy metav1.DeletionPropagation
		block  bool
	}{
		{name: "background", policy: metav1.DeletePropagationBackground},
		{name: "foreground", policy: metav1.DeletePropagationForeground, block: true},
		{name: "orphan", policy: metav1.DeletePropagationOrphan},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runBuiltInPropagation(ctx, t, cp, tt.policy, tt.block)
		})
	}
}

// runBuiltInPropagation drives one propagation policy with a ConfigMap owner
// and a ConfigMap dependent in a fresh namespace.
func runBuiltInPropagation(
	ctx context.Context, t *testing.T, cp *petri.ControlPlane, policy metav1.DeletionPropagation, block bool,
) {
	t.Helper()

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	owner := mustCreateConfigMap(ctx, t, cs, ns, "owner", nil)
	ref := configMapOwnerRef(owner)
	if block {
		ref = blocking(ref)
	}
	dependent := mustCreateConfigMap(ctx, t, cs, ns, "dependent", &ref)

	deleteConfigMap(ctx, t, cs, ns, owner.Name, policy)

	if policy == metav1.DeletePropagationOrphan {
		// The owner is collected but the dependent survives without the
		// reference. The wait holds until the GC stripped the references:
		// a plain Exists observes the dependent while orphaning is still in
		// flight.
		petri.WaitFor(t, cs.CoreV1().ConfigMaps(ns), owner.Name, petri.Deleted[*corev1.ConfigMap]())

		got := petri.WaitFor(t, cs.CoreV1().ConfigMaps(ns), dependent.Name, func(cm *corev1.ConfigMap) (bool, error) {
			return cm != nil && len(cm.OwnerReferences) == 0, nil
		})
		if got == nil {
			t.Fatal("orphaned dependent disappeared")
		}

		return
	}

	// Foreground collects the dependents before finalizing the owner;
	// background deletes the owner at once and the dependent when the graph
	// notices it is dangling.
	petri.WaitFor(t, cs.CoreV1().ConfigMaps(ns), dependent.Name, petri.Deleted[*corev1.ConfigMap]())
	petri.WaitFor(t, cs.CoreV1().ConfigMaps(ns), owner.Name, petri.Deleted[*corev1.ConfigMap]())
}

// customCase describes one propagation scenario with a custom resource on one
// side of the owner/dependent pair.
type customCase struct {
	name           string
	ownerBuiltIn   bool
	dependentBuilt bool
	policy         metav1.DeletionPropagation
	block          bool
}

// TestGarbageCollectorPropagatesForCustomResources runs the propagation
// policies against custom resources of a CRD installed after startup, in both
// directions: custom owners of built-in dependents and built-in owners of
// custom dependents.
func TestGarbageCollectorPropagatesForCustomResources(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cp.InstallCRDs(t, "testdata/crds")

	for _, tt := range []customCase{
		{name: "custom-owner-background", policy: metav1.DeletePropagationBackground},
		{name: "custom-owner-foreground", policy: metav1.DeletePropagationForeground, block: true},
		{name: "custom-owner-orphan", policy: metav1.DeletePropagationOrphan},
		{name: "custom-owner-builtin-dependent", dependentBuilt: true, policy: metav1.DeletePropagationBackground},
		{name: "custom-dependent", ownerBuiltIn: true, policy: metav1.DeletePropagationBackground},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runCustomPropagation(ctx, t, cp, tt)
		})
	}
}

// runCustomPropagation drives one propagation policy where the owner or the
// dependent is a custom resource. Everything is read through the dynamic
// client, so built-in and custom kinds wait the same way.
func runCustomPropagation(ctx context.Context, t *testing.T, cp *petri.ControlPlane, tt customCase) {
	t.Helper()

	cs := clientFor(t, cp.Config())
	dyn := dynamicFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	widgets := petri.DynamicResource(dyn.Resource(widgetGVR()).Namespace(ns))
	configMaps := petri.DynamicResource(
		dyn.Resource(corev1.SchemeGroupVersion.WithResource("configmaps")).Namespace(ns))

	var ownerRef metav1.OwnerReference
	if tt.ownerBuiltIn {
		ownerRef = configMapOwnerRef(mustCreateConfigMap(ctx, t, cs, ns, "owner", nil))
	} else {
		ownerRef = widgetOwnerRef(mustCreateWidget(ctx, t, dyn, ns, "owner", nil))
	}
	if tt.block {
		ownerRef = blocking(ownerRef)
	}

	if tt.dependentBuilt {
		mustCreateConfigMap(ctx, t, cs, ns, "dependent", &ownerRef)
	} else {
		mustCreateWidget(ctx, t, dyn, ns, "dependent", &ownerRef)
	}

	if tt.ownerBuiltIn {
		deleteConfigMap(ctx, t, cs, ns, "owner", tt.policy)
	} else {
		deleteWidget(ctx, t, dyn, ns, "owner", tt.policy)
	}

	ownerClient, dependentClient := widgets, widgets
	if tt.ownerBuiltIn {
		ownerClient = configMaps
	}
	if tt.dependentBuilt {
		dependentClient = configMaps
	}

	if tt.policy == metav1.DeletePropagationOrphan {
		// The owner is collected but the dependent survives without the
		// reference. The wait holds until the GC stripped the references:
		// a plain Exists observes the dependent while orphaning is still in
		// flight.
		petri.WaitFor(t, ownerClient, "owner", petri.Deleted[*unstructured.Unstructured]())

		got := petri.WaitFor(t, dependentClient, "dependent", func(u *unstructured.Unstructured) (bool, error) {
			return u != nil && len(u.GetOwnerReferences()) == 0, nil
		})
		if got == nil {
			t.Fatal("orphaned dependent disappeared")
		}

		return
	}

	// The dependent goes first when its reference blocks, and both are gone
	// by the end either way.
	petri.WaitFor(t, dependentClient, "dependent", petri.Deleted[*unstructured.Unstructured]())
	petri.WaitFor(t, ownerClient, "owner", petri.Deleted[*unstructured.Unstructured]())
}

// TestNewWithoutDefaultControllersMatchesEnvtest starts a plane with the
// defaults off: namespaces stay Terminating with their contents and owner
// references do not cascade, as in envtest.
func TestNewWithoutDefaultControllersMatchesEnvtest(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	cp, err := petri.New(ctx, petri.WithoutDefaultControllers())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	owner := mustCreateConfigMap(ctx, t, cs, ns, "owner", nil)
	mustCreateConfigMap(ctx, t, cs, ns, "dependent", new(configMapOwnerRef(owner)))

	// A deleted namespace is marked Terminating by the API server itself, but
	// with no namespace controller it is never finalized and its contents
	// stay.
	err = deleteNamespace(ctx, cs, ns)
	if err != nil {
		t.Fatalf("delete namespace: %v", err)
	}

	petri.WaitFor(t, cs.CoreV1().Namespaces(), ns, namespacePhase(corev1.NamespaceTerminating))

	// Owner references do not cascade: the owner is really deleted, the
	// dependent is left behind.
	err = cs.CoreV1().ConfigMaps(ns).Delete(ctx, owner.Name, metav1.DeleteOptions{})
	if err != nil {
		t.Fatalf("delete owner: %v", err)
	}

	time.Sleep(envtestSemanticsWindow)

	terminating, err := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace vanished with the controllers off: %v", err)
	}
	if terminating.Status.Phase != corev1.NamespaceTerminating {
		t.Errorf("namespace phase = %q, want Terminating", terminating.Status.Phase)
	}

	if _, err = cs.CoreV1().ConfigMaps(ns).Get(ctx, "dependent", metav1.GetOptions{}); err != nil {
		t.Fatalf("dependent was garbage collected with the controllers off: %v", err)
	}
}

// TestTwoPlanesRunDefaultsIndependently keeps proving two dedicated planes
// work in one process, now with the default controllers running on both: the
// same names exist on both planes and cleaning up one plane leaves the other
// untouched.
func TestTwoPlanesRunDefaultsIndependently(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	const (
		nsName        = "two-planes"
		ownerName     = "owner"
		dependentName = "dependent"
	)

	planes := make([]*petri.ControlPlane, 2)
	errs := make([]error, 2)

	var wg sync.WaitGroup
	for i := range planes {
		wg.Go(func() { planes[i], errs[i] = petri.New(ctx) })
	}
	wg.Wait()

	t.Cleanup(func() {
		for _, cp := range planes {
			if cp != nil {
				_ = cp.Stop()
			}
		}
	})

	for i, err := range errs {
		if err != nil {
			t.Fatalf("New #%d: %v", i, err)
		}
	}

	active, untouched := planes[0], planes[1]

	for _, cp := range planes {
		createTwoPlanesFixtures(ctx, t, cp, nsName, ownerName, dependentName)
	}

	if err := deleteNamespace(ctx, active.Client(), nsName); err != nil {
		t.Fatalf("delete namespace on the active plane: %v", err)
	}

	// The active plane's namespace is finalized and its contents go with it.
	petri.WaitFor(t, active.Client().CoreV1().Namespaces(), nsName, petri.Deleted[*corev1.Namespace]())
	petri.WaitFor(t, active.Client().CoreV1().ConfigMaps(nsName), dependentName, petri.Deleted[*corev1.ConfigMap]())

	// The untouched plane keeps everything, including its namespace.
	if _, err := untouched.Client().CoreV1().Namespaces().Get(ctx, nsName, metav1.GetOptions{}); err != nil {
		t.Fatalf("namespace on the untouched plane is gone: %v", err)
	}

	// Its garbage collector still works: deleting the owner cascades there.
	deleteConfigMap(ctx, t, untouched.Client(), nsName, ownerName, metav1.DeletePropagationBackground)
	petri.WaitFor(t, untouched.Client().CoreV1().ConfigMaps(nsName), dependentName, petri.Deleted[*corev1.ConfigMap]())

	// The workload controllers are only process-global in their metrics
	// registration, which upstream tolerates re-attempting or guards with a
	// sync.Once: enabling the whole set on both planes in one process must
	// not panic, and each plane's controllers must actually run.
	for _, cp := range planes {
		cp.EnableControllers(t, petri.WorkloadControllers()...)
	}

	for i, cp := range planes {
		ns, err := cp.NewNamespace(ctx)
		if err != nil {
			t.Fatalf("NewNamespace #%d: %v", i, err)
		}

		rs := &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Name: "workloads"},
			Spec: appsv1.ReplicaSetSpec{
				Replicas: new(int32(2)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "workloads"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "workloads"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "nginx:1.0"}}},
				},
			},
		}

		created, err := cp.Client().AppsV1().ReplicaSets(ns).Create(ctx, rs, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("create replicaset on plane #%d: %v", i, err)
		}

		waitForControlledPods(ctx, t, cp.Client(), ns, created.UID, 2)
	}
}

// createTwoPlanesFixtures creates the same namespace and ConfigMap pair on one
// plane, so cross-talk between planes would be visible.
func createTwoPlanesFixtures(
	ctx context.Context, t *testing.T, cp *petri.ControlPlane, nsName, ownerName, dependentName string,
) {
	t.Helper()

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	if _, err := cp.Client().CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	owner := mustCreateConfigMap(ctx, t, cp.Client(), nsName, ownerName, nil)
	mustCreateConfigMap(ctx, t, cp.Client(), nsName, dependentName, new(configMapOwnerRef(owner)))
}
