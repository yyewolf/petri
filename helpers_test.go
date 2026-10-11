package petri_test

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func clientFor(t *testing.T, cfg *rest.Config) *kubernetes.Clientset {
	t.Helper()

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	return cs
}

func newDeployment(name string, containers ...corev1.Container) *appsv1.Deployment {
	labels := map[string]string{"app": name}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: containers},
			},
		},
	}
}
