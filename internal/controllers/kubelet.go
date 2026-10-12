package controllers

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const (
	// KubeletName keys the kubelet simulator in the registry. It is petri's
	// own controller, not an upstream one: no kube-controller-manager flag
	// names it.
	KubeletName = "petri-kubelet"

	// SimulatedNodeAnnotation marks the Nodes petri created. The kubelet
	// simulator manages Pods on those Nodes only, so a Node a test created
	// through the API stays as inert as on a nodeless plane.
	SimulatedNodeAnnotation = "petri.yyewolf.github.io/simulated"

	// ContainerRuntime is the scheme of the container and image IDs the
	// simulator reports, and the runtime version Nodes advertise.
	ContainerRuntime = "petri"
)

// KubeletSpecs returns the registry spec of the kubelet simulator, which runs
// the Pods bound to petri's Nodes the way kwok does: no containers, only the
// status a kubelet would report.
func KubeletSpecs() []Spec {
	return []Spec{
		{Name: KubeletName, Build: buildKubelet},
	}
}

// kubelet is the simulator: one worker, so Pod IP allocation per Node needs
// no locking.
type kubelet struct {
	client kubernetes.Interface
	pods   corelisters.PodLister
	nodes  corelisters.NodeLister
	queue  workqueue.TypedRateLimitingInterface[cache.ObjectName]
	now    func() time.Time
}

func buildKubelet(_ context.Context, m *Manager) (Run, error) {
	client, err := m.KubeClient(KubeletName)
	if err != nil {
		return nil, err
	}

	factory := m.KubeFactory()
	gate := m.constructorGate()
	pods := gatedPods(factory.Core().V1().Pods(), gate)
	nodes := gatedNodes(factory.Core().V1().Nodes(), gate)

	k := &kubelet{
		client: client,
		pods:   pods.Lister(),
		nodes:  nodes.Lister(),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[cache.ObjectName](),
			workqueue.TypedRateLimitingQueueConfig[cache.ObjectName]{Name: KubeletName},
		),
		now: time.Now,
	}

	_, err = pods.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    k.enqueuePod,
		UpdateFunc: func(_, obj any) { k.enqueuePod(obj) },
	})
	if err != nil {
		return nil, fmt.Errorf("petri: watch pods for %s: %w", KubeletName, err)
	}

	// A Node that becomes Ready again resumes the Pods its kubelet ignored
	// while it was not.
	_, err = nodes.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    k.enqueueNodePods,
		UpdateFunc: func(_, obj any) { k.enqueueNodePods(obj) },
	})
	if err != nil {
		return nil, fmt.Errorf("petri: watch nodes for %s: %w", KubeletName, err)
	}

	return k.run, nil
}

func (k *kubelet) enqueuePod(obj any) {
	pod, ok := obj.(*corev1.Pod)
	if !ok || pod.Spec.NodeName == "" {
		return
	}

	k.queue.Add(cache.MetaObjectToName(pod))
}

func (k *kubelet) enqueueNodePods(obj any) {
	node, ok := obj.(*corev1.Node)
	if !ok || !simulated(node) || !NodeReady(node) {
		return
	}

	pods, err := k.pods.List(labels.Everything())
	if err != nil {
		utilruntime.HandleError(err)

		return
	}

	for _, pod := range pods {
		if pod.Spec.NodeName == node.Name {
			k.queue.Add(cache.MetaObjectToName(pod))
		}
	}
}

// run works the queue until ctx is cancelled, then shuts the queue down and
// waits for the worker.
func (k *kubelet) run(ctx context.Context) {
	done := make(chan struct{})

	go func() {
		defer close(done)

		wait.UntilWithContext(ctx, k.work, time.Second)
	}()

	<-ctx.Done()
	k.queue.ShutDown()
	<-done
}

func (k *kubelet) work(ctx context.Context) {
	for {
		key, quit := k.queue.Get()
		if quit {
			return
		}

		err := k.sync(ctx, key)
		if err != nil && ctx.Err() == nil {
			utilruntime.HandleErrorWithContext(ctx, err, "petri kubelet sync failed", "pod", key)
			k.queue.AddRateLimited(key)
		} else {
			k.queue.Forget(key)
		}

		k.queue.Done(key)
	}
}

// sync does what a kubelet would for one Pod: start it once it is bound, and
// finalize its deletion. A kubelet on a Node that is not Ready does nothing,
// so its Pods stay as they are, deletions included, until the Node is Ready.
func (k *kubelet) sync(ctx context.Context, key cache.ObjectName) error {
	pod, err := k.pods.Pods(key.Namespace).Get(key.Name)
	if apierrors.IsNotFound(err) {
		return nil
	}

	if err != nil {
		return err
	}

	if pod.Spec.NodeName == "" {
		return nil
	}

	node, err := k.nodes.Get(pod.Spec.NodeName)
	if apierrors.IsNotFound(err) {
		return nil
	}

	if err != nil {
		return err
	}

	if !simulated(node) || !NodeReady(node) {
		return nil
	}

	if pod.DeletionTimestamp != nil {
		return k.finalize(ctx, pod)
	}

	// The simulator moves each Pod out of Pending once and never again, so
	// a phase or readiness a test set afterwards sticks.
	if pod.Status.Phase != corev1.PodPending && pod.Status.Phase != "" {
		return nil
	}

	podIP, err := k.podIP(pod, node)
	if err != nil {
		return err
	}

	started := pod.DeepCopy()
	started.Status = RunningStatus(pod, node, podIP, metav1.NewTime(k.now()))

	_, err = k.client.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, started, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}

	return err
}

