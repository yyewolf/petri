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

	BeforeEach(func(ctx SpecContext) {
		ns, err := cp.NewNamespace(ctx)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) error {
			return cp.Client().CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
		})

		configMaps = cp.Client().CoreV1().ConfigMaps(ns)
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

		_, err = petri.WaitUntil(ctx, configMaps, "dependent", petri.Deleted[*corev1.ConfigMap]())
		Expect(err).NotTo(HaveOccurred())
	})
})
