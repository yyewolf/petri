package controllers

import (
	"context"
	"time"

	"k8s.io/client-go/util/flowcontrol"

	"github.com/yyewolf/petri/internal/kubernetes/pkg/controller/cronjob"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/controller/daemon"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/controller/deployment"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/controller/job"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/controller/replicaset"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/controller/statefulset"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/controller/ttlafterfinished"
)

const (
	// DeploymentName keys the deployment controller in the registry, after
	// upstream's --controllers value.
	DeploymentName = "deployment"
	// ReplicaSetName keys the replicaset controller in the registry.
	ReplicaSetName = "replicaset"
	// StatefulSetName keys the statefulset controller in the registry.
	StatefulSetName = "statefulset"
	// DaemonSetName keys the daemonset controller in the registry.
	DaemonSetName = "daemonset"
	// JobName keys the job controller in the registry.
	JobName = "job"
	// CronJobName keys the cronjob controller in the registry.
	CronJobName = "cronjob"
	// TTLAfterFinishedName keys the TTL-after-finished controller in the
	// registry.
	TTLAfterFinishedName = "ttl-after-finished"
)

// Worker counts, one per controller: kube-controller-manager's defaults for
// --concurrent-deployment-syncs, --concurrent-replicaset-syncs,
// --concurrent-statefulset-syncs, --concurrent-daemonset-syncs,
// --concurrent-job-syncs, --concurrent-cronjob-syncs and
// --concurrent-ttl-syncs. A test plane is idle enough that the defaults cost
// nothing and keep the controllers shaped like the real thing.
const (
	deploymentWorkers       = 5
	replicasetWorkers       = 5
	statefulsetWorkers      = 5
	daemonSetWorkers        = 2
	jobWorkers              = 5
	cronJobWorkers          = 5
	ttlAfterFinishedWorkers = 5
)

// The failed-Pod backoff kube-controller-manager passes to the DaemonSet
// controller: recreation attempts start at 1s and cap at 15 minutes.
const (
	daemonSetPodBackoffInitial = 1 * time.Second
	daemonSetPodBackoffMax     = 15 * time.Minute
)

// WorkloadSpecs returns the registry specs of the workload controllers petri
// can enable by name: Deployment, ReplicaSet, StatefulSet, DaemonSet, Job,
// CronJob and TTL-after-finished. None of them run by default — they exist
// for tests that exercise workload objects — and enabling one never enables
// another: a Deployment without the ReplicaSet controller creates
// ReplicaSets but no Pods.
//
// Pods never leave Pending, because petri runs no scheduler and no kubelets.
// Each controller therefore stops where node state would be needed: Jobs
// never complete, OrderedReady StatefulSets stop at Pod 0, and DaemonSets
// schedule nothing (there are no nodes to run their Pods on).
func WorkloadSpecs() []Spec {
	return []Spec{
		{Name: DeploymentName, Build: buildDeploymentController},
		{Name: ReplicaSetName, Build: buildReplicaSetController},
		{Name: StatefulSetName, Build: buildStatefulSetController},
		{Name: DaemonSetName, Build: buildDaemonSetController},
		{Name: JobName, Build: buildJobController},
		{Name: CronJobName, Build: buildCronJobController},
		{Name: TTLAfterFinishedName, Build: buildTTLAfterFinishedController},
	}
}

// buildDeploymentController constructs the upstream Deployment controller,
// wired like kube-controller-manager's startDeploymentController: typed
// informers from the shared factory and the controller's own client.
func buildDeploymentController(ctx context.Context, m *Manager) (Run, error) {
	client, err := m.KubeClient(DeploymentName)
	if err != nil {
		return nil, err
	}

	factory := m.KubeFactory()
	gate := m.constructorGate()

	controller, err := deployment.NewDeploymentController(
		ctx,
		gatedDeployments(factory.Apps().V1().Deployments(), gate),
		gatedReplicaSets(factory.Apps().V1().ReplicaSets(), gate),
		gatedPods(factory.Core().V1().Pods(), gate),
		client,
	)
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) { controller.Run(ctx, deploymentWorkers) }, nil
}

// buildReplicaSetController constructs the upstream ReplicaSet controller
// with upstream's own burst limit, like startReplicaSetController does.
func buildReplicaSetController(ctx context.Context, m *Manager) (Run, error) {
	client, err := m.KubeClient(ReplicaSetName)
	if err != nil {
		return nil, err
	}

	factory := m.KubeFactory()
	gate := m.constructorGate()

	controller := replicaset.NewReplicaSetController(
		ctx,
		gatedReplicaSets(factory.Apps().V1().ReplicaSets(), gate),
		gatedPods(factory.Core().V1().Pods(), gate),
		client,
		replicaset.BurstReplicas,
	)

	return func(ctx context.Context) { controller.Run(ctx, replicasetWorkers) }, nil
}

