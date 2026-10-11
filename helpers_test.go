package petri_test

import (
	"testing"

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
