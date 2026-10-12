package petri

import (
	"context"
	"fmt"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

// Bind places a Pending Pod on a Node through the pods/binding subresource,
// exactly as kube-scheduler does. The Node's kubelet then starts it. Bind
// needs a dedicated control plane without [SchedulerController].
func (cp *ControlPlane) Bind(t testing.TB, pod *corev1.Pod, node *Node) {
	t.Helper()

	if err := cp.BindCtx(t.Context(), pod, node); err != nil {
		t.Fatal(err)
	}
}

// BindCtx is Bind returning the error.
func (cp *ControlPlane) BindCtx(ctx context.Context, pod *corev1.Pod, node *Node) error {
	if err := cp.claimScheduling(schedulingManual); err != nil {
		return err
	}

	// A Pod bound by hand was expected by the test, whatever strict mode
	// thinks of Pods nobody claimed.
	if d := cp.pods.Load(); d != nil {
		d.markClaimed(pod.UID)
	}

	return cp.bind(ctx, pod, node.name)
}

func (cp *ControlPlane) bind(ctx context.Context, pod *corev1.Pod, nodeName string) error {
	binding := &corev1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace, UID: pod.UID},
		Target:     corev1.ObjectReference{Kind: "Node", Name: nodeName},
	}

	err := cp.Client().CoreV1().Pods(pod.Namespace).Bind(ctx, binding, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("petri: bind pod %s/%s to node %q: %w", pod.Namespace, pod.Name, nodeName, err)
	}

	return nil
}

// Evict evicts a Pod through the Eviction API, which honours
// PodDisruptionBudgets: an eviction a budget refuses fails the test. Use
// EvictCtx to assert on the refusal, a TooManyRequests error.
func (cp *ControlPlane) Evict(t testing.TB, pod *corev1.Pod) {
	t.Helper()

	if err := cp.EvictCtx(t.Context(), pod); err != nil {
		t.Fatal(err)
	}
}

// EvictCtx is Evict returning the error, which wraps the API error so
// apierrors.IsTooManyRequests reports a PodDisruptionBudget refusal.
func (cp *ControlPlane) EvictCtx(ctx context.Context, pod *corev1.Pod) error {
	eviction := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace}}

	err := cp.Client().PolicyV1().Evictions(pod.Namespace).Evict(ctx, eviction)
	if err != nil {
		return fmt.Errorf("petri: evict pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}

	return nil
}

// CompletePod ends a running Pod's containers with exit code 0 and moves the
// Pod to Succeeded, as a kubelet reports a finished Job Pod. The kubelet
// simulator never overrides it.
func (cp *ControlPlane) CompletePod(t testing.TB, pod *corev1.Pod) {
	t.Helper()

	if err := cp.CompletePodCtx(t.Context(), pod); err != nil {
		t.Fatal(err)
	}
}

// CompletePodCtx is CompletePod returning the error.
func (cp *ControlPlane) CompletePodCtx(ctx context.Context, pod *corev1.Pod) error {
	return cp.finishPod(ctx, pod, corev1.PodSucceeded, 0, "Completed")
}

// FailPod ends a running Pod's containers with exitCode, which must not be 0,
// and moves the Pod to Failed. The kubelet simulator never overrides it.
func (cp *ControlPlane) FailPod(t testing.TB, pod *corev1.Pod, exitCode int32) {
	t.Helper()

	if err := cp.FailPodCtx(t.Context(), pod, exitCode); err != nil {
		t.Fatal(err)
	}
}

// FailPodCtx is FailPod returning the error.
func (cp *ControlPlane) FailPodCtx(ctx context.Context, pod *corev1.Pod, exitCode int32) error {
	if exitCode == 0 {
		return fmt.Errorf("petri: fail pod %s/%s: exit code 0 is a success; use CompletePod", pod.Namespace, pod.Name)
	}

	return cp.finishPod(ctx, pod, corev1.PodFailed, exitCode, "Error")
}

func (cp *ControlPlane) finishPod(
	ctx context.Context, pod *corev1.Pod, phase corev1.PodPhase, exitCode int32, reason string,
) error {
	return cp.updatePodStatus(ctx, pod, func(p *corev1.Pod) {
		now := metav1.Now()
		p.Status.Phase = phase

		for i := range p.Status.ContainerStatuses {
			cs := &p.Status.ContainerStatuses[i]

			started := now
			if cs.State.Running != nil {
				started = cs.State.Running.StartedAt
			}

			cs.Ready = false
			cs.Started = new(false)
			cs.State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: exitCode, Reason: reason, StartedAt: started, FinishedAt: now, ContainerID: cs.ContainerID,
			}}
		}

		setPodCondition(p, corev1.ContainersReady, false, "PodCompleted", now)
		setPodCondition(p, corev1.PodReady, false, "PodCompleted", now)
	})
}

// SetPodReady sets a running Pod's readiness, its containers' included, as a
// readiness probe starting or stopping to pass would. The kubelet simulator
// never overrides it.
func (cp *ControlPlane) SetPodReady(t testing.TB, pod *corev1.Pod, ready bool) {
	t.Helper()

	if err := cp.SetPodReadyCtx(t.Context(), pod, ready); err != nil {
		t.Fatal(err)
	}
}

// SetPodReadyCtx is SetPodReady returning the error.
func (cp *ControlPlane) SetPodReadyCtx(ctx context.Context, pod *corev1.Pod, ready bool) error {
	return cp.updatePodStatus(ctx, pod, func(p *corev1.Pod) {
		now := metav1.Now()

		for i := range p.Status.ContainerStatuses {
			p.Status.ContainerStatuses[i].Ready = ready
		}

		reason := ""
		if !ready {
			reason = "ContainersNotReady"
		}

		setPodCondition(p, corev1.ContainersReady, ready, reason, now)
		setPodCondition(p, corev1.PodReady, ready, reason, now)
	})
}

// updatePodStatus applies mutate to the live Pod's status, retrying
// conflicts with the kubelet simulator and controllers.
func (cp *ControlPlane) updatePodStatus(ctx context.Context, pod *corev1.Pod, mutate func(*corev1.Pod)) error {
	pods := cp.Client().CoreV1().Pods(pod.Namespace)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		live, err := pods.Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		mutate(live)

		_, err = pods.UpdateStatus(ctx, live, metav1.UpdateOptions{})

		return err
	})
	if err != nil {
		return fmt.Errorf("petri: update pod %s/%s status: %w", pod.Namespace, pod.Name, err)
	}

	return nil
}

func setPodCondition(p *corev1.Pod, kind corev1.PodConditionType, value bool, reason string, now metav1.Time) {
	status := corev1.ConditionFalse
	if value {
		status = corev1.ConditionTrue
	}

	i := slices.IndexFunc(p.Status.Conditions, func(c corev1.PodCondition) bool { return c.Type == kind })
	if i < 0 {
		p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{
			Type: kind, Status: status, Reason: reason, LastTransitionTime: now,
		})

		return
	}

	c := &p.Status.Conditions[i]
	if c.Status != status {
		c.LastTransitionTime = now
	}

	c.Status = status
	c.Reason = reason
}
