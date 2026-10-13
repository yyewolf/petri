package nodes_test

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

func TestNotReadyNodeHoldsItsPods(t *testing.T) {
	t.Parallel()

	cp := dedicatedPlane(t)
	ns := cp.Namespace(t)
	pods := cp.Client().CoreV1().Pods(ns)
	node := cp.AddNode(t, petri.NodeOptions{})

	// The kubelet stops reporting: the Node is not Ready and gets the
	// not-ready taint. Nothing is evicted; petri runs no node lifecycle
	// controller.
	node.SetReady(t, false)

	pod, err := pods.Create(t.Context(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app"}}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cp.Bind(t, pod, node)

	// To check that something does not happen, wait for it briefly and expect
	// the wait to time out.
	_, err = petri.WaitUntil(t.Context(), pods, "app", running, petri.WithTimeout(2*time.Second))
	if err == nil {
		t.Fatal("pod started on a NotReady node")
	}

	// Back to Ready: the kubelet catches up and starts the Pod.
	node.SetReady(t, true)
	petri.WaitFor(t, pods, "app", running)
}

func running(p *corev1.Pod) (bool, error) {
	return p != nil && p.Status.Phase == corev1.PodRunning, nil
}
