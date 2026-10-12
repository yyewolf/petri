// Package leakcheck_test checks that petri control planes release their
// goroutines. It is its own test binary so no other test's goroutines skew the
// counts.
package leakcheck_test

import (
	"context"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/yyewolf/petri"
	"github.com/yyewolf/petri/internal/controllers"
)

const (
	cycles       = 3
	slack        = 10
	pollInterval = 250 * time.Millisecond
	// stableFor outlasts kube-apiserver's 3s watch-cache wakeup timers, which
	// exit on their own after Stop.
	stableFor     = 4 * time.Second
	settleTimeout = 30 * time.Second
	startTimeout  = 2 * time.Minute
	// stubRunTimeout bounds how long the stub controller may take to run
	// after its informers synced, which on a healthy plane is immediate.
	stubRunTimeout = 10 * time.Second
	maxDumpBytes   = 20000
)

//nolint:tparallel // The node cycles' subtests must finish, cleanups included, before the count.
func TestRepeatedNewAndStopDoNotLeakGoroutines(t *testing.T) {
	t.Parallel()

	// The first control plane starts process-wide goroutines (klog's flush
	// daemon, shared caches) that legitimately outlive it.
	cycle(t)

	baseline := stableCount(t)

	for range cycles {
		cycle(t)
	}

	if n := waitAtMost(t, baseline+slack); n > baseline+slack {
		t.Fatalf("%d goroutines after %d New/Stop cycles, want at most %d (baseline %d + %d)\n%s",
			n, cycles, baseline+slack, baseline, slack, goroutineDump())
	}

	// The standalone-manager path keeps working beside the defaults: a second
	// manager enabled on a plane that already runs the default controllers
	// must release its informers all the same.
	for range cycles {
		stubCycle(t)
	}

	if n := waitAtMost(t, baseline+slack); n > baseline+slack {
		t.Fatalf("%d goroutines after %d stub-manager cycles, want at most %d (baseline %d + %d)\n%s",
			n, cycles, baseline+slack, baseline, slack, goroutineDump())
	}

	// Every workload controller enabled beside the defaults: their informers,
	// workers, queues and event broadcasters must all release with the plane.
	for range cycles {
		workloadCycle(t)
	}

	if n := waitAtMost(t, baseline+slack); n > baseline+slack {
		t.Fatalf("%d goroutines after %d workload-controller cycles, want at most %d (baseline %d + %d)\n%s",
			n, cycles, baseline+slack, baseline, slack, goroutineDump())
	}

	// The full controller set — defaults, workloads and the remaining five:
	// EndpointSlice, ServiceAccount, token, ResourceQuota and disruption.
	// Their extra informer factories (quota monitors), scale clients and
	// event broadcasters must release all the same.
	for range cycles {
		fullSetCycle(t)
	}

	if n := waitAtMost(t, baseline+slack); n > baseline+slack {
		t.Fatalf("%d goroutines after %d full-set cycles, want at most %d (baseline %d + %d)\n%s",
			n, cycles, baseline+slack, baseline, slack, goroutineDump())
	}

	// Nodes with Pods running on them, placed by kube-scheduler on one plane
	// and by Pod expectations on another: the scheduler's own informer
	// factories and event broadcaster, the kubelet simulator and the Pod
	// dispatcher with its actions must all release with the plane. Each
	// cycle runs in a subtest so the cleanups the helpers register run
	// before the count.
	for range cycles { //nolint:paralleltest // Sequential: the count needs every cycle done.
		t.Run("scheduler", schedulerCycle)
		t.Run("expectations", expectationsCycle)
	}

	if n := waitAtMost(t, baseline+slack); n > baseline+slack {
		t.Fatalf("%d goroutines after %d node cycles, want at most %d (baseline %d + %d)\n%s",
			n, cycles, baseline+slack, baseline, slack, goroutineDump())
	}
}

// schedulerCycle runs a ReplicaSet on a Node through kube-scheduler and
// stops the plane with its Pods running.
func schedulerCycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), startTimeout)
	defer cancel()

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cp.EnableControllers(t, petri.SchedulerController, petri.ReplicaSetController)
	cp.AddNode(t, petri.NodeOptions{})

	ns, err := cp.NewNamespace(ctx)
	if err != nil {
		t.Fatalf("NewNamespace: %v", err)
	}

	cs := cp.Client()
	labels := map[string]string{"app": "churn"}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "churn"},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: new(int32(2)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "nginx:1.0"}}},
			},
		},
	}
	if _, err = cs.AppsV1().ReplicaSets(ns).Create(ctx, rs, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create replicaset: %v", err)
	}

	petri.WaitFor(t, cs.AppsV1().ReplicaSets(ns), "churn", func(rs *appsv1.ReplicaSet) (bool, error) {
		return rs.Status.ReadyReplicas == 2, nil
	})

	if err = cp.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// expectationsCycle places Pods with an expectation and ExpectPod, and stops
