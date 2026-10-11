package controllerruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/yyewolf/petri"
)

// managerSyncTimeout bounds the wait for a Manager's caches to sync.
const managerSyncTimeout = 2 * time.Minute

// ManagerOption adjusts the Manager NewManager builds.
type ManagerOption func(*managerOptions)

type managerOptions struct {
	scheme   *runtime.Scheme
	webhooks webhook.Server
}

// WithScheme sets the scheme the Manager and its caches use. Without it, the
// built-in Kubernetes types are registered.
func WithScheme(scheme *runtime.Scheme) ManagerOption {
	return func(o *managerOptions) { o.scheme = scheme }
}

// WithWebhookServer sets the webhook server the Manager runs. Use it to point
// the server at a port and a petri serving certificate directory.
func WithWebhookServer(s webhook.Server) ManagerOption {
	return func(o *managerOptions) { o.webhooks = s }
}

// NewManager returns a manager.Manager for the control plane, with test
// defaults: the control plane's admin config, metrics and health probes off,
// and the built-in Kubernetes types registered.
func NewManager(t testing.TB, cp *petri.ControlPlane, opts ...ManagerOption) manager.Manager {
	t.Helper()

	mgr, err := newManager(cp, opts)
	if err != nil {
		t.Fatalf("petri controllerruntime: build manager: %v", err)
	}

	return mgr
}

func newManager(cp *petri.ControlPlane, opts []ManagerOption) (manager.Manager, error) {
	options := managerOptions{
		scheme:   clientgoscheme.Scheme,
		webhooks: webhook.NewServer(webhook.Options{}),
	}
	for _, opt := range opts {
		opt(&options)
	}

	// A metrics server per test binary would fight over :8080, and no test
	// scrapes it.
	return manager.New(cp.Config(), manager.Options{
		Scheme:        options.scheme,
		Metrics:       server.Options{BindAddress: "0"},
		WebhookServer: options.webhooks,
	})
}

// StartManager runs the manager until the test finishes and returns once its
// caches have synced, so controllers see objects created right after it. It
// fails the test when the manager exits before syncing, and reports unexpected
// errors from the run during cleanup.
func StartManager(t testing.TB, mgr manager.Manager) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- mgr.Start(ctx) }()

	syncCtx, stopSync := context.WithTimeout(ctx, managerSyncTimeout)
	defer stopSync()

	synced := make(chan error, 1)
	go func() {
		if mgr.GetCache().WaitForCacheSync(syncCtx) {
			synced <- nil

			return
		}

		synced <- errors.New("caches did not sync in time")
	}()

	select {
	case err := <-stopped:
		// The manager exited before the caches synced, which is always a
		// failure, and it would leave the sync wait hanging.
		cancel()
		t.Fatalf("petri controllerruntime: manager exited before its caches synced: %v", err)
	case err := <-synced:
		if err != nil {
			cancel()
			t.Fatalf("petri controllerruntime: %v", err)
		}
	}

	t.Cleanup(func() {
		cancel()

		if err := <-stopped; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("petri controllerruntime: manager stopped with %v", err)
		}
	})
}
