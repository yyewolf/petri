package nodes_test

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

func TestSchedulerKeepsWebOffTheGPUNode(t *testing.T) {
	t.Parallel()

	cp := dedicatedPlane(t)
	ns := cp.Namespace(t)
	cs := cp.Client()

	general := cp.AddNode(t, petri.NodeOptions{Name: "general"})
	cp.AddNode(t, petri.NodeOptions{
		Name:   "gpu",
		Labels: map[string]string{"pool": "gpu"},
		Taints: []corev1.Taint{{Key: "gpu", Effect: corev1.TaintEffectNoSchedule}},
	})
	cp.EnableControllers(t,
		petri.SchedulerController, petri.DeploymentController, petri.ReplicaSetController)

	labels := map[string]string{"app": "web"}
	deployments := cs.AppsV1().Deployments(ns)
	_, err := deployments.Create(t.Context(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web"},
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(2)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "nginx"}}},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Scheduled, started by the simulated kubelet, counted by the controllers.
	petri.WaitFor(t, deployments, "web",
		petri.StatusCondition[*appsv1.Deployment]("Available", metav1.ConditionTrue))

	pods, err := cs.CoreV1().Pods(ns).List(t.Context(), metav1.ListOptions{LabelSelector: "app=web"})
	if err != nil {
		t.Fatal(err)
	}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != general.Name() {
			t.Errorf("pod %s runs on %s; it does not tolerate the gpu taint", pod.Name, pod.Spec.NodeName)
		}
	}
}
