package envtest_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Namespaces", func() {
	It("go away when deleted, unlike with envtest", func(ctx SpecContext) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "scratch-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		Expect(k8sClient.Delete(ctx, ns)).To(Succeed())

		// envtest runs no namespace controller, so there the namespace would
		// stay Terminating forever. petri runs it by default.
		Eventually(func(ctx SpecContext) error {
			return k8sClient.Get(ctx, client.ObjectKeyFromObject(ns), &corev1.Namespace{})
		}).WithContext(ctx).Should(Satisfy(apierrors.IsNotFound))
	})
})
