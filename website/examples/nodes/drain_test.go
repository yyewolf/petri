package nodes_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/yyewolf/petri"
)

func TestBudgetBlocksEvictionUntilLifted(t *testing.T) {
	t.Parallel()

	cp := dedicatedPlane(t)
	ns := cp.Namespace(t)
	cs := cp.Client()
	node := cp.AddNode(t, petri.NodeOptions{})
	cp.EnableControllers(t, petri.DisruptionController)

	labels := map[string]string{"app": "db"}
	pod, err := cs.CoreV1().Pods(ns).Create(t.Context(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Labels: labels},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "db", Image: "postgres"}}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cp.Bind(t, pod, node)

	pdbs := cs.PolicyV1().PodDisruptionBudgets(ns)
	_, err = pdbs.Create(t.Context(), &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "db"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: new(intstr.FromInt32(1)),
			Selector:     &metav1.LabelSelector{MatchLabels: labels},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The disruption controller has counted the running Pod.
	petri.WaitFor(t, pdbs, "db", func(p *policyv1.PodDisruptionBudget) (bool, error) {
		return p != nil && p.Status.ObservedGeneration == p.Generation && p.Status.CurrentHealthy == 1, nil
	})

	// One Pod, one required: the Eviction API refuses, as on a cluster.
	err = cp.EvictCtx(t.Context(), pod)
	if !apierrors.IsTooManyRequests(err) {
		t.Fatalf("eviction under budget: %v, want TooManyRequests", err)
	}

	// Lift the budget and drain: cordon, then evict everything but DaemonSet
	// Pods. Drain retries evictions a budget refuses until its timeout.
	err = pdbs.Delete(t.Context(), "db", metav1.DeleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Drain(t)

	petri.WaitFor(t, cs.CoreV1().Pods(ns), "db", petri.Deleted[*corev1.Pod]())
}
