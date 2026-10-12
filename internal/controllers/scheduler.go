package controllers

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/events"

	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/apis/config/latest"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/profile"
)

// SchedulerName keys kube-scheduler in the registry.
const SchedulerName = "scheduler"

// SchedulerSpecs returns the registry spec of upstream kube-scheduler, run
// with its default configuration: the default profile, no leader election,
// no extenders and no serving endpoints.
func SchedulerSpecs() []Spec {
	return []Spec{
		{Name: SchedulerName, Build: buildScheduler},
	}
}

// buildScheduler wires kube-scheduler like the kube-scheduler command's Setup
// and Run. The scheduler gets informer factories of its own, as upstream: its
// Pod informer leaves out terminal Pods, which the shared factory's Pod
// informer must keep for the Job and garbage-collection controllers. Enable
// returns once those informers and the scheduler's handlers synced.
func buildScheduler(ctx context.Context, m *Manager) (Run, error) {
	cfg, err := latest.Default()
	if err != nil {
		return nil, fmt.Errorf("petri: default scheduler configuration: %w", err)
	}

	client, err := m.KubeClient(SchedulerName)
	if err != nil {
		return nil, err
	}

	dynClient, err := dynamic.NewForConfig(m.controllerConfig(SchedulerName))
	if err != nil {
		return nil, fmt.Errorf("petri: build %s dynamic client: %w", SchedulerName, err)
	}

	// No informer name: upstream registers names process-wide and refuses
	// duplicates, which would fail the second plane in a test binary.
	factory := scheduler.NewInformerFactory(client, 0, nil)
	dynFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dynClient, 0, corev1.NamespaceAll, nil)
	broadcaster := events.NewBroadcaster(&events.EventSinkImpl{Interface: client.EventsV1()})

	m.onStop(func() {
		broadcaster.Shutdown()
		factory.Shutdown()
		dynFactory.Shutdown()
	})

	sched, err := scheduler.New(ctx,
		client,
		factory,
		dynFactory,
		profile.NewRecorderFactory(broadcaster),
		scheduler.WithComponentConfigVersion(cfg.APIVersion),
		scheduler.WithKubeConfig(m.controllerConfig(SchedulerName)),
		scheduler.WithProfiles(cfg.Profiles...),
		scheduler.WithPercentageOfNodesToScore(cfg.PercentageOfNodesToScore),
		scheduler.WithPodMaxBackoffSeconds(cfg.PodMaxBackoffSeconds),
		scheduler.WithPodInitialBackoffSeconds(cfg.PodInitialBackoffSeconds),
		scheduler.WithParallelism(cfg.Parallelism),
	)
	if err != nil {
		return nil, fmt.Errorf("petri: build scheduler: %w", err)
	}

	m.afterSync(func(ctx context.Context) error {
		factory.Start(ctx.Done())
		dynFactory.Start(ctx.Done())

		for typ, synced := range factory.WaitForCacheSync(ctx.Done()) {
			if !synced {
				return fmt.Errorf("%v never synced", typ)
			}
		}

		for gvr, synced := range dynFactory.WaitForCacheSync(ctx.Done()) {
			if !synced {
				return fmt.Errorf("%s never synced", gvr)
			}
		}

		return sched.WaitForHandlersSync(ctx)
	})

	return func(ctx context.Context) {
		broadcaster.StartRecordingToSink(ctx.Done())
		sched.Run(ctx)
	}, nil
}
