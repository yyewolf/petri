package controllers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apiserver/pkg/quota/v1/generic"
	"k8s.io/client-go/discovery"
	"k8s.io/controller-manager/pkg/informerfactory"

	pkgcontroller "github.com/yyewolf/petri/internal/kubernetes/pkg/controller"
	resourcequotacontroller "github.com/yyewolf/petri/internal/kubernetes/pkg/controller/resourcequota"
	quotainstall "github.com/yyewolf/petri/internal/kubernetes/pkg/quota/v1/install"
)

// ResourceQuotaName keys the resourcequota controller in the registry, after
// upstream's --controllers value.
const ResourceQuotaName = "resourcequota"

// ResourceQuotaSpecs returns the registry specs of the quota controller petri
// can enable by name.
//
// The ResourceQuota admission plugin is on by default on petri's API server —
// apiserver.go only disables ServiceAccount — so a quota's hard limits are
// enforced without any server change; this controller is what keeps
// status.used tracking reality.
func ResourceQuotaSpecs() []Spec {
	return []Spec{
		{Name: ResourceQuotaName, Build: buildResourceQuotaController},
	}
}

const (
	// resourceQuotaWorkers matches kube-controller-manager's
	// --concurrent-resource-quota-syncs default.
	resourceQuotaWorkers = 5
	// resourceQuotaSyncPeriod matches upstream's --resource-quota-sync-period
	// default. A quota whose spec and status already agree is still
	// recalculated this often.
	resourceQuotaSyncPeriod = 5 * time.Minute
	// resourceQuotaDiscoveryResyncPeriod is how often the controller's Sync
	// loop re-reads discovery and grows its monitor set. Upstream re-syncs
	// every 30s, which is far too slow for a test plane: a CRD installed
	// after startup would not join the quota monitor set until the next
	// tick. Two seconds keeps the pickup inside a few seconds of
	// InstallCRDs returning, while the per-tick cost stays one aggregated
	// discovery request against an in-process API server, as with the
	// garbage collector's discovery resync.
	resourceQuotaDiscoveryResyncPeriod = 2 * time.Second
)

// buildResourceQuotaController constructs the upstream quota controller,
// wired like kube-controller-manager's startResourceQuotaController: the
// evaluator registry from the quota install package, discovery for the
// monitor set, and the typed+metadata factory adapter for the monitors'
// informers (custom resources join through metadata-only informers).
func buildResourceQuotaController(ctx context.Context, m *Manager) (Run, error) {
	client, err := m.KubeClient(ResourceQuotaName)
	if err != nil {
		return nil, err
	}

	// The Sync loop needs a discovery client of its own: it must not be the
	// one inside the shared RESTMapper, whose cache it would reset
	// underneath the mapper (the caveat on the Sync godoc).
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(m.controllerConfig(ResourceQuotaName))
	if err != nil {
		return nil, fmt.Errorf("petri: build %s discovery client: %w", ResourceQuotaName, err)
	}

	discoveryFunc := discoveryClient.ServerPreferredNamespacedResources
	listerFuncForResource := generic.ListerFuncForResourceFunc(m.KubeFactory().ForResource)
	quotaConfiguration, err := quotainstall.NewQuotaConfigurationForControllers(listerFuncForResource, m.KubeFactory())
	if err != nil {
		return nil, err
	}

	controller, err := resourcequotacontroller.NewController(ctx, &resourcequotacontroller.ControllerOptions{
		QuotaClient:           client.CoreV1(),
		ResourceQuotaInformer: gatedResourceQuotas(m.KubeFactory().Core().V1().ResourceQuotas(), m.constructorGate()),
		ResyncPeriod:          pkgcontroller.StaticResyncPeriodFunc(resourceQuotaSyncPeriod),
		InformerFactory:       informerfactory.NewInformerFactory(m.KubeFactory(), m.MetaFactory()),
		// The replenishment informers resync with the manager's shared
		// factories instead of upstream's jittered --min-resync-period: one
		// period for everything a test plane runs.
		ReplenishmentResyncPeriod: func() time.Duration { return informerResyncPeriod },
		DiscoveryFunc:             discoveryFunc,
		IgnoredResourcesFunc:      quotaConfiguration.IgnoredResources,
		InformersStarted:          m.InformersStarted(),
		Registry:                  generic.NewRegistry(quotaConfiguration.Evaluators()),
		UpdateFilter:              quotainstall.DefaultUpdateFilter(),
	})
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) {
		// Run and Sync are concurrent upstream (concurrentRun): Sync keeps
		// the monitor set aligned with discovery, Run drives the quota
		// workers.
		var wg sync.WaitGroup
		wg.Go(func() {
			controller.Sync(ctx, discoveryFunc, resourceQuotaDiscoveryResyncPeriod)
		})
		controller.Run(ctx, resourceQuotaWorkers)
		wg.Wait()
	}, nil
}
