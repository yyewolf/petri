package petri

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/util/retry"

	"github.com/yyewolf/petri/internal/controllers"
)

const (
	// nodePrefix names the Nodes AddNode creates without a name.
	nodePrefix = "petri-node-"
	// maxNodes bounds the Nodes of one control plane: each gets a /24 of
	// nodePodNetwork and an address in nodeNetwork.
	maxNodes = 256
	// drainRetryInterval spaces out evictions a PodDisruptionBudget refused.
	drainRetryInterval = 500 * time.Millisecond
	// nodeIPsPerOctet and firstNodeIP keep Node addresses in 172.18.x.2 to
	// 172.18.x.251.
	nodeIPsPerOctet = 250
	firstNodeIP     = 2
	// kubeletPort is the kubelet endpoint Nodes advertise; nothing listens.
	kubeletPort = 10250
)

// NodeOptions describes a Node for [ControlPlane.AddNode]. The zero value is
// a ready, schedulable Node with a generated name and default capacity.
type NodeOptions struct {
	// Name is the Node's name; empty picks petri-node-<random>.
	Name string
	// Labels are added to the well-known labels a kubelet sets
	// (kubernetes.io/hostname, kubernetes.io/os, kubernetes.io/arch), and
	// override them.
	Labels map[string]string
	// Annotations are added to the Node's annotations.
	Annotations map[string]string
	// Taints are the Node's taints from the start, so no Pod that does not
	// tolerate them is ever scheduled there.
	Taints []corev1.Taint
	// Capacity overrides the default capacity per resource: 32 CPUs, 256Gi
	// of memory, 1Ti of ephemeral storage and 110 Pods. Allocatable equals
	// capacity.
	Capacity corev1.ResourceList
	// Unschedulable creates the Node cordoned.
	Unschedulable bool
}

// Node is a simulated Node petri created on a dedicated control plane. Its
// kubelet is petri's simulator: Pods bound to it start at once and deleted
// Pods go away at once, while the Node is Ready. Use its methods to change it
// the way a cluster operator or a failing kubelet would.
type Node struct {
	cp   *ControlPlane
	name string
}

// Name returns the Node's name.
func (n *Node) Name() string {
	return n.name
}

// AddNode creates a simulated Node, ready and schedulable unless opts say
// otherwise, and deletes it when the test finishes. Pods reach it through
// kube-scheduler ([SchedulerController]) or through [ControlPlane.Bind].
//
// A Node belongs to the whole cluster, so AddNode only works on a dedicated
// control plane from New: the shared one refuses it.
func (cp *ControlPlane) AddNode(t testing.TB, opts NodeOptions) *Node {
	t.Helper()

	node, err := cp.AddNodeCtx(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()

		if delErr := node.DeleteCtx(ctx); delErr != nil && !cp.stoppedAlready() {
			t.Errorf("petri: delete node %q: %v", node.name, delErr)
		}
	})

	return node
}

// AddNodeCtx is AddNode for callers that manage their own lifecycle: the
// caller owns deletion.
func (cp *ControlPlane) AddNodeCtx(ctx context.Context, opts NodeOptions) (*Node, error) {
	if cp.shared {
		return nil, errors.New(
			"petri: Nodes belong to the whole cluster; use petri.New(ctx) for a dedicated control plane")
	}

	index, err := cp.nextNodeIndex()
	if err != nil {
		return nil, err
	}

	name := opts.Name
	if name == "" {
		name = nodePrefix + randomSuffix()
	}

	client := cp.Client()

	created, err := client.CoreV1().Nodes().Create(ctx, newNode(name, index, opts), metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("petri: create node %q: %w", name, err)
	}

	created.Status = nodeStatus(index, opts.Capacity, true)

	if _, err = client.CoreV1().Nodes().UpdateStatus(ctx, created, metav1.UpdateOptions{}); err != nil {
		return nil, fmt.Errorf("petri: report node %q ready: %w", name, err)
	}

	node := &Node{cp: cp, name: name}

	// The TaintNodesByCondition admission plugin taints every new Node
	// not-ready until the node lifecycle controller sees it Ready; petri
	// plays that controller.
	if err = node.update(ctx, func(n *corev1.Node) { syncReadyTaint(n, true) }); err != nil {
		return nil, err
	}

	if err = cp.manager.EnableControllers(controllers.KubeletName); err != nil {
		return nil, err
	}

	return node, nil
}

