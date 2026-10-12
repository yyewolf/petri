package controllers

import (
	"context"
	"fmt"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/scale"

	disruptioncontroller "github.com/yyewolf/petri/internal/kubernetes/pkg/controller/disruption"
)

// DisruptionName keys the disruption controller in the registry, after
// upstream's --controllers value.
const DisruptionName = "disruption"

// DisruptionSpecs returns the registry specs of the disruption controller
// petri can enable by name, which drives PodDisruptionBudget statuses.
//
// Its expected-scale fallback resolves Pod controllers it has no lister for
// through the scale subresource, so it takes the manager's shared RESTMapper
// and a scale client built on discovery — the wiring
// startDisruptionController does. On a nodeless plane nothing readies, so
// every PDB settles on the safe zero disruptionsAllowed.
func DisruptionSpecs() []Spec {
	return []Spec{
		{Name: DisruptionName, Build: buildDisruptionController},
	}
}

// disruptionWorkers matches kube-controller-manager's
// --concurrent-disruption-syncs default.
const disruptionWorkers = 1

// buildDisruptionController constructs the upstream disruption controller,
// wired like kube-controller-manager's startDisruptionController: typed
// informers from the shared factory, the shared RESTMapper, and a scale
// client for the kinds outside its own listers.
func buildDisruptionController(ctx context.Context, m *Manager) (Run, error) {
	client, err := m.KubeClient(DisruptionName)
	if err != nil {
		return nil, err
	}

	factory := m.KubeFactory()
	gate := m.constructorGate()

	scaleKindResolver := scale.NewDiscoveryScaleKindResolver(client.Discovery())
	scaleClient, err := scale.NewForConfig(
		m.controllerConfig(DisruptionName), m.Mapper(), dynamic.LegacyAPIPathResolverFunc, scaleKindResolver)
	if err != nil {
		return nil, fmt.Errorf("petri: build %s scale client: %w", DisruptionName, err)
	}

	controller := disruptioncontroller.NewDisruptionController(
		ctx,
		gatedPods(factory.Core().V1().Pods(), gate),
		gatedPodDisruptionBudgets(factory.Policy().V1().PodDisruptionBudgets(), gate),
		gatedReplicationControllers(factory.Core().V1().ReplicationControllers(), gate),
		gatedReplicaSets(factory.Apps().V1().ReplicaSets(), gate),
		gatedDeployments(factory.Apps().V1().Deployments(), gate),
		gatedStatefulSets(factory.Apps().V1().StatefulSets(), gate),
		client,
		m.Mapper(),
		scaleClient,
		client.Discovery(),
	)

	return func(ctx context.Context) { controller.Run(ctx, disruptionWorkers) }, nil
}
