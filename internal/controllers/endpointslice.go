package controllers

import (
	"context"

	endpointslicecontroller "github.com/yyewolf/petri/internal/kubernetes/pkg/controller/endpointslice"
)

// EndpointSliceName keys the endpointslice controller in the registry, after
// upstream's --controllers value.
const EndpointSliceName = "endpointslice"

// EndpointSliceSpecs returns the registry specs of the service-discovery
// controllers petri can enable by name.
//
// The scoping decision: only the selector-based EndpointSlice controller is
// registered. The legacy Endpoints controller ("endpoint" upstream) and the
// mirroring controller ("endpointslicemirroring") are not. The Endpoints
// controller exists for clients that read the legacy Endpoints objects; with
// no kubelets no Pod ever becomes Ready, so its Endpoints would exist as
// empty shells for every selector Service, and petri's tests read
// EndpointSlices directly. The mirroring controller copies hand-written
// Endpoints objects into slices; without the Endpoints controller and without
// users creating Endpoints there is nothing to mirror. Services with a
// selector still get their EndpointSlice objects, with no endpoints, from
// this controller.
func EndpointSliceSpecs() []Spec {
	return []Spec{
		{Name: EndpointSliceName, Build: buildEndpointSliceController},
	}
}

const (
	// endpointsliceWorkers matches kube-controller-manager's
	// --concurrent-service-endpoint-syncs default.
	endpointsliceWorkers = 5
	// endpointsliceMaxEndpointsPerSlice matches upstream's
	// --max-endpoints-per-slice default.
	endpointsliceMaxEndpointsPerSlice = 100
	// endpointsliceUpdatesBatchPeriod is the zero upstream's flag defaults
	// to: Pod changes sync their slices at once instead of joining a batch.
	// A test plane is idle, so prompt slices cost nothing.
	endpointsliceUpdatesBatchPeriod = 0
)

// buildEndpointSliceController constructs the upstream EndpointSlice
// controller, wired like kube-controller-manager's startEndpointSliceController:
// typed informers from the shared factory and the controller's own client.
func buildEndpointSliceController(ctx context.Context, m *Manager) (Run, error) {
	client, err := m.KubeClient(EndpointSliceName)
	if err != nil {
		return nil, err
	}

	factory := m.KubeFactory()
	gate := m.constructorGate()

	controller := endpointslicecontroller.NewController(
		ctx,
		gatedPods(factory.Core().V1().Pods(), gate),
		gatedServices(factory.Core().V1().Services(), gate),
		gatedNodes(factory.Core().V1().Nodes(), gate),
		gatedEndpointSlices(factory.Discovery().V1().EndpointSlices(), gate),
		endpointsliceMaxEndpointsPerSlice,
		client,
		endpointsliceUpdatesBatchPeriod,
	)

	return func(ctx context.Context) { controller.Run(ctx, endpointsliceWorkers) }, nil
}
