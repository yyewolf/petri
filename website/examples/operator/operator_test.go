package operator_test

import (
	"context"
	"embed"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/yyewolf/petri"
	petricr "github.com/yyewolf/petri/controllerruntime"
)

//go:embed testdata/widgets.yaml
var crds embed.FS

func widgetKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Widget"}
}

// widgetReconciler is the operator under test: every Widget gets a Deployment.
type widgetReconciler struct {
	client.Client
}

func (r *widgetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	widget := &unstructured.Unstructured{}
	widget.SetGroupVersionKind(widgetKind())
	if err := r.Get(ctx, req.NamespacedName, widget); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	labels := map[string]string{"widget": widget.GetName()}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: widget.GetName(), Namespace: widget.GetNamespace(),
	}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		deployment.Spec = appsv1.DeploymentSpec{
			Replicas: new(int32(2)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "widget", Image: "widget"}}},
			},
		}

		return controllerutil.SetControllerReference(widget, deployment, r.Scheme())
	})

	return ctrl.Result{}, err
}

func TestWidgetBecomesARunningDeployment(t *testing.T) {
	t.Parallel()

	// A dedicated plane: this test adds a Node and runs the scheduler.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stopErr := cp.Stop(); stopErr != nil {
			t.Error(stopErr)
		}
	})

	// The cluster the operator runs in: its CRD, one Node, and the controllers
	// that turn a Deployment into running Pods.
	cp.InstallCRDs(t, crds)
	cp.AddNode(t, petri.NodeOptions{})
	cp.EnableControllers(t,
		petri.DeploymentController, petri.ReplicaSetController, petri.SchedulerController)

	// The operator, as its main would start it.
	mgr := petricr.NewManager(t, cp.Config())
	widgets := &unstructured.Unstructured{}
	widgets.SetGroupVersionKind(widgetKind())
	err = ctrl.NewControllerManagedBy(mgr).
		For(widgets).
		Owns(&appsv1.Deployment{}).
		Complete(&widgetReconciler{Client: mgr.GetClient()})
	if err != nil {
		t.Fatal(err)
	}
	petricr.StartManager(t, mgr)

	// The user creates a Widget...
	ns := cp.Namespace(t)
	c, err := client.NewWithWatch(cp.Config(), client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	widget := &unstructured.Unstructured{}
	widget.SetGroupVersionKind(widgetKind())
	widget.SetName("blue")
	widget.SetNamespace(ns)
	err = c.Create(t.Context(), widget)
	if err != nil {
		t.Fatal(err)
	}

	// ...and gets a Deployment whose Pods were scheduled and started.
	petricr.WaitFor(t, c, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "blue", Namespace: ns}},
		func(d *appsv1.Deployment) (bool, error) {
			return d != nil && d.Status.AvailableReplicas == 2, nil
		})
}
