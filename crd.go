package petri

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// InstallCRDs installs CRDs from sources into the control plane and returns
// once every one is Established and its resources appear in discovery, so a
// RESTMapper built right afterwards resolves them.
//
// Sources are file and directory paths, an [fs.FS] (so //go:embed works), and
// *apiextensionsv1.CustomResourceDefinition objects. YAML and JSON manifests
// are read, including multi-document files; documents that are not CRDs are
// skipped, as envtest does.
//
// A CRD that is already installed with an equivalent spec, compared after the
// server's defaulting, is left alone; a different spec fails the test with a
// readable diff. CRDs are never deleted: other tests on the shared control
// plane may still be using them.
func (cp *ControlPlane) InstallCRDs(t testing.TB, sources ...any) {
	t.Helper()

	if err := cp.InstallCRDsCtx(t.Context(), sources...); err != nil {
		t.Fatal(err)
	}
}

// InstallCRDsCtx is InstallCRDs for callers that need the error instead of a
// failed test, such as Ginkgo suites and the envtest drop-in.
func (cp *ControlPlane) InstallCRDsCtx(ctx context.Context, sources ...any) error {
	crds, err := parseCRDs(sources)
	if err != nil {
		return err
	}
	if len(crds) == 0 {
		return nil
	}

	client, err := apiextensionsclient.NewForConfig(cp.Config())
	if err != nil {
		return fmt.Errorf("petri: build apiextensions client: %w", err)
	}

	names := make([]string, 0, len(crds))
	for _, crd := range crds {
		if installErr := installCRD(ctx, client, crd); installErr != nil {
			return installErr
		}

		names = append(names, crd.Name)
	}

	return waitCRDsReady(ctx, client, cp.config, crds, names)
}

// installCRD creates the CRD, or compares it against the one that is already
// there: the AlreadyExists race between parallel tests must succeed for
// equivalent specs and fail loudly for different ones.
func installCRD(
	ctx context.Context, client apiextensionsclient.Interface, crd *apiextensionsv1.CustomResourceDefinition,
) error {
	_, err := client.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("petri: create crd %s: %w", crd.Name, err)
	}

	stored, err := client.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("petri: get existing crd %s: %w", crd.Name, err)
	}

	if diff := crdSpecDiff(crd, stored); diff != "" {
		return fmt.Errorf(
			"petri: crd %s is already installed with a different spec (before vs after):\n%s", crd.Name, diff,
		)
	}

	return nil
}

// crdSpecDiff compares specs after defaulting both the way the server would,
// so a CRD never conflicts with its own source file just because the server
// filled in defaults the manifest left out. Status and metadata are ignored:
// status belongs to the server and metadata to whoever installed it.
func crdSpecDiff(incoming, stored *apiextensionsv1.CustomResourceDefinition) string {
	defaulted := incoming.DeepCopy()
	apiextensionsv1.SetObjectDefaults_CustomResourceDefinition(defaulted)

	return cmp.Diff(defaulted.Spec, stored.Spec, cmpopts.EquateEmpty())
}

// waitCRDsReady returns once every CRD reports Established and every served
// version's main resource appears in discovery. Querying discovery is what
// keeps a RESTMapper built right after this from being stale.
func waitCRDsReady(
	ctx context.Context, client apiextensionsclient.Interface, cfg *rest.Config,
	crds []*apiextensionsv1.CustomResourceDefinition, names []string,
) error {
	discovery, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("petri: build discovery client: %w", err)
	}

	ready := func(context.Context) (bool, error) {
		for _, crd := range crds {
			if !crdEstablished(ctx, client, crd.Name) || !crdInDiscovery(ctx, discovery, crd) {
				return false, nil
			}
		}

		return true, nil
	}

	if waitErr := RetryUntil(ctx, ready); waitErr != nil {
		return fmt.Errorf("petri: wait for %s to be established and served: %w", strings.Join(names, ", "), waitErr)
	}

	return nil
}

// crdEstablished reports whether the CRD's Established condition is true.
func crdEstablished(ctx context.Context, client apiextensionsclient.Interface, name string) bool {
	crd, err := client.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false
	}

	established, err := crdStatusCondition(crd, "Established")

	return err == nil && established
}

// crdStatusCondition reads a condition from the CRD status. The CRD condition
// type has no metav1.Condition accessor, so it is read by hand.
func crdStatusCondition(
	crd *apiextensionsv1.CustomResourceDefinition, condType string,
) (bool, error) {
	for _, condition := range crd.Status.Conditions {
		if string(condition.Type) == condType {
			return condition.Status == apiextensionsv1.ConditionTrue, nil
		}
	}

	return false, fmt.Errorf("petri: crd %s has no %s condition yet", crd.Name, condType)
}

// crdInDiscovery reports whether the CRD's main resource is listed for every
// served version.
func crdInDiscovery(
	_ context.Context, client *discovery.DiscoveryClient, crd *apiextensionsv1.CustomResourceDefinition,
) bool {
	for _, version := range crd.Spec.Versions {
		if !version.Served {
			continue
		}

		resources, err := client.ServerResourcesForGroupVersion(
			schema.GroupVersion{Group: crd.Spec.Group, Version: version.Name}.String(),
		)
		if err != nil {
			return false
		}

		found := false
		for _, resource := range resources.APIResources {
			if resource.Name == crd.Spec.Names.Plural {
				found = true

				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}
