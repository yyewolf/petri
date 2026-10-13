package ginkgo_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/yyewolf/petri"
)

// cp is the suite's control plane. petri.Start and the helpers that take a
// [testing.TB] need a real [testing.T], which Ginkgo specs do not get, so the
// suite starts a dedicated plane and uses the context-taking variants.
var cp *petri.ControlPlane

func TestSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "petri with Ginkgo")
}

var _ = BeforeSuite(func(ctx SpecContext) {
	var err error
	cp, err = petri.New(ctx) // ctx bounds startup only
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(cp.Stop)

	Expect(cp.InstallCRDsCtx(ctx, "../crds/testdata")).To(Succeed())
})
