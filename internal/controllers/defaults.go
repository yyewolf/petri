package controllers

import (
	"context"
	"fmt"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/controller-manager/pkg/informerfactory"

	"github.com/yyewolf/petri/internal/kubernetes/pkg/controller/garbagecollector"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/controller/namespace"
)

const (
	// NamespaceName keys the namespace controller in the registry, after
	// upstream's controller name.
	NamespaceName = "namespace"
	// GarbageCollectorName keys the garbage collector in the registry, after
	// upstream's controller name.
	GarbageCollectorName = "garbagecollector"
)

const (
	// namespaceWorkers matches kube-controller-manager's
	// --concurrent-namespace-syncs default.
	namespaceWorkers = 10
	// namespaceResyncPeriod matches upstream's --namespace-sync-period default.
	// A namespace deleted while the controller was down is still picked up by
	// the informer's initial list; the resync only re-drives namespaces stuck
	// on their backoff.
	namespaceResyncPeriod = 5 * time.Minute

	// gcWorkers matches kube-controller-manager's --concurrent-gc-syncs
	// default.
	gcWorkers = 20
	// gcInitialSyncTimeout matches the initial monitor-sync budget upstream
	// passes to the garbage collector's Run.
	gcInitialSyncTimeout = 30 * time.Second
	// gcDiscoveryResyncPeriod is how often the garbage collector re-reads
	// discovery and grows its monitor set. Upstream re-syncs every 30s, which
	// is far too slow for a test plane: a CRD installed after startup would
	// not join the dependency graph until the next tick. Two seconds keeps
	// the pickup inside a few seconds of InstallCRDs returning, while the
	// per-tick cost stays one aggregated discovery request against an
	// in-process API server.
	gcDiscoveryResyncPeriod = 2 * time.Second
)

// DefaultSpecs returns the registry specs of the controllers petri runs by
// default: the namespace controller and the garbage collector. They make a
// petri plane behave like a small cluster — a deleted namespace really goes
// away, and deleting an owner deletes its dependents — which is intentionally
// different from envtest, where neither happens.
func DefaultSpecs() []Spec {
	return []Spec{
		{Name: NamespaceName, Build: buildNamespaceController},
		{Name: GarbageCollectorName, Build: buildGarbageCollector},
	}
}

// EnableDefaults registers the default controllers and enables them, returning
// once their informers are synced, so objects created right after it returns
// are seen by both controllers. ctx bounds the wait: a caller that gives up on
// startup stops the control plane, which winds the start in flight down.
func (m *Manager) EnableDefaults(ctx context.Context) error {
	specs := DefaultSpecs()

	if err := m.Register(specs...); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		for _, spec := range specs {
			if err := m.Enable(spec.Name); err != nil {
				done <- err

				return
			}
		}

		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("petri: enable default controllers: %w", err)
		}

		return nil
	case <-ctx.Done():
		return fmt.Errorf("petri: enable default controllers: %w", ctx.Err())
	}
}

// buildNamespaceController constructs the upstream namespace controller
// against the manager's clients and factories. The controller discovers the
// served resources on every namespace deletion, so custom resources whose CRD
// was installed after startup are swept like built-in ones.
func buildNamespaceController(ctx context.Context, m *Manager) (Run, error) {
	kubeClient, err := m.KubeClient(NamespaceName)
	if err != nil {
		return nil, err
	}

	metadataClient, err := m.MetaClient(NamespaceName)
	if err != nil {
		return nil, err
	}

	// The finalizer token is the one the API server puts on every namespace's
	// spec.finalizers: removing it is what lets the namespace disappear.
	controller := namespace.NewNamespaceController(
		ctx,
		kubeClient,
		metadataClient,
		kubeClient.Discovery().ServerPreferredNamespacedResources,
		m.KubeFactory().Core().V1().Namespaces(),
		namespaceResyncPeriod,
		v1.FinalizerKubernetes,
	)

	return func(ctx context.Context) { controller.Run(ctx, namespaceWorkers) }, nil
}

// buildGarbageCollector constructs the upstream garbage collector against the
// manager's shared state. Its monitors come from discovery through the Sync
// loop below, which is also how custom resources join the dependency graph
// after their CRD is installed.
func buildGarbageCollector(ctx context.Context, m *Manager) (Run, error) {
	kubeClient, err := m.KubeClient(GarbageCollectorName)
	if err != nil {
		return nil, err
	}

	metadataClient, err := m.MetaClient(GarbageCollectorName)
	if err != nil {
		return nil, err
	}

	// The Sync loop needs a discovery client of its own: it must not be the
	// one inside the shared RESTMapper, whose cache it would reset underneath
	// the mapper (the caveat on the Sync godoc).
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(m.controllerConfig(GarbageCollectorName))
	if err != nil {
		return nil, fmt.Errorf("petri: build %s discovery client: %w", GarbageCollectorName, err)
	}

	gc, err := garbagecollector.NewGarbageCollector(
		ctx,
		kubeClient,
		metadataClient,
		m.Mapper(),
		garbagecollector.DefaultIgnoredResources(),
		informerfactory.NewInformerFactory(m.KubeFactory(), m.MetaFactory()),
		m.InformersStarted(),
	)
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) {
		// Run and Sync are concurrent upstream: Sync keeps the monitors and
		// the RESTMapper aligned with discovery, and re-starts the shared
		// factories so informers registered for new resources actually run.
		var wg sync.WaitGroup
		wg.Go(func() { gc.Sync(ctx, discoveryClient, gcDiscoveryResyncPeriod) })
		gc.Run(ctx, gcWorkers, gcInitialSyncTimeout)
		wg.Wait()
	}, nil
}
