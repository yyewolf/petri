package example_test

import (
	"context"
	"crypto/rand"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/yyewolf/petri/controllerruntime"
)

// Widget is the API type matching the widgets.example.com CRD the suite
// installs from its manifests, with the tags a kubebuilder project generates.
type Widget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	Spec   WidgetSpec   `json:"spec"`
	Status WidgetStatus `json:"status"`
}

type WidgetSpec struct {
	Size string `json:"size,omitempty"`
}

type WidgetStatus struct {
	Phase string `json:"phase,omitempty"`
}

func (w *Widget) DeepCopyInto(out *Widget) {
	out.TypeMeta = w.TypeMeta
	w.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = w.Spec
	out.Status = w.Status
}

func (w *Widget) DeepCopyObject() runtime.Object {
	out := &Widget{}
	w.DeepCopyInto(out)

	return out
}

// WidgetList is the collection type the scheme needs for watches.
type WidgetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Widget `json:"items"`
}

func (l *WidgetList) DeepCopyObject() runtime.Object {
	out := &WidgetList{}
	out.TypeMeta = l.TypeMeta
	l.ListMeta.DeepCopyInto(&out.ListMeta)
	out.Items = append([]Widget(nil), l.Items...)

	return out
}

// widgetReconciler marks every widget Reconciled, the way a kubebuilder
// controller would.
type widgetReconciler struct {
	client client.Client
}

func (r *widgetReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	widget := &Widget{}
	if err := r.client.Get(ctx, req.NamespacedName, widget); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	if widget.Status.Phase == "Reconciled" {
		return reconcile.Result{}, nil
	}

	widget.Status.Phase = "Reconciled"

	return reconcile.Result{}, r.client.Status().Update(ctx, widget)
}

// TestWidgetReconcilerSetsPhase is the controller test: create a widget, wait
// for the reconciler to mark it.
func TestWidgetReconcilerSetsPhase(t *testing.T) {
	t.Parallel()

	mgr := controllerruntime.NewManager(t, testConfig, controllerruntime.WithScheme(widgetScheme))
	if err := builder.ControllerManagedBy(mgr).
		For(&Widget{}).
		Complete(&widgetReconciler{client: mgr.GetClient()}); err != nil {
		t.Fatalf("build controller: %v", err)
	}
	controllerruntime.StartManager(t, mgr)

	ns := makeNamespace(t)
	widget := &Widget{
		ObjectMeta: metav1.ObjectMeta{Name: "reconciled", Namespace: ns},
		Spec:       WidgetSpec{Size: "large"},
	}
	if err := k8sClient.Create(t.Context(), widget); err != nil {
		t.Fatalf("create widget: %v", err)
	}

	// Waits go through the direct client: the manager's cached client can
	// lag behind a creation made just before the wait.
	got := controllerruntime.WaitFor(t, k8sClient, widget.DeepCopyObject().(*Widget),
		func(w *Widget) (bool, error) { return w != nil && w.Status.Phase == "Reconciled", nil })
	if got.Status.Phase != "Reconciled" {
		t.Errorf("widget phase = %q, want Reconciled", got.Status.Phase)
	}
}

// makeNamespace creates a uniquely named namespace, like petri.Namespace does
// for tests that hold a control plane. Until petri runs the namespace
// controller, deleted namespaces stay Terminating, so the name carries a fresh
// random suffix every run.
func makeNamespace(t *testing.T) string {
	t.Helper()

	suffix := make([]byte, 5)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("draw namespace suffix: %v", err)
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	for i, b := range suffix {
		suffix[i] = alphabet[int(b)%len(alphabet)]
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "example-" + string(suffix)}}
	if err := k8sClient.Create(t.Context(), ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	return ns.Name
}
