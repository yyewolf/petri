package controllerruntime_test

import (
	"context"
	"testing"
	"time"

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

// recordReconciler forwards every reconciled object's name.
type recordReconciler struct {
	seen chan<- string
}

func (r *recordReconciler) Reconcile(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
	r.seen <- req.Name

	return reconcile.Result{}, nil
}

func TestStartManagerSyncsCachesAndRunsControllers(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	c := clientForTest(t, env)
	ns := env.Namespace(t)

	mgr := controllerruntime.NewManager(t, env)
	seen := make(chan string, 16)
	if err := builder.ControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}).
		Complete(&recordReconciler{seen: seen}); err != nil {
		t.Fatalf("build controller: %v", err)
	}

	// StartManager returns once the caches have synced, so the controller
	// sees objects created right after it. Parallel tests reconcile their
	// own configmaps too, so drain until the right one shows up.
	controllerruntime.StartManager(t, mgr)

	createConfigMap(t, c, ns, "observed")

	for {
		select {
		case name := <-seen:
			if name == "observed" {
				return
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the controller never reconciled the configmap; caches did not sync")
		}
	}
}