// nextNodeIndex hands out the network index of the plane's next Node.
func (cp *ControlPlane) nextNodeIndex() (int, error) {
	cp.nodesMu.Lock()
	defer cp.nodesMu.Unlock()

	if cp.nodeCount >= maxNodes {
		return 0, fmt.Errorf("petri: a control plane holds at most %d nodes", maxNodes)
	}

	cp.nodeCount++

	return cp.nodeCount - 1, nil
}

func newNode(name string, index int, opts NodeOptions) *corev1.Node {
	labels := map[string]string{
		corev1.LabelHostname:   name,
		corev1.LabelOSStable:   "linux",
		corev1.LabelArchStable: "amd64",
	}
	maps.Copy(labels, opts.Labels)

	annotations := map[string]string{controllers.SimulatedNodeAnnotation: "true"}
	maps.Copy(annotations, opts.Annotations)

	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, Annotations: annotations},
		Spec: corev1.NodeSpec{
			PodCIDR:       fmt.Sprintf("10.244.%d.0/24", index),
			PodCIDRs:      []string{fmt.Sprintf("10.244.%d.0/24", index)},
			Taints:        slices.Clone(opts.Taints),
			Unschedulable: opts.Unschedulable,
		},
	}
}

func nodeStatus(index int, capacity corev1.ResourceList, ready bool) corev1.NodeStatus {
	resources := corev1.ResourceList{ //nolint:exhaustive // The resources a kubelet reports; the rest are quota names.
		corev1.ResourceCPU:              resource.MustParse("32"),
		corev1.ResourceMemory:           resource.MustParse("256Gi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("1Ti"),
		corev1.ResourcePods:             resource.MustParse("110"),
	}
	maps.Copy(resources, capacity)

	now := metav1.Now()
	status := corev1.NodeStatus{
		Capacity:    resources,
		Allocatable: resources.DeepCopy(),
		Phase:       corev1.NodeRunning,
		Addresses: []corev1.NodeAddress{
			{
				Type:    corev1.NodeInternalIP,
				Address: fmt.Sprintf("172.18.%d.%d", index/nodeIPsPerOctet, index%nodeIPsPerOctet+firstNodeIP),
			},
		},
		DaemonEndpoints: corev1.NodeDaemonEndpoints{KubeletEndpoint: corev1.DaemonEndpoint{Port: kubeletPort}},
		NodeInfo: corev1.NodeSystemInfo{
			OperatingSystem:         "linux",
			Architecture:            "amd64",
			KubeletVersion:          "v" + KubernetesVersion + ".0-petri",
			ContainerRuntimeVersion: controllers.ContainerRuntime + "://" + KubernetesVersion,
		},
	}

	for _, pressure := range []corev1.NodeConditionType{
		corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure,
	} {
		status.Conditions = append(status.Conditions, corev1.NodeCondition{
			Type: pressure, Status: corev1.ConditionFalse, Reason: "PetriSimulated",
			LastHeartbeatTime: now, LastTransitionTime: now,
		})
	}

	status.Conditions = append(status.Conditions, readyCondition(ready, now))

	return status
}

func readyCondition(ready bool, now metav1.Time) corev1.NodeCondition {
	if ready {
		return corev1.NodeCondition{
			Type: corev1.NodeReady, Status: corev1.ConditionTrue, Reason: "KubeletReady",
			Message: "petri kubelet simulator is ready", LastHeartbeatTime: now, LastTransitionTime: now,
		}
	}

	return corev1.NodeCondition{
		Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady",
		Message: "marked not ready by the test", LastHeartbeatTime: now, LastTransitionTime: now,
	}
}

// syncReadyTaint adds or removes the not-ready NoSchedule taint the node
// lifecycle controller keeps in step with the Ready condition.
func syncReadyTaint(n *corev1.Node, ready bool) {
	n.Spec.Taints = slices.DeleteFunc(n.Spec.Taints, func(t corev1.Taint) bool {
		return t.Key == corev1.TaintNodeNotReady && t.Effect == corev1.TaintEffectNoSchedule
	})

	if !ready {
		n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{
			Key: corev1.TaintNodeNotReady, Effect: corev1.TaintEffectNoSchedule,
		})
	}
}