// buildStatefulSetController constructs the upstream StatefulSet controller,
// which owns its Pods' identities and the PVCs its volume claim templates
// ask for.
func buildStatefulSetController(ctx context.Context, m *Manager) (Run, error) {
	client, err := m.KubeClient(StatefulSetName)
	if err != nil {
		return nil, err
	}

	factory := m.KubeFactory()
	gate := m.constructorGate()

	controller := statefulset.NewStatefulSetController(
		ctx,
		gatedPods(factory.Core().V1().Pods(), gate),
		gatedStatefulSets(factory.Apps().V1().StatefulSets(), gate),
		gatedPersistentVolumeClaims(factory.Core().V1().PersistentVolumeClaims(), gate),
		gatedControllerRevisions(factory.Apps().V1().ControllerRevisions(), gate),
		client,
	)

	return func(ctx context.Context) { controller.Run(ctx, statefulsetWorkers) }, nil
}

// buildDaemonSetController constructs the upstream DaemonSet controller with
// the backoff kube-controller-manager passes for failed pod recreation.
func buildDaemonSetController(ctx context.Context, m *Manager) (Run, error) {
	client, err := m.KubeClient(DaemonSetName)
	if err != nil {
		return nil, err
	}

	factory := m.KubeFactory()
	gate := m.constructorGate()

	controller, err := daemon.NewDaemonSetsController(
		ctx,
		gatedDaemonSets(factory.Apps().V1().DaemonSets(), gate),
		gatedControllerRevisions(factory.Apps().V1().ControllerRevisions(), gate),
		gatedPods(factory.Core().V1().Pods(), gate),
		gatedNodes(factory.Core().V1().Nodes(), gate),
		client,
		flowcontrol.NewBackOff(daemonSetPodBackoffInitial, daemonSetPodBackoffMax),
	)
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) { controller.Run(ctx, daemonSetWorkers) }, nil
}

// buildJobController constructs the upstream Job controller. The workload and
// pod group informers stay nil because petri runs upstream's default feature
// gates and WorkloadWithJob is alpha-off — the exact shape
// startJobController passes in that case.
func buildJobController(ctx context.Context, m *Manager) (Run, error) {
	client, err := m.KubeClient(JobName)
	if err != nil {
		return nil, err
	}

	factory := m.KubeFactory()
	gate := m.constructorGate()

	controller, err := job.NewController(
		ctx,
		client,
		gatedPods(factory.Core().V1().Pods(), gate),
		gatedJobs(factory.Batch().V1().Jobs(), gate),
		nil,
		nil,
	)
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) { controller.Run(ctx, jobWorkers) }, nil
}

// buildCronJobController constructs the upstream CronJob controller (v2, the
// only one upstream wires). It schedules by wall clock: the constructor fixes
// its clock to [time.Now] with no injection point, so tests that need a Job
// now create one from the CronJob's jobTemplate instead.
func buildCronJobController(ctx context.Context, m *Manager) (Run, error) {
	client, err := m.KubeClient(CronJobName)
	if err != nil {
		return nil, err
	}

	factory := m.KubeFactory()
	gate := m.constructorGate()

	controller, err := cronjob.NewControllerV2(
		ctx,
		gatedJobs(factory.Batch().V1().Jobs(), gate),
		gatedCronJobs(factory.Batch().V1().CronJobs(), gate),
		client,
	)
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) { controller.Run(ctx, cronJobWorkers) }, nil
}

// buildTTLAfterFinishedController constructs the upstream TTL-after-finished
// controller, which deletes Jobs ttlSecondsAfterFinished seconds after a
// Complete or Failed condition marks them finished.
func buildTTLAfterFinishedController(ctx context.Context, m *Manager) (Run, error) {
	client, err := m.KubeClient(TTLAfterFinishedName)
	if err != nil {
		return nil, err
	}

	controller := ttlafterfinished.New(
		ctx,
		gatedJobs(m.KubeFactory().Batch().V1().Jobs(), m.constructorGate()),
		client,
	)

	return func(ctx context.Context) { controller.Run(ctx, ttlAfterFinishedWorkers) }, nil
}
