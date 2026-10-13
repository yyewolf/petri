package waiting_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

func TestDependentIsCollected(t *testing.T) {
	t.Parallel()

	cp := petri.Start(t)
	configMaps := cp.Client().CoreV1().ConfigMaps(cp.Namespace(t))

	owner, err := configMaps.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "owner"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = configMaps.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "dependent",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "ConfigMap", Name: owner.Name, UID: owner.UID,
			}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	err = configMaps.Delete(t.Context(), owner.Name, metav1.DeleteOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// The garbage collector runs on every plane, so the dependent goes too.
	petri.WaitFor(t, configMaps, "dependent", petri.Deleted[*corev1.ConfigMap]())
}