// the plane with an ExpectPod still waiting on a Pod that never comes.
func expectationsCycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), startTimeout)
	defer cancel()

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	node := cp.AddNode(t, petri.NodeOptions{})

	ns, err := cp.NewNamespace(ctx)
	if err != nil {
		t.Fatalf("NewNamespace: %v", err)
	}

	cs := cp.Client()
	expectation := cp.ExpectPods(t, petri.InNamespace(ns)).Times(2).BindTo(node)

	for _, name := range []string{"a", "b"} {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:1.0"}}},
		}
		if _, err = cs.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create pod: %v", err)
		}
	}

	expectation.Wait(t)

	for _, name := range []string{"a", "b"} {
		petri.WaitFor(t, cs.CoreV1().Pods(ns), name,
			petri.StatusCondition[*corev1.Pod]("Ready", metav1.ConditionTrue))
	}

	waiting := make(chan struct{})

	go func() {
		defer close(waiting)

		_, _ = cp.ExpectPodCtx(context.Background(), petri.InNamespace("nowhere"))
	}()

	if err = cp.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	<-waiting
}

// cycle starts and stops one control plane running its default controllers,
// the namespace controller and the garbage collector, with a namespace and a
// ConfigMap in flight so the controllers have real work when Stop releases
// their informers, workers and graph-builder goroutines.
func cycle(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), startTimeout)
	defer cancel()

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ns, err := cp.NewNamespace(ctx)
	if err != nil {
		t.Fatalf("NewNamespace: %v", err)
	}

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "churn"}}
	if _, err = cp.Client().CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create configmap: %v", err)
	}

	background := metav1.DeletePropagationBackground
	deleteOpts := metav1.DeleteOptions{PropagationPolicy: &background}
	if err = cp.Client().CoreV1().Namespaces().Delete(ctx, ns, deleteOpts); err != nil {
		t.Fatalf("delete namespace: %v", err)
	}

	if err = cp.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// stubCycle starts one control plane and enables a stub controller on it
// through a second manager's registry. The stub's build registers a real
// ConfigMap informer, so the standalone manager starts real informer
// goroutines, waits for the cache and joins them again at its own Stop.
func stubCycle(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), startTimeout)
	defer cancel()

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runStubController(t, cp)

	if err = cp.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// runStubController enables a stub controller on cp through a manager
// registry. Its build registers a real ConfigMap informer, so the manager
// starts real informer goroutines, waits for the cache and joins them again
// at its own Stop, mirroring how Stop releases the plane's manager.
func runStubController(t *testing.T, cp *petri.ControlPlane) {
	t.Helper()

	mgr, err := controllers.New(t.Context(), cp.Config())
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}

	ran := make(chan struct{})

	spec := controllers.Spec{
		Name: "leakcheck-stub",
		Build: func(_ context.Context, m *controllers.Manager) (controllers.Run, error) {
			_ = m.KubeFactory().Core().V1().ConfigMaps().Informer()

			return func(context.Context) { close(ran) }, nil
		},
	}

	if err = mgr.Register(spec); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err = mgr.Enable(spec.Name); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	select {
	case <-ran:
	case <-time.After(stubRunTimeout):
		t.Fatal("stub controller never ran")
	}

	mgr.Stop()
}

// workloadCycle starts a plane, enables every workload controller beside the
// defaults, and gives them real work: a Deployment the chain reacts to, and a
// namespace deletion the namespace controller and the garbage collector have
// to wind down while the workload controllers hold informers open.
func workloadCycle(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), startTimeout)
	defer cancel()

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cp.EnableControllers(t, petri.WorkloadControllers()...)

	ns, err := cp.NewNamespace(ctx)
	if err != nil {
		t.Fatalf("NewNamespace: %v", err)
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "churn"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "churn"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "churn"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "nginx:1.0"}}},
			},
		},
	}
	if _, err = cp.Client().AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Let the chain react so Stop releases controllers with live queues and
	// workers, not freshly synced informers.
	petri.Retry(t, func(context.Context) (bool, error) {
		rss, lerr := cp.Client().AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{})
		if lerr != nil {
			return false, lerr
		}

		return len(rss.Items) > 0, nil
	})

	background := metav1.DeletePropagationBackground
	if err = cp.Client().
		CoreV1().
		Namespaces().
		Delete(ctx, ns, metav1.DeleteOptions{PropagationPolicy: &background}); err != nil {
		t.Fatalf("delete namespace: %v", err)
	}

	if err = cp.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// fullSetCycle starts a plane, enables every controller — defaults, workloads
