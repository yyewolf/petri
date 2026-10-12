package petri_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/yyewolf/petri"
)

func gpuTaint() corev1.Taint {
	return corev1.Taint{Key: "example.com/gpu", Value: "true", Effect: corev1.TaintEffectNoSchedule}
}

func newDedicatedPlane(t *testing.T) (context.Context, *petri.ControlPlane) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	return ctx, cp
}

func newNodePod(name string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "init", Image: "busybox:1.0"}},
			Containers:     []corev1.Container{{Name: "app", Image: "nginx:1.0"}},
		},
	}
}

func mustCreatePod(ctx context.Context, t *testing.T, cs kubernetes.Interface, ns string, pod *corev1.Pod) *corev1.Pod {
	t.Helper()

	created, err := cs.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create pod %s: %v", pod.Name, err)
	}

	return created
}

func podPhase(phase corev1.PodPhase) petri.Condition[*corev1.Pod] {
	return func(p *corev1.Pod) (bool, error) { return p.Status.Phase == phase, nil }
}

func podReady() petri.Condition[*corev1.Pod] {
	return petri.StatusCondition[*corev1.Pod]("Ready", metav1.ConditionTrue)
}

func scheduledTo(node string) petri.Condition[*corev1.Pod] {
	return func(p *corev1.Pod) (bool, error) {
		if p.Spec.NodeName != "" && p.Spec.NodeName != node {
			return false, fmt.Errorf("scheduled to %s, want %s", p.Spec.NodeName, node)
		}

		return p.Spec.NodeName == node, nil
	}
}

func hasTaint(node *corev1.Node, key string) bool {
	return slices.ContainsFunc(node.Spec.Taints, func(t corev1.Taint) bool { return t.Key == key })
}