// update applies mutate to the Node's spec and metadata, retrying conflicts.
func (n *Node) update(ctx context.Context, mutate func(*corev1.Node)) error {
	nodes := n.cp.Client().CoreV1().Nodes()

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := nodes.Get(ctx, n.name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		mutate(node)

		_, err = nodes.Update(ctx, node, metav1.UpdateOptions{})

		return err
	})
	if err != nil {
		return fmt.Errorf("petri: update node %q: %w", n.name, err)
	}

	return nil
}

// Taint adds taints to the Node, replacing any with the same key and effect.
// A NoExecute taint does not evict anything: petri runs no taint-eviction
// controller.
func (n *Node) Taint(t testing.TB, taints ...corev1.Taint) {
	t.Helper()

	if err := n.TaintCtx(t.Context(), taints...); err != nil {
		t.Fatal(err)
	}
}

// TaintCtx is Taint returning the error.
func (n *Node) TaintCtx(ctx context.Context, taints ...corev1.Taint) error {
	return n.update(ctx, func(node *corev1.Node) {
		node.Spec.Taints = slices.DeleteFunc(node.Spec.Taints, func(have corev1.Taint) bool {
			return slices.ContainsFunc(taints, func(want corev1.Taint) bool { return have.MatchTaint(&want) })
		})
		node.Spec.Taints = append(node.Spec.Taints, taints...)
	})
}

// Untaint removes the Node's taints matching the given ones by key and
// effect; their values are ignored.
func (n *Node) Untaint(t testing.TB, taints ...corev1.Taint) {
	t.Helper()

	if err := n.UntaintCtx(t.Context(), taints...); err != nil {
		t.Fatal(err)
	}
}

// UntaintCtx is Untaint returning the error.
func (n *Node) UntaintCtx(ctx context.Context, taints ...corev1.Taint) error {
	return n.update(ctx, func(node *corev1.Node) {
		node.Spec.Taints = slices.DeleteFunc(node.Spec.Taints, func(have corev1.Taint) bool {
			return slices.ContainsFunc(taints, func(want corev1.Taint) bool { return have.MatchTaint(&want) })
		})
	})
}

