package manager_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/yyewolf/petri"
	petricr "github.com/yyewolf/petri/controllerruntime"
)

// stamper is the reconciler under test: it marks every ConfigMap it sees.
type stamper struct {
	client.Client
}

func (s *stamper) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cm := &corev1.ConfigMap{}
	if err := s.Get(ctx, req.NamespacedName, cm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if cm.Annotations["stamped"] == "true" {
		return ctrl.Result{}, nil
	}

	patch := client.MergeFrom(cm.DeepCopy())
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	cm.Annotations["stamped"] = "true"

	return ctrl.Result{}, s.Patch(ctx, cm, patch)
}

func TestStamperMarksConfigMaps(t *testing.T) {
	t.Parallel()

	cp := petri.Start(t)
	ns := cp.Namespace(t)

	// Metrics and health probes are off, so parallel tests never fight over
	// ports. Pass petricr.WithScheme for your own API types.
	mgr := petricr.NewManager(t, cp.Config())

	// On the shared plane the manager sees every test's objects; this test's
	// controller only takes its own namespace's.
	err := ctrl.NewControllerManagedBy(mgr).
		Named("stamper-" + ns).
		For(&corev1.ConfigMap{}).
		WithEventFilter(predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetNamespace() == ns
		})).
		Complete(&stamper{Client: mgr.GetClient()})
	if err != nil {
		t.Fatal(err)
	}

	// Returns once the caches have synced; stops the manager when the test
	// ends, and fails the test if it exits early.
	petricr.StartManager(t, mgr)

	// Check results with a direct client: the manager's client reads from its
	// cache, which lags behind, and cannot watch.
	c, err := client.NewWithWatch(cp.Config(), client.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cm := &corev1.ConfigMap{}
	cm.Name, cm.Namespace = "settings", ns
	err = c.Create(t.Context(), cm)
	if err != nil {
		t.Fatal(err)
	}

	// The condition gets nil while the object does not exist.
	petricr.WaitFor(t, c, cm, func(cm *corev1.ConfigMap) (bool, error) {
		return cm != nil && cm.Annotations["stamped"] == "true", nil
	})
}
