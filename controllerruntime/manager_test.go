package controllerruntime_test

import (
	"context"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/yyewolf/petri"
	"github.com/yyewolf/petri/controllerruntime"
)

func clientForTest(t *testing.T, env *petri.ControlPlane) client.Client {
	t.Helper()

	c, err := client.New(env.Config(), client.Options{Scheme: clientgoscheme.Scheme})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	return c
}

func createConfigMap(t *testing.T, c client.Client, ns, name string) {
	t.Helper()

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	if err := c.Create(t.Context(), cm); err != nil {
		t.Fatalf("create configmap: %v", err)
	}
}

// recordReconciler records every reconciled object's name without ever
// blocking a controller worker: a blocking Reconcile would stall the manager's
// graceful shutdown for as long as controller-runtime waits for its workers.
type recordReconciler struct {
	mu   sync.Mutex
	seen map[string]bool
}

func newRecordReconciler() *recordReconciler {
	return &recordReconciler{seen: map[string]bool{}}
}

func (r *recordReconciler) Reconcile(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seen[req.Name] = true

	return reconcile.Result{}, nil
}

func (r *recordReconciler) saw(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.seen[name]
}

func TestStartManagerSyncsCachesAndRunsControllers(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	c := clientForTest(t, env)
	ns := env.Namespace(t)

	mgr := controllerruntime.NewManager(t, env.Config())
	reconciler := newRecordReconciler()
	if err := builder.ControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}).
		Complete(reconciler); err != nil {
		t.Fatalf("build controller: %v", err)
	}

	// StartManager returns once the caches have synced, so the controller
	// sees objects created right after it. Parallel tests reconcile their
	// own configmaps too, which the recorder tolerates.
	controllerruntime.StartManager(t, mgr)

	createConfigMap(t, c, ns, "observed")

	petri.Retry(t, func(_ context.Context) (bool, error) {
		return reconciler.saw("observed"), nil
	})
}
