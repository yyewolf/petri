package controlplanes_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

// Both tests run in parallel against the one control plane petri.Start shares
// with the whole test binary. Each works in its own namespace, so neither sees
// the other's objects.

func TestTeamA(t *testing.T) {
	t.Parallel()
	seesOnlyItsOwnConfigMap(t, "team-a")
}

func TestTeamB(t *testing.T) {
	t.Parallel()
	seesOnlyItsOwnConfigMap(t, "team-b")
}

func seesOnlyItsOwnConfigMap(t *testing.T, name string) {
	t.Helper()

	cp := petri.Start(t)
	configMaps := cp.Client().CoreV1().ConfigMaps(cp.Namespace(t))

	_, err := configMaps.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"example": "shared"}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	list, err := configMaps.List(t.Context(), metav1.ListOptions{LabelSelector: "example=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Name != name {
		t.Fatalf("listed %d ConfigMaps, want only %s", len(list.Items), name)
	}
}