// and the five remaining ones — and gives each of the new ones real work: a
// Service the EndpointSlice controller slices, a quota over the Deployment's
// Pods, a PDB watching them, and a token Secret the token controller fills
// while the namespace churn runs the ServiceAccount controller.
func fullSetCycle(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), startTimeout)
	defer cancel()

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cp.EnableControllers(t, petri.WorkloadControllers()...)
	cp.EnableControllers(t,
		petri.EndpointSliceController,
		petri.ServiceAccountController,
		petri.ServiceAccountTokenController,
		petri.ResourceQuotaController,
		petri.DisruptionController,
	)

	ns, err := cp.NewNamespace(ctx)
	if err != nil {
		t.Fatalf("NewNamespace: %v", err)
	}

	cs := cp.Client()

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "churn"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "churn"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "churn"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "nginx:1.0"}}},
			},
		},
	}
	if _, err = cs.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "churn"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "churn"},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 80}},
		},
	}
	if _, err = cs.CoreV1().Services(ns).Create(ctx, service, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create service: %v", err)
	}

	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "churn"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: new(intstr.FromInt32(1)),
			Selector:     dep.Spec.Selector,
		},
	}
	if _, err = cs.PolicyV1().PodDisruptionBudgets(ns).Create(ctx, pdb, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create poddisruptionbudget: %v", err)
	}

	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "churn"},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{ //nolint:exhaustive // One pods limit is the point; the other resource names are irrelevant.
				corev1.ResourcePods: resource.MustParse("10"),
			},
		},
	}
	if _, err = cs.CoreV1().ResourceQuotas(ns).Create(ctx, quota, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create resourcequota: %v", err)
	}

	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "churn-token",
			Annotations: map[string]string{corev1.ServiceAccountNameKey: "default"},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}
	if _, err = cs.CoreV1().Secrets(ns).Create(ctx, tokenSecret, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create token secret: %v", err)
	}

	// Let the controllers react so Stop releases them with live queues and
	// workers: the workload chain produces a ReplicaSet, the slice controller
	// names a slice after the Service, and the token controller fills the
	// Secret for the default ServiceAccount.
	petri.Retry(t, func(context.Context) (bool, error) {
		rss, lerr := cs.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{})
		if lerr != nil {
			return false, lerr
		}

		return len(rss.Items) > 0, nil
	})
	petri.Retry(t, func(context.Context) (bool, error) {
		list, lerr := cs.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{
			LabelSelector: discoveryv1.LabelServiceName + "=churn",
		})
		if lerr != nil {
			return false, lerr
		}

		return len(list.Items) == 1, nil
	})
	petri.Retry(t, func(context.Context) (bool, error) {
		secret, lerr := cs.CoreV1().Secrets(ns).Get(ctx, "churn-token", metav1.GetOptions{})
		if lerr != nil {
			return false, lerr
		}

		return len(secret.Data[corev1.ServiceAccountTokenKey]) > 0, nil
	})

	background := metav1.DeletePropagationBackground
	if err = cs.CoreV1().
		Namespaces().
		Delete(ctx, ns, metav1.DeleteOptions{PropagationPolicy: &background}); err != nil {
		t.Fatalf("delete namespace: %v", err)
	}

	if err = cp.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// stableCount waits until the goroutine count has not changed for stableFor
// and returns it.
func stableCount(t *testing.T) int {
	t.Helper()

	deadline := time.Now().Add(settleTimeout)
	last, since := runtime.NumGoroutine(), time.Now()

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		if n := runtime.NumGoroutine(); n != last {
			last, since = n, time.Now()
		} else if time.Since(since) >= stableFor {
			return n
		}
	}

	t.Fatalf("goroutine count never settled; last %d\n%s", last, goroutineDump())

	return last
}

// waitAtMost waits up to settleTimeout for the goroutine count to drop to
// limit and returns the last count seen.
func waitAtMost(t *testing.T, limit int) int {
	t.Helper()

	deadline := time.Now().Add(settleTimeout)

	n := runtime.NumGoroutine()
	for n > limit && time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		n = runtime.NumGoroutine()
	}

	return n
}

func goroutineDump() string {
	var b strings.Builder

	_ = pprof.Lookup("goroutine").WriteTo(&b, 1)

	s := b.String()
	if len(s) > maxDumpBytes {
		s = s[:maxDumpBytes] + "\n... (truncated)"
	}

	return s
}
