package quickstart_test

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

func TestDeploymentIsReconciled(t *testing.T) {
	t.Parallel()

	cp := petri.Start(t)  // the control plane shared by this test binary
	ns := cp.Namespace(t) // a fresh namespace, deleted when the test ends
	cs := cp.Client()     // an admin clientset; cp.Config() gives a *rest.Config
	cp.EnableControllers(t, petri.DeploymentController)

	labels := map[string]string{"app": "web"}
	deployments := cs.AppsV1().Deployments(ns)
	_, err := deployments.Create(t.Context(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "web", Image: "nginx"}},
				},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// The Deployment controller has seen the Deployment once it reports the
	// generation it acted on.
	petri.WaitFor(t, deployments, "web", func(d *appsv1.Deployment) (bool, error) {
		return d != nil && d.Status.ObservedGeneration == d.Generation, nil
	})
}