// finalize removes a deleted Pod, as a kubelet does once its containers have
// stopped: there are none here, so it happens at once.
func (k *kubelet) finalize(ctx context.Context, pod *corev1.Pod) error {
	zero := int64(0)
	uid := pod.UID

	err := k.client.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
		GracePeriodSeconds: &zero,
		Preconditions:      &metav1.Preconditions{UID: &uid},
	})
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}

	return err
}

// podIP picks the Pod's IP: the Node's own for host-network Pods, otherwise
// the lowest address in the Node's PodCIDR that no live Pod on it holds.
func (k *kubelet) podIP(pod *corev1.Pod, node *corev1.Node) (string, error) {
	if pod.Spec.HostNetwork {
		return NodeInternalIP(node), nil
	}

	prefix, err := netip.ParsePrefix(node.Spec.PodCIDR)
	if err != nil {
		return "", fmt.Errorf("petri: node %s pod CIDR %q: %w", node.Name, node.Spec.PodCIDR, err)
	}

	pods, err := k.pods.List(labels.Everything())
	if err != nil {
		return "", err
	}

	used := map[string]struct{}{}

	for _, other := range pods {
		if other.Spec.NodeName == node.Name && other.Status.PodIP != "" && !terminal(other) {
			used[other.Status.PodIP] = struct{}{}
		}
	}

	// Skip the network address and the gateway, as CNI plugins do.
	addr := prefix.Masked().Addr().Next().Next()
	for prefix.Contains(addr) {
		if _, taken := used[addr.String()]; !taken {
			return addr.String(), nil
		}

		addr = addr.Next()
	}

	return "", fmt.Errorf("petri: node %s pod CIDR %s is exhausted", node.Name, node.Spec.PodCIDR)
}

// RunningStatus is the status a kubelet reports for a Pod whose containers all
// started: init containers completed, sidecars and containers running and
// ready.
func RunningStatus(pod *corev1.Pod, node *corev1.Node, podIP string, now metav1.Time) corev1.PodStatus {
	hostIP := NodeInternalIP(node)
	status := corev1.PodStatus{
		Phase:     corev1.PodRunning,
		HostIP:    hostIP,
		HostIPs:   []corev1.HostIP{{IP: hostIP}},
		PodIP:     podIP,
		PodIPs:    []corev1.PodIP{{IP: podIP}},
		StartTime: &now,
		QOSClass:  pod.Status.QOSClass,
	}

	kubeletConditions := []corev1.PodConditionType{
		corev1.PodReadyToStartContainers, corev1.PodInitialized, corev1.ContainersReady, corev1.PodReady,
	}
	for _, kind := range kubeletConditions {
		status.Conditions = append(status.Conditions, corev1.PodCondition{
			Type: kind, Status: corev1.ConditionTrue, LastTransitionTime: now,
		})
	}

	// Keep the scheduler's condition, and any a test or controller added.
	for _, c := range pod.Status.Conditions {
		if !slices.Contains(kubeletConditions, c.Type) {
			status.Conditions = append(status.Conditions, c)
		}
	}

	for _, c := range pod.Spec.InitContainers {
		cs := containerStatus(pod, c, now)
		if c.RestartPolicy == nil || *c.RestartPolicy != corev1.ContainerRestartPolicyAlways {
			cs.Ready = false
			cs.Started = new(false)
			cs.State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 0, Reason: "Completed", StartedAt: now, FinishedAt: now, ContainerID: cs.ContainerID,
			}}
		}

		status.InitContainerStatuses = append(status.InitContainerStatuses, cs)
	}

	for _, c := range pod.Spec.Containers {
		status.ContainerStatuses = append(status.ContainerStatuses, containerStatus(pod, c, now))
	}

	return status
}

func containerStatus(pod *corev1.Pod, c corev1.Container, now metav1.Time) corev1.ContainerStatus {
	id := fmt.Sprintf("%s://%s-%s", ContainerRuntime, pod.UID, c.Name)

	return corev1.ContainerStatus{
		Name:        c.Name,
		Image:       c.Image,
		ImageID:     ContainerRuntime + "://" + c.Image,
		ContainerID: id,
		Ready:       true,
		Started:     new(true),
		State:       corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}},
	}
}

// NodeReady reports whether the Node's Ready condition is True.
func NodeReady(node *corev1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}

	return false
}

// NodeInternalIP returns the Node's first InternalIP address.
func NodeInternalIP(node *corev1.Node) string {
	for _, a := range node.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}

	return ""
}

func simulated(node *corev1.Node) bool {
	_, ok := node.Annotations[SimulatedNodeAnnotation]

	return ok
}

func terminal(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}