// TestAddNodeReportsAReadyNode checks the Node AddNode creates: ready,
// carrying the test's taints and labels but not the not-ready taint
// admission put on it, with default capacity and a PodCIDR.
func TestAddNodeReportsAReadyNode(t *testing.T) {
	t.Parallel()

	ctx, cp := newDedicatedPlane(t)
	cs := cp.Client()

	node := cp.AddNode(t, petri.NodeOptions{
		Name:     "gpu-a",
		Labels:   map[string]string{"example.com/pool": "gpu"},
		Taints:   []corev1.Taint{gpuTaint()},
		Capacity: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}, //nolint:exhaustive // CPU alone.
	})

	got, err := cs.CoreV1().Nodes().Get(ctx, node.Name(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}

	if !hasTaint(got, gpuTaint().Key) || hasTaint(got, corev1.TaintNodeNotReady) {
		t.Errorf("taints = %v, want only %s", got.Spec.Taints, gpuTaint().Key)
	}

	if got.Labels["example.com/pool"] != "gpu" || got.Labels[corev1.LabelHostname] != "gpu-a" {
		t.Errorf("labels = %v", got.Labels)
	}

	if cpu := got.Status.Allocatable[corev1.ResourceCPU]; cpu.String() != "4" {
		t.Errorf("allocatable cpu = %s, want 4", cpu.String())
	}

	if pods := got.Status.Allocatable[corev1.ResourcePods]; pods.String() != "110" {
		t.Errorf("allocatable pods = %s, want 110", pods.String())
	}

	if got.Spec.PodCIDR == "" {
		t.Error("node has no PodCIDR")
	}

	ready := slices.IndexFunc(got.Status.Conditions, func(c corev1.NodeCondition) bool {
		return c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue
	})
	if ready < 0 {
		t.Errorf("node not Ready: %v", got.Status.Conditions)
	}

	// Tainting and untainting by key and effect; cordoning.
	extra := corev1.Taint{Key: "example.com/maintenance", Effect: corev1.TaintEffectNoExecute}
	node.Taint(t, extra)
	node.Untaint(t, gpuTaint())
	node.Cordon(t)

	got, err = cs.CoreV1().Nodes().Get(ctx, node.Name(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}

	if hasTaint(got, gpuTaint().Key) || !hasTaint(got, extra.Key) || !got.Spec.Unschedulable {
		t.Errorf("after taint/untaint/cordon: taints %v, unschedulable %t", got.Spec.Taints, got.Spec.Unschedulable)
	}

	node.Uncordon(t)
}

// TestManualPlacementRunsPods drives a Pod by hand: expected, bound, started
// by the simulator with an IP from its Node, completed by the test, and left
// alone afterwards.
func TestManualPlacementRunsPods(t *testing.T) {
	t.Parallel()

	ctx, cp := newDedicatedPlane(t)
	cs := cp.Client()
	ns := newNamespace(ctx, t, cp)
	node := cp.AddNode(t, petri.NodeOptions{})

	mustCreatePod(ctx, t, cs, ns, newNodePod("job", map[string]string{"app": "job"}))

	pod := cp.ExpectPod(t, petri.InNamespace(ns), petri.Labeled("app", "job"))
	if pod.Name != "job" {
		t.Fatalf("ExpectPod returned %s, want job", pod.Name)
	}

	cp.Bind(t, pod, node)

	pods := cs.CoreV1().Pods(ns)
	running := petri.WaitFor(t, pods, "job", podReady())

	if running.Status.Phase != corev1.PodRunning {
		t.Errorf("phase = %s, want Running", running.Status.Phase)
	}

	got, err := cs.CoreV1().Nodes().Get(ctx, node.Name(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}

	cidr := netip.MustParsePrefix(got.Spec.PodCIDR)
	if ip, parseErr := netip.ParseAddr(running.Status.PodIP); parseErr != nil || !cidr.Contains(ip) {
		t.Errorf("pod IP %q not in node CIDR %s", running.Status.PodIP, cidr)
	}

	if running.Status.HostIP == "" || len(running.Status.InitContainerStatuses) != 1 ||
		running.Status.InitContainerStatuses[0].State.Terminated == nil {
		t.Errorf("status = %+v", running.Status)
	}

	cp.CompletePod(t, running)
	petri.WaitFor(t, pods, "job", podPhase(corev1.PodSucceeded))

	// Another Pod on the same Node gets another IP, and the completed Pod
	// stays completed while the simulator works.
	mustCreatePod(ctx, t, cs, ns, newNodePod("second", nil))
	cp.Bind(t, cp.ExpectPod(t, petri.InNamespace(ns)), node)

	second := petri.WaitFor(t, pods, "second", podReady())

	finished, err := pods.Get(ctx, "job", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}

	if finished.Status.Phase != corev1.PodSucceeded {
		t.Errorf("completed pod phase = %s", finished.Status.Phase)
	}

	cp.SetPodReady(t, second, false)
	petri.WaitFor(t, pods, "second", petri.StatusCondition[*corev1.Pod]("Ready", metav1.ConditionFalse))

	cp.FailPod(t, second, 3)

	failed := petri.WaitFor(t, pods, "second", podPhase(corev1.PodFailed))
	if code := failed.Status.ContainerStatuses[0].State.Terminated.ExitCode; code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

// TestNotReadyNodeFreezesItsPods checks that a Node marked not Ready behaves
// like one whose kubelet died: tainted not-ready, bound Pods do not start and
// deletions do not finish, until it is Ready again.
func TestNotReadyNodeFreezesItsPods(t *testing.T) {
	t.Parallel()

	ctx, cp := newDedicatedPlane(t)
	cs := cp.Client()
	ns := newNamespace(ctx, t, cp)
	node := cp.AddNode(t, petri.NodeOptions{})
	pods := cs.CoreV1().Pods(ns)

	mustCreatePod(ctx, t, cs, ns, newNodePod("victim", nil))
	cp.Bind(t, cp.ExpectPod(t, petri.InNamespace(ns)), node)
	petri.WaitFor(t, pods, "victim", podReady())

	node.SetReady(t, false)

	got, err := cs.CoreV1().Nodes().Get(ctx, node.Name(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}

	if !hasTaint(got, corev1.TaintNodeNotReady) {
		t.Errorf("not-ready node taints = %v", got.Spec.Taints)
	}

	mustCreatePod(ctx, t, cs, ns, newNodePod("frozen", nil))
	cp.Bind(t, cp.ExpectPod(t, petri.InNamespace(ns)), node)

	if err = pods.Delete(ctx, "victim", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete pod: %v", err)
	}

	_, err = petri.WaitUntil(ctx, pods, "frozen", podPhase(corev1.PodRunning), petri.WithTimeout(2*time.Second))
	if err == nil {
		t.Error("pod started on a not-ready node")
	}

	if _, err = pods.Get(ctx, "victim", metav1.GetOptions{}); err != nil {
		t.Errorf("deleted pod on a not-ready node is gone: %v", err)
	}

	node.SetReady(t, true)
	petri.WaitFor(t, pods, "frozen", podReady())
	petri.WaitFor(t, pods, "victim", petri.Deleted[*corev1.Pod]())
}

// TestEvictionAndDrainHonourBudgets checks that Evict and Drain go through
// the Eviction API: a PodDisruptionBudget refuses them until it allows the
// disruption, and a drained Node ends cordoned and empty.
func TestEvictionAndDrainHonourBudgets(t *testing.T) {
	t.Parallel()

	ctx, cp := newDedicatedPlane(t)
	cp.EnableControllers(t, petri.DisruptionController)

	cs := cp.Client()
	ns := newNamespace(ctx, t, cp)
	node := cp.AddNode(t, petri.NodeOptions{})
	pods := cs.CoreV1().Pods(ns)

	labels := map[string]string{"app": "guarded"}
	for _, name := range []string{"a", "b"} {
		mustCreatePod(ctx, t, cs, ns, newNodePod(name, labels))
	}

	cp.ExpectPods(t, petri.InNamespace(ns)).Times(2).BindTo(node).Wait(t)

	for _, name := range []string{"a", "b"} {
		petri.WaitFor(t, pods, name, podReady())
	}

	pdbs := cs.PolicyV1().PodDisruptionBudgets(ns)

	_, err := pdbs.Create(ctx, &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "guard"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: new(intstr.FromInt32(2)),
			Selector:     &metav1.LabelSelector{MatchLabels: labels},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create pdb: %v", err)
	}

	// Two ready Pods, two required: the budget computes zero disruptions.
	petri.WaitFor(t, pdbs, "guard", func(p *policyv1.PodDisruptionBudget) (bool, error) {
		return p.Status.ObservedGeneration == p.Generation && p.Status.CurrentHealthy == 2, nil
	})

	victim, err := pods.Get(ctx, "a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}

	if err = cp.EvictCtx(ctx, victim); !apierrors.IsTooManyRequests(err) {
		t.Fatalf("evict under budget: %v, want TooManyRequests", err)
	}

	// Drain retries until the budget allows the evictions.
	drained := make(chan error, 1)

	go func() { drained <- node.DrainCtx(ctx, petri.WithTimeout(time.Minute)) }()

	if err = pdbs.Delete(ctx, "guard", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete pdb: %v", err)
	}

	if err = <-drained; err != nil {
		t.Fatalf("drain: %v", err)
	}

	list, err := pods.List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}

	if len(list.Items) != 0 {
		t.Errorf("%d pods left after drain", len(list.Items))
	}
}

// TestDeploymentBecomesAvailableOnManualNodes runs a Deployment end to end
// with the test placing its Pods: it reaches Available, its Service gets
// ready endpoints, and deleting the namespace finishes despite the Pods.
func TestDeploymentBecomesAvailableOnManualNodes(t *testing.T) {
	t.Parallel()

	ctx, cp := newDedicatedPlane(t)
	cp.EnableControllers(t, petri.DeploymentController, petri.ReplicaSetController, petri.EndpointSliceController)

	cs := cp.Client()
	ns := newNamespace(ctx, t, cp)
	node := cp.AddNode(t, petri.NodeOptions{})

	cp.ExpectPods(t, petri.InNamespace(ns), petri.Labeled("app", "web")).Times(2).BindTo(node)

	dep := newDeployment("web", corev1.Container{Name: "web", Image: "nginx:1.0"})
	dep.Spec.Replicas = new(int32(2))

	if _, err := cs.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	petri.WaitFor(t, cs.AppsV1().Deployments(ns), "web",
		petri.StatusCondition[*appsv1.Deployment]("Available", metav1.ConditionTrue))
	petri.WaitFor(t, cs.AppsV1().Deployments(ns), "web", func(d *appsv1.Deployment) (bool, error) {
		return d.Status.ReadyReplicas == 2, nil
	})

	_, err := cs.CoreV1().Services(ns).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "web"},
			Ports:    []corev1.ServicePort{{Port: 80}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	petri.Retry(t, func(ctx context.Context) (bool, error) {
		slices, listErr := cs.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{})
		if listErr != nil {
			return false, listErr
		}

		ready := 0

		for _, s := range slices.Items {
			for _, e := range s.Endpoints {
				if e.Conditions.Ready != nil && *e.Conditions.Ready {
					ready++
				}
			}
		}

		return ready == 2, nil
	})

	if err = cs.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete namespace: %v", err)
	}

	petri.WaitFor(t, cs.CoreV1().Namespaces(), ns, petri.Deleted[*corev1.Namespace](), petri.WithTimeout(time.Minute))
}

// TestSchedulerPlacesPodsLikeACluster runs kube-scheduler over a few Nodes:
// tolerations, node selectors and resource requests decide placement as on
// a real cluster, and a Deployment becomes Available with nobody binding.
func TestSchedulerPlacesPodsLikeACluster(t *testing.T) {
	t.Parallel()

	ctx, cp := newDedicatedPlane(t)
	cp.EnableControllers(t, petri.SchedulerController, petri.DeploymentController, petri.ReplicaSetController)

	cs := cp.Client()
	ns := newNamespace(ctx, t, cp)
	pods := cs.CoreV1().Pods(ns)

	cp.AddNode(t, petri.NodeOptions{Name: "gpu", Taints: []corev1.Taint{gpuTaint()}})
	cp.AddNode(t, petri.NodeOptions{Name: "zone-b", Labels: map[string]string{"topology.kubernetes.io/zone": "b"}})
	cp.AddNode(t, petri.NodeOptions{
		Name:     "small",
		Capacity: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}, //nolint:exhaustive // CPU alone.
	})

	tolerating := newNodePod("tolerating", nil)
	tolerating.Spec.Tolerations = []corev1.Toleration{{Key: gpuTaint().Key, Operator: corev1.TolerationOpExists}}
	tolerating.Spec.NodeSelector = map[string]string{corev1.LabelHostname: "gpu"}
	mustCreatePod(ctx, t, cs, ns, tolerating)

	zoned := newNodePod("zoned", nil)
	zoned.Spec.NodeSelector = map[string]string{"topology.kubernetes.io/zone": "b"}
	mustCreatePod(ctx, t, cs, ns, zoned)

	intolerant := newNodePod("intolerant", nil)
	intolerant.Spec.NodeSelector = map[string]string{corev1.LabelHostname: "gpu"}
	mustCreatePod(ctx, t, cs, ns, intolerant)

	greedy := newNodePod("greedy", nil)
	greedy.Spec.NodeSelector = map[string]string{corev1.LabelHostname: "small"}
	greedy.Spec.Containers[0].Resources.Requests = corev1.ResourceList{ //nolint:exhaustive // CPU alone.
		corev1.ResourceCPU: resource.MustParse("2"),
	}
	mustCreatePod(ctx, t, cs, ns, greedy)

	petri.WaitFor(t, pods, "tolerating", scheduledTo("gpu"))
	petri.WaitFor(t, pods, "tolerating", podReady())
	petri.WaitFor(t, pods, "zoned", scheduledTo("zone-b"))

	unschedulable := petri.StatusCondition[*corev1.Pod]("PodScheduled", metav1.ConditionFalse)
	for _, name := range []string{"intolerant", "greedy"} {
		pod := petri.WaitFor(t, pods, name, unschedulable)
		if pod.Spec.NodeName != "" {
			t.Errorf("pod %s scheduled to %s", name, pod.Spec.NodeName)
		}
	}

	dep := newDeployment("web", corev1.Container{Name: "web", Image: "nginx:1.0"})
	dep.Spec.Replicas = new(int32(3))

	if _, err := cs.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	petri.WaitFor(t, cs.AppsV1().Deployments(ns), "web", func(d *appsv1.Deployment) (bool, error) {
		return d.Status.ReadyReplicas == 3, nil
	})

	// Placing Pods by hand is refused while the scheduler runs.
	if _, err := cp.ExpectPodCtx(ctx, petri.InNamespace(ns)); err == nil {
		t.Error("ExpectPod worked with the scheduler on")
	}
}

// recordingT is a [testing.TB] whose errors and cleanups the test inspects
// instead of failing on them.
type recordingT struct {
	testing.TB

	mu       sync.Mutex
	errs     []string
	cleanups []func()
}

func (r *recordingT) Helper() {}

func (r *recordingT) Error(args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.errs = append(r.errs, fmt.Sprint(args...))
}

func (r *recordingT) Errorf(format string, args ...any) {
	r.Error(fmt.Sprintf(format, args...))
}

func (r *recordingT) Cleanup(f func()) {
	r.cleanups = append(r.cleanups, f)
}

// finish runs the cleanups, last first, and returns the errors recorded.
func (r *recordingT) finish() []string {
	for _, cleanup := range slices.Backward(r.cleanups) {
		cleanup()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.errs)
}

// TestPodExpectations checks the declarative layer: expectations claim Pods
// before ExpectPod, in registration order and up to their count; unmet ones
// and, in strict mode, unexpected Pods fail the test when it finishes.
func TestPodExpectations(t *testing.T) {
	t.Parallel()

	ctx, cp := newDedicatedPlane(t)
	cs := cp.Client()
	ns := newNamespace(ctx, t, cp)
	node := cp.AddNode(t, petri.NodeOptions{})

	rec := &recordingT{TB: t}

	cp.StrictPods(rec)

	first := cp.ExpectPods(rec, petri.InNamespace(ns), petri.Labeled("tier", "web")).Times(2).BindTo(node)
	second := cp.ExpectPods(rec, petri.InNamespace(ns), petri.Labeled("tier", "web"))
	cp.ExpectPods(rec, petri.InNamespace(ns), petri.Labeled("tier", "never"))

	for _, name := range []string{"w1", "w2", "w3"} {
		mustCreatePod(ctx, t, cs, ns, newNodePod(name, map[string]string{"tier": "web"}))
	}

	mustCreatePod(ctx, t, cs, ns, newNodePod("worker", map[string]string{"tier": "worker"}))
	mustCreatePod(ctx, t, cs, ns, newNodePod("stray", map[string]string{"tier": "stray"}))

	first.Wait(t)
	second.Wait(t)

	if n := len(first.Pods()); n != 2 {
		t.Errorf("first expectation claimed %d pods, want 2", n)
	}

	// The third web Pod went to the second expectation, which has no action:
	// it stays Pending.
	claimed := second.Pods()
	if len(claimed) != 1 {
		t.Fatalf("second expectation claimed %d pods, want 1", len(claimed))
	}

	for _, p := range first.Pods() {
		petri.WaitFor(t, cs.CoreV1().Pods(ns), p.Name, podReady())
	}

	// ExpectPod gets what no expectation wanted.
	worker := cp.ExpectPod(t, petri.InNamespace(ns), petri.Labeled("tier", "worker"))
	if worker.Name != "worker" {
		t.Errorf("ExpectPod returned %s, want worker", worker.Name)
	}

	errs := rec.finish()

	wantErrs := []string{
		"expected 1 Pods in namespace " + ns + ", labeled tier=never, claimed 0",
		"unexpected Pods: " + ns + "/stray",
	}
	for _, want := range wantErrs {
		if !slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, want) }) {
			t.Errorf("errors %q lack %q", errs, want)
		}
	}

	if len(errs) != len(wantErrs) {
		t.Errorf("errors = %q, want %d", errs, len(wantErrs))
	}

	// The Ctx variants report the same things to suites without a
	// testing.TB.
	if names := cp.UnexpectedPods(); !slices.Equal(names, []string{ns + "/stray"}) {
		t.Errorf("UnexpectedPods = %q, want only %s/stray", names, ns)
	}

	ghost, err := cp.ExpectPodsCtx(ctx, petri.InNamespace(ns), petri.Labeled("tier", "ghost"))
	if err != nil {
		t.Fatalf("ExpectPodsCtx: %v", err)
	}

	if err = ghost.Verify(ctx); err == nil || !strings.Contains(err.Error(), "claimed 0") {
		t.Errorf("Verify of an unmet expectation = %v", err)
	}
}

