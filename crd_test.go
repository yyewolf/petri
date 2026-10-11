package petri_test

import (
	"embed"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"

	"github.com/yyewolf/petri"
)

//go:embed testdata/crds
var crdFiles embed.FS

func crdSource() *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "clashes.example.com"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.com",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "clashes", Singular: "clash", Kind: "Clash",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name:    "v1",
				Served:  true,
				Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"spec": {Type: "object"},
						},
					},
				},
			}},
		},
	}
}

func TestInstallCRDsFromDirectoryMakesResourcesResolvable(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	env.InstallCRDs(t, "testdata/crds")

	dc, err := discovery.NewDiscoveryClientForConfig(env.Config())
	if err != nil {
		t.Fatalf("build discovery client: %v", err)
	}

	// A RESTMapper built right after InstallCRDs must resolve the new kinds;
	// a stale discovery or mapper is the known envtest failure.
	groups, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		t.Fatalf("discover groups: %v", err)
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groups)

	mapping, err := mapper.RESTMapping(schema.GroupKind{Group: "example.com", Kind: "Widget"}, "v1")
	if err != nil {
		t.Fatalf("RESTMapping Widget: %v", err)
	}
	if mapping.Resource.Resource != "widgets" {
		t.Errorf("mapping resource = %q, want widgets", mapping.Resource.Resource)
	}

	// The resource must not just be discoverable but servable: create one
	// right away through the dynamic client.
	dyn, err := dynamic.NewForConfig(env.Config())
	if err != nil {
		t.Fatalf("build dynamic client: %v", err)
	}
	ns := env.Namespace(t)

	widget := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"name": "w"},
		"spec":       map[string]any{},
	}}
	created, err := dyn.Resource(mapping.Resource).Namespace(ns).Create(t.Context(), widget, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create widget right after InstallCRDs: %v", err)
	}
	if got := created.Object["spec"].(map[string]any)["size"]; got != "small" {
		t.Errorf("defaulted spec.size = %v, want small", got)
	}

	// The second CRD of the multi-document file is served too, and the
	// ConfigMap document was skipped rather than installed.
	gadget := schema.GroupKind{Group: "example.com", Kind: "Gadget"}
	if _, mapErr := mapper.RESTMapping(gadget, "v1beta1"); mapErr != nil {
		t.Errorf("RESTMapping Gadget: %v", mapErr)
	}
	_, err = clientFor(t, env.Config()).CoreV1().ConfigMaps(metav1.NamespaceDefault).
		Get(t.Context(), "not-a-crd", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("the skipped ConfigMap document appeared: %v", err)
	}
}

func TestInstallCRDsFromEmbedFSAndJSON(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)

	fsys := fstest.MapFS{
		"json/gadgets.json": &fstest.MapFile{Data: []byte(`{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind": "CustomResourceDefinition",
			"metadata": {"name": "thingies.example.com"},
			"spec": {
				"group": "example.com",
				"names": {"plural": "thingies", "singular": "thingy", "kind": "Thingy"},
				"scope": "Namespaced",
				"versions": [{
					"name": "v1", "served": true, "storage": true,
					"schema": {"openAPIV3Schema": {"type": "object", "x-kubernetes-preserve-unknown-fields": true}}
				}]
			}
		}`)},
	}

	env.InstallCRDs(t, crdFiles, fsys)

	dc, err := discovery.NewDiscoveryClientForConfig(env.Config())
	if err != nil {
		t.Fatalf("build discovery client: %v", err)
	}
	groups, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		t.Fatalf("discover groups: %v", err)
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groups)

	widget := schema.GroupKind{Group: "example.com", Kind: "Widget"}
	if _, mapErr := mapper.RESTMapping(widget, "v1"); mapErr != nil {
		t.Errorf("RESTMapping Widget from embedded files: %v", mapErr)
	}
	thingy := schema.GroupKind{Group: "example.com", Kind: "Thingy"}
	if _, mapErr := mapper.RESTMapping(thingy, "v1"); mapErr != nil {
		t.Errorf("RESTMapping Thingy from MapFS JSON: %v", mapErr)
	}
}

func TestInstallCRDsIsIdempotent(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	env.InstallCRDs(t, "testdata/crds")

	// Installing the same manifests again must be a no-op: the stored spec is
	// compared after the server's defaulting, not against the raw input.
	env.InstallCRDs(t, "testdata/crds")

	crdObj := crdSource()
	if err := env.InstallCRDsCtx(t.Context(), crdObj); err != nil {
		t.Fatalf("install the same object twice: %v", err)
	}
}

func TestInstallCRDsConcurrentlyOnSharedPlane(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)

	const installers = 5
	errs := make([]error, installers)
	var wg sync.WaitGroup
	for i := range installers {
		wg.Go(func() {
			errs[i] = env.InstallCRDsCtx(t.Context(), "testdata/crds")
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("installer #%d: %v", i, err)
		}
	}
}

func TestInstallCRDsConflictingSpecFailsWithDiff(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)

	if err := env.InstallCRDsCtx(t.Context(), crdSource()); err != nil {
		t.Fatalf("first install: %v", err)
	}

	clash := crdSource()
	clash.Spec.Names.Kind = "Sprocket"

	err := env.InstallCRDsCtx(t.Context(), clash)
	if err == nil {
		t.Fatal("installing a different spec under the same name succeeded, want an error")
	}
	for _, want := range []string{"clashes.example.com", "Kind", "Clash", "Sprocket"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("conflict error %q does not mention %q", err, want)
		}
	}
}

func TestInstallCRDsFailsClearlyOnMissingPath(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)

	err := env.InstallCRDsCtx(t.Context(), "testdata/no-such-directory")
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing path error = %v, want a not-exist error", err)
	}
	if !strings.Contains(err.Error(), "testdata/no-such-directory") {
		t.Errorf("error %q does not name the missing path", err)
	}
}

func TestInstallCRDsFailsWhenSourcesHaveNoCRDs(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)

	empty := fstest.MapFS{"readme.txt": &fstest.MapFile{Data: []byte("not a manifest")}}
	if err := env.InstallCRDsCtx(t.Context(), empty); err == nil {
		t.Fatal("installing from a source without CRDs succeeded, want an error")
	}
}

func TestInstallCRDsRejectsUnknownSource(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)

	err := env.InstallCRDsCtx(t.Context(), 42)
	if err == nil || !strings.Contains(err.Error(), "int") {
		t.Fatalf("unknown source error = %v, want a message naming int", err)
	}
}
