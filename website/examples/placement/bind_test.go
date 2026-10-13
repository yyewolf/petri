package placement_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

func TestPlaceAPodByHand(t *testing.T) {
	t.Parallel()

	cp := dedicatedPlane(t)
	ns := cp.Namespace(t)
	pods := cp.Client().CoreV1().Pods(ns)
	node := cp.AddNode(t, petri.NodeOptions{Name: "worker"})

	// Your code under test would create this Pod.
	_, err := pods.Create(t.Context(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "job-runner", Labels: map[string]string{"app": "runner"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "run", Image: "runner"}}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// ExpectPod waits for a matching Pending Pod and hands it over; Bind
	// places it with the pods/binding call kube-scheduler makes.
	pod := cp.ExpectPod(t, petri.InNamespace(ns), petri.Labeled("app", "runner"))
	cp.Bind(t, pod, node)

	petri.WaitFor(t, pods, pod.Name, func(p *corev1.Pod) (bool, error) {
		return p != nil && p.Status.Phase == corev1.PodRunning && p.Status.PodIP != "", nil
	})
}
