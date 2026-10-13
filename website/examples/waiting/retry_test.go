package waiting_test

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

func TestDeploymentGetsAReplicaSet(t *testing.T) {
	t.Parallel()

	cp := petri.Start(t)
	ns := cp.Namespace(t)
	cs := cp.Client()
	cp.EnableControllers(t, petri.DeploymentController)

	labels := map[string]string{"app": "api"}
	_, err := cs.AppsV1().Deployments(ns).Create(t.Context(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "api"}}},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// The ReplicaSet's name is generated, so there is no one object to watch:
	// Retry polls a condition of your own until it holds.
	petri.Retry(t, func(ctx context.Context) (bool, error) {
		list, listErr := cs.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=api"})
		if listErr != nil {
			return false, listErr
		}

		return len(list.Items) == 1, nil
	})
}
