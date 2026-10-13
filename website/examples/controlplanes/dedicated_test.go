package controlplanes_test

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

func TestEnvtestSemantics(t *testing.T) {
	t.Parallel()

	// A dedicated plane belongs to this test alone. The context bounds startup
	// only; the plane runs until Stop.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	cp, err := petri.New(ctx, petri.WithoutDefaultControllers())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stopErr := cp.Stop(); stopErr != nil {
			t.Error(stopErr)
		}
	})

	ns := cp.Namespace(t)
	namespaces := cp.Client().CoreV1().Namespaces()
	err = namespaces.Delete(t.Context(), ns, metav1.DeleteOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Without the namespace controller, nothing empties and removes a deleted
	// namespace: it stays Terminating, as it would with envtest.
	petri.WaitFor(t, namespaces, ns, func(n *corev1.Namespace) (bool, error) {
		return n != nil && n.Status.Phase == corev1.NamespaceTerminating, nil
	})
}
