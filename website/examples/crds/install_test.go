package crds_test

import (
	"embed"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/yyewolf/petri"
)

//go:embed testdata/*.yaml
var crdManifests embed.FS

func widgets() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
}

func TestWidgetSizeDefaultsToSmall(t *testing.T) {
	t.Parallel()

	cp := petri.Start(t)
	// Returns once the CRD is Established and served, so the new kind is
	// usable on the next line. Installing it again from another test is a
	// no-op.
	cp.InstallCRDs(t, crdManifests)

	client, err := dynamic.NewForConfig(cp.Config())
	if err != nil {
		t.Fatal(err)
	}
	widget := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"name": "w"},
		// Defaults apply inside objects that are present, so spec is sent
		// empty rather than left out.
		"spec": map[string]any{},
	}}
	created, err := client.Resource(widgets()).Namespace(cp.Namespace(t)).
		Create(t.Context(), widget, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// The API server applies the CRD's structural defaults.
	size, _, _ := unstructured.NestedString(created.Object, "spec", "size")
	if size != "small" {
		t.Fatalf("spec.size = %q, want the default %q", size, "small")
	}
}
