package petri

import (
	"testing"

	"github.com/yyewolf/petri/internal/controllers"
)

// Controller identifies a built-in controller that
// [ControlPlane.EnableControllers] can run on a control plane. The names
// follow kube-controller-manager's --controllers flag values.
type Controller string

// The workload controllers.
//
// Pods never leave Pending — petri runs no scheduler and no kubelets — so
// each controller stops where node state would be needed:
//
//   - Deployment creates ReplicaSets. Enabling it alone creates no Pods:
//     enabling a controller never enables another. Enable ReplicaSet too for
//     the Pods.
//   - ReplicaSet realizes spec.replicas as Pending Pods, scaling included.
//   - StatefulSet with the default OrderedReady policy creates Pod 0 and
//     stops there: Pod 0 can never become Ready.
//   - DaemonSet creates no Pods at all, because there are no nodes to run
//     its Pods on.
//   - Job creates Pending Pods up to spec.parallelism; the Job never
//     completes.
//   - CronJob schedules by wall clock, and upstream injects no clock, so
//     tests that need a Job now create one from the CronJob's
//     spec.jobTemplate themselves.
//   - TTLAfterFinished deletes Jobs ttlSecondsAfterFinished seconds after a
//     Complete or Failed condition marks them finished.
const (
	DeploymentController       Controller = controllers.DeploymentName
	ReplicaSetController       Controller = controllers.ReplicaSetName
	StatefulSetController      Controller = controllers.StatefulSetName
	DaemonSetController        Controller = controllers.DaemonSetName
	JobController              Controller = controllers.JobName
	CronJobController          Controller = controllers.CronJobName
	TTLAfterFinishedController Controller = controllers.TTLAfterFinishedName
)

// The remaining built-in controllers.
//
//   - EndpointSlice gives every Service with a selector an EndpointSlice
//     owned by it, with no endpoints: no Pod is ever Ready on a nodeless
//     plane. The legacy Endpoints controller and the mirroring controller are
//     not registered — see internal/controllers/endpointslice.go for the
//     scoping decision.
//   - ServiceAccount keeps a default ServiceAccount in every namespace. The
//     ServiceAccount admission plugin stays disabled: it applies to the whole
//     API server, and enabling it would make Pod creation race against
//     ServiceAccount creation in new namespaces.
//   - ServiceAccountToken fills the tokens of legacy service-account token
//     Secrets with a JWT signed by the API server's own in-memory signing
//     key, so a TokenReview authenticates them. It does not mint a Secret per
//     ServiceAccount any more (1.37 removed that with bound tokens).
//   - ResourceQuota keeps a quota's status.used tracking reality; the
//     ResourceQuota admission plugin is on by default, so hard limits are
//     enforced even while the controller is off.
//   - Disruption drives PodDisruptionBudget statuses from Pod facts. Without
//     ready Pods every budget settles on zero disruptionsAllowed, the safe
//     nodeless value.
const (
	EndpointSliceController       Controller = controllers.EndpointSliceName
	ServiceAccountController      Controller = controllers.ServiceAccountName
	ServiceAccountTokenController Controller = controllers.ServiceAccountTokenName
	ResourceQuotaController       Controller = controllers.ResourceQuotaName
	DisruptionController          Controller = controllers.DisruptionName
)

// WorkloadControllers returns the workload controllers as a fresh slice, so a
// test can enable the whole set:
//
//	cp.EnableControllers(t, petri.WorkloadControllers()...)
func WorkloadControllers() []Controller {
	return []Controller{
		DeploymentController,
		ReplicaSetController,
		StatefulSetController,
		DaemonSetController,
		JobController,
		CronJobController,
		TTLAfterFinishedController,
	}
}

// EnableControllers enables built-in controllers on the control plane and
// returns once the last one's informers have synced, so an object created
// right afterwards is seen by every enabled controller. A controller that
// already runs is left alone, and the calls are safe to make concurrently:
// parallel tests can enable the same controllers without racing, and on the
// shared control plane controllers stay enabled once enabled, the same model
// as InstallCRDs. A plane started with [WithoutDefaultControllers] can enable
// them too.
//
// Enabling a controller never enables another: Deployment without ReplicaSet
// creates ReplicaSets but no Pods. With no controllers named,
// EnableControllers does nothing.
//
// Errors fail the test, in the style of Start.
func (cp *ControlPlane) EnableControllers(t testing.TB, cs ...Controller) {
	t.Helper()

	if len(cs) == 0 {
		return
	}

	names := make([]string, 0, len(cs))
	for _, c := range cs {
		names = append(names, string(c))
	}

	if err := cp.manager.EnableControllers(names...); err != nil {
		t.Fatal(err)
	}
}
