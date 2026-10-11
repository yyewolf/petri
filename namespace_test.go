package petri_test

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

func TestNewNamespaceCreatesActiveRandomNamespace(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())

	name, err := env.NewNamespace(t.Context())
	if err != nil {
		t.Fatalf("NewNamespace: %v", err)
	}

	if !strings.HasPrefix(name, "petri-") {
		t.Errorf("namespace name = %q, want a petri- prefix without the test name", name)
	}
	if strings.Contains(name, strings.ToLower(strings.ReplaceAll(t.Name(), "/", ""))) {
		t.Errorf("namespace name = %q, want no test-name prefix", name)
	}

	ns, err := cs.CoreV1().Namespaces().Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if ns.Status.Phase != corev1.NamespaceActive {
		t.Errorf("phase = %q, want Active", ns.Status.Phase)
	}

	// Two calls must not collide on the random suffix.
	other, err := env.NewNamespace(t.Context())
	if err != nil {
		t.Fatalf("second NewNamespace: %v", err)
	}
	if other == name {
		t.Errorf("two NewNamespace calls returned the same name %q", name)
	}
}
