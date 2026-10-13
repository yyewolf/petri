package controllers_test

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

func TestReplicaSetCreatesPendingPods(t *testing.T) {
	t.Parallel()

	cp := petri.Start(t)
	ns := cp.Namespace(t)
	cs := cp.Client()
	// Enabling a controller never enables another: Deployment alone would
	// create the ReplicaSet and stop there.
	cp.EnableControllers(t, petri.DeploymentController, petri.ReplicaSetController)

	labels := map[string]string{"app": "web"}
	_, err := cs.AppsV1().Deployments(ns).Create(t.Context(), &appsv1.Deployment{
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

	// The shared plane has no Nodes, so the three Pods exist but stay Pending.
	petri.Retry(t, func(ctx context.Context) (bool, error) {
		pods, listErr := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=web"})
		if listErr != nil {
			return false, listErr
		}
		for _, pod := range pods.Items {
			if pod.Status.Phase != corev1.PodPending {
				return false, nil
			}
		}

		return len(pods.Items) == 3, nil
	})
}