// TestNodesNeedADedicatedPlane checks that the shared control plane refuses
// Nodes and every way of placing Pods.
func TestNodesNeedADedicatedPlane(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)

	if _, err := env.AddNodeCtx(t.Context(), petri.NodeOptions{}); err == nil {
		t.Error("AddNode worked on the shared plane")
	}

	if _, err := env.ExpectPodCtx(t.Context()); err == nil {
		t.Error("ExpectPod worked on the shared plane")
	}

	err := env.BindCtx(t.Context(), &corev1.Pod{}, nil)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Errorf("Bind on the shared plane: %v", err)
	}
}

// TestWorkloadsRunOnNodes checks the workload controllers end to end on
// Nodes under kube-scheduler: a StatefulSet rolls out Pod after Pod, a
// DaemonSet places a Pod on every Node it tolerates, and a Job completes once
// the test completes its Pod.
func TestWorkloadsRunOnNodes(t *testing.T) {
	t.Parallel()

	ctx, cp := newDedicatedPlane(t)
	cp.EnableControllers(t, petri.SchedulerController, petri.StatefulSetController,
		petri.DaemonSetController, petri.JobController)

	cs := cp.Client()
	ns := newNamespace(ctx, t, cp)

	cp.AddNode(t, petri.NodeOptions{Name: "one"})
	cp.AddNode(t, petri.NodeOptions{Name: "two"})
	cp.AddNode(t, petri.NodeOptions{Name: "gpu", Taints: []corev1.Taint{gpuTaint()}})

	template := func(app string) corev1.PodTemplateSpec {
		return corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": app}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: app, Image: "nginx:1.0"}}},
		}
	}

	selector := func(app string) *metav1.LabelSelector {
		return &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}}
	}

	_, err := cs.AppsV1().StatefulSets(ns).Create(ctx, &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db"},
		Spec: appsv1.StatefulSetSpec{
			Replicas: new(int32(3)), Selector: selector("db"), Template: template("db"),
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create statefulset: %v", err)
	}

	_, err = cs.AppsV1().DaemonSets(ns).Create(ctx, &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "agent"},
		Spec:       appsv1.DaemonSetSpec{Selector: selector("agent"), Template: template("agent")},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create daemonset: %v", err)
	}

	jobTemplate := template("batch")
	jobTemplate.Spec.RestartPolicy = corev1.RestartPolicyNever

	_, err = cs.BatchV1().Jobs(ns).Create(ctx, &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "batch"},
		Spec:       batchv1.JobSpec{Template: jobTemplate},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	petri.WaitFor(t, cs.AppsV1().StatefulSets(ns), "db", func(s *appsv1.StatefulSet) (bool, error) {
		return s.Status.ReadyReplicas == 3, nil
	}, petri.WithTimeout(time.Minute))

	// The gpu Node's taint keeps the agent off it.
	petri.WaitFor(t, cs.AppsV1().DaemonSets(ns), "agent", func(d *appsv1.DaemonSet) (bool, error) {
		return d.Status.DesiredNumberScheduled == 2 && d.Status.NumberReady == 2, nil
	})

	var jobPod *corev1.Pod

	petri.Retry(t, func(ctx context.Context) (bool, error) {
		list, listErr := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=batch"})
		if listErr != nil || len(list.Items) == 0 || list.Items[0].Status.Phase != corev1.PodRunning {
			return false, listErr
		}

		jobPod = &list.Items[0]

		return true, nil
	})

	cp.CompletePod(t, jobPod)

	petri.WaitFor(t, cs.BatchV1().Jobs(ns), "batch",
		petri.StatusCondition[*batchv1.Job]("Complete", metav1.ConditionTrue))
}
