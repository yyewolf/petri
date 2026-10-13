package placement_test

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

func TestDeploymentRunsOnTheNodesWeChoose(t *testing.T) {
	t.Parallel()

	cp := dedicatedPlane(t)
	ns := cp.Namespace(t)
	deployments := cp.Client().AppsV1().Deployments(ns)
	node := cp.AddNode(t, petri.NodeOptions{})
	cp.EnableControllers(t, petri.DeploymentController, petri.ReplicaSetController)

	// Like gomock: declare what must happen before it happens. Each matching
	// Pod is claimed as it arrives and bound to the node. If fewer than three
	// arrive, the test fails when it ends; StrictPods also fails it for any
	// Pod nobody expected.
	web := cp.ExpectPods(t, petri.InNamespace(ns), petri.Labeled("app", "web")).Times(3).BindTo(node)
	cp.StrictPods(t)

	labels := map[string]string{"app": "web"}
	_, err := deployments.Create(t.Context(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web"},
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(3)),
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

	web.Wait(t) // all three claimed and bound
	petri.WaitFor(t, deployments, "web",
		petri.StatusCondition[*appsv1.Deployment]("Available", metav1.ConditionTrue))
}
