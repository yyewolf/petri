package crds_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/yyewolf/petri"
)

func TestWaitForAWidgetCondition(t *testing.T) {
	t.Parallel()

	cp := petri.Start(t)
	cp.InstallCRDs(t, crdManifests)

	client, err := dynamic.NewForConfig(cp.Config())
	if err != nil {
		t.Fatal(err)
	}
	resource := client.Resource(widgets()).Namespace(cp.Namespace(t))

	widget, err := resource.Create(t.Context(), &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"name": "w"},
	}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Your controller would set this; the test plays its part.
	conditions := []any{map[string]any{"type": "Ready", "status": "True", "reason": "Built"}}
	err = unstructured.SetNestedSlice(widget.Object, conditions, "status", "conditions")
	if err != nil {
		t.Fatal(err)
	}
	_, err = resource.UpdateStatus(t.Context(), widget, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// DynamicResource lets the wait helpers follow kinds with no typed client.
	petri.WaitFor(t, petri.DynamicResource(resource), "w",
		petri.StatusCondition[*unstructured.Unstructured]("Ready", metav1.ConditionTrue))
}