// Cordon marks the Node unschedulable, as kubectl cordon does.
func (n *Node) Cordon(t testing.TB) {
	t.Helper()

	if err := n.CordonCtx(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// CordonCtx is Cordon returning the error.
func (n *Node) CordonCtx(ctx context.Context) error {
	return n.update(ctx, func(node *corev1.Node) { node.Spec.Unschedulable = true })
}

// Uncordon marks the Node schedulable again.
func (n *Node) Uncordon(t testing.TB) {
	t.Helper()

	if err := n.UncordonCtx(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// UncordonCtx is Uncordon returning the error.
func (n *Node) UncordonCtx(ctx context.Context) error {
	return n.update(ctx, func(node *corev1.Node) { node.Spec.Unschedulable = false })
}

// SetReady sets the Node's Ready condition, as a kubelet that stopped or
// resumed reporting would, and keeps the not-ready NoSchedule taint in step,
// as the node lifecycle controller does. While the Node is not Ready its
// kubelet does nothing: bound Pods do not start and deleted Pods stay
// Terminating. Nothing is evicted: petri runs no taint-eviction controller.
func (n *Node) SetReady(t testing.TB, ready bool) {
	t.Helper()

	if err := n.SetReadyCtx(t.Context(), ready); err != nil {
		t.Fatal(err)
	}
}

// SetReadyCtx is SetReady returning the error.
func (n *Node) SetReadyCtx(ctx context.Context, ready bool) error {
	nodes := n.cp.Client().CoreV1().Nodes()

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := nodes.Get(ctx, n.name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		now := metav1.Now()
		condition := readyCondition(ready, now)

		i := slices.IndexFunc(node.Status.Conditions, func(c corev1.NodeCondition) bool {
			return c.Type == corev1.NodeReady
		})
		if i < 0 {
			node.Status.Conditions = append(node.Status.Conditions, condition)
		} else {
			if node.Status.Conditions[i].Status == condition.Status {
				condition.LastTransitionTime = node.Status.Conditions[i].LastTransitionTime
			}

			node.Status.Conditions[i] = condition
		}

		_, err = nodes.UpdateStatus(ctx, node, metav1.UpdateOptions{})

		return err
	})
	if err != nil {
		return fmt.Errorf("petri: set node %q ready=%t: %w", n.name, ready, err)
	}

	return n.update(ctx, func(node *corev1.Node) { syncReadyTaint(node, ready) })
}

// Drain cordons the Node and evicts its Pods through the Eviction API, as
// kubectl drain --ignore-daemonsets does, retrying evictions a
// PodDisruptionBudget refuses. It returns once the evicted Pods are gone,
// failing the test after the wait timeout.
func (n *Node) Drain(t testing.TB, opts ...WaitOption) {
	t.Helper()

	if err := n.DrainCtx(t.Context(), opts...); err != nil {
		t.Fatal(err)
	}
}

// DrainCtx is Drain returning the error.
func (n *Node) DrainCtx(ctx context.Context, opts ...WaitOption) error {
	if err := n.CordonCtx(ctx); err != nil {
		return err
	}

	cfg := newWaitConfig(opts)

	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	var last error

	for {
		pods, err := n.pods(ctx)
		if err != nil {
			return err
		}

		pods = slices.DeleteFunc(pods, ownedByDaemonSet)
		if len(pods) == 0 {
			return nil
		}

		last = n.evict(ctx, pods)
		if last != nil && !errors.Is(last, errEvictionRefused) {
			return last
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("petri: drain node %q: %d pods left: %w", n.name, len(pods), errors.Join(ctx.Err(), last))
		case <-time.After(drainRetryInterval):
		}
	}
}

// errEvictionRefused marks an eviction a PodDisruptionBudget refused, which
// Drain retries.
var errEvictionRefused = errors.New("a PodDisruptionBudget refused the eviction")

// evict evicts the Pods not already deleting. It returns the last refusal
// from a PodDisruptionBudget, wrapping errEvictionRefused, and fails on any
// other error.
func (n *Node) evict(ctx context.Context, pods []corev1.Pod) error {
	evictions := n.cp.Client().PolicyV1()

	var refused error

	for _, pod := range pods {
		if pod.DeletionTimestamp != nil {
			continue
		}

		eviction := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace}}

		err := evictions.Evictions(pod.Namespace).Evict(ctx, eviction)

		switch {
		case err == nil || apierrors.IsNotFound(err):
		case apierrors.IsTooManyRequests(err):
			refused = fmt.Errorf("%w: pod %s/%s: %w", errEvictionRefused, pod.Namespace, pod.Name, err)
		default:
			return fmt.Errorf("petri: drain node %q: evict pod %s/%s: %w", n.name, pod.Namespace, pod.Name, err)
		}
	}

	return refused
}

// Delete deletes the Node and force-deletes the Pods bound to it, as the Pod
// garbage collector does for Pods on a Node that no longer exists.
func (n *Node) Delete(t testing.TB) {
	t.Helper()

	if err := n.DeleteCtx(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// DeleteCtx is Delete returning the error. Deleting a Node that is already
// gone succeeds.
func (n *Node) DeleteCtx(ctx context.Context) error {
	client := n.cp.Client()

	err := client.CoreV1().Nodes().Delete(ctx, n.name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("petri: delete node %q: %w", n.name, err)
	}

	pods, err := n.pods(ctx)
	if err != nil {
		return err
	}

	zero := int64(0)

	for _, pod := range pods {
		err = client.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("petri: delete pod %s/%s of node %q: %w", pod.Namespace, pod.Name, n.name, err)
		}
	}

	return nil
}

// pods lists the Pods bound to the Node.
func (n *Node) pods(ctx context.Context) ([]corev1.Pod, error) {
	list, err := n.cp.Client().CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", n.name).String(),
	})
	if err != nil {
		return nil, fmt.Errorf("petri: list pods of node %q: %w", n.name, err)
	}

	return list.Items, nil
}

func ownedByDaemonSet(pod corev1.Pod) bool {
	owner := metav1.GetControllerOf(&pod)

	return owner != nil && owner.Kind == "DaemonSet"
}
