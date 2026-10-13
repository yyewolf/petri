package ginkgo_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/yyewolf/petri"
)

var _ = Describe("Owner references", func() {
	var configMaps typedcorev1.ConfigMapInterface

	BeforeEach(func() {
		// GinkgoTB adapts the running spec to testing.TB, so petri's helpers
		// work as in a plain test: the shared plane, and a namespace that is
		// deleted when the spec ends.
		t := GinkgoTB()
		cp := petri.Start(t)
		configMaps = cp.Client().CoreV1().ConfigMaps(cp.Namespace(t))
	})

	It("cascade to dependents", func(ctx SpecContext) {
		owner, err := configMaps.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "owner"},
		}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		_, err = configMaps.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: "dependent",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "v1", Kind: "ConfigMap", Name: owner.Name, UID: owner.UID,
				}},
			},
		}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		Expect(configMaps.Delete(ctx, owner.Name, metav1.DeleteOptions{})).To(Succeed())

		petri.WaitFor(GinkgoTB(), configMaps, "dependent", petri.Deleted[*corev1.ConfigMap]())
	})
})
