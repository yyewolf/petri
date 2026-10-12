package petri_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/yyewolf/petri"
)

// newPodTemplate builds a one-container pod template with the given labels.
// The image is the same everywhere: the tests only need a container to exist.
func newPodTemplate(name string, labels map[string]string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labels},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: name, Image: "nginx:1.0"}},
		},
	}
}

// newReplicaSet builds a ReplicaSet with the given replica count.
func newReplicaSet(name string, replicas int32) *appsv1.ReplicaSet {
	labels := map[string]string{"app": name}

	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: new(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: newPodTemplate(name, labels),
		},
	}
}

// newStatefulSet builds a two-replica OrderedReady StatefulSet with one
// volume claim template, so its Pods get identity-named PVCs.
func newStatefulSet(name string, replicas int32) *appsv1.StatefulSet {
	labels := map[string]string{"app": name}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: appsv1.StatefulSetSpec{
			Replicas: new(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: newPodTemplate(name, labels),
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "data"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{ //nolint:exhaustive // One storage request is the point; the other resource names are irrelevant.
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			}},
		},
	}
}

// newDaemonSet builds a DaemonSet. It has no node selector, so on a cluster
// with nodes every one of them would run a Pod.
func newDaemonSet(name string) *appsv1.DaemonSet {
	labels := map[string]string{"app": name}

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: newPodTemplate(name, labels),
		},
	}
}

// newJob builds a Job with the given parallelism; completions below one
// leaves the field unset, meaning "one".
func newJob(name string, parallelism, completions int32) *batchv1.Job {
	template := newPodTemplate(name, nil)
	template.Spec.RestartPolicy = corev1.RestartPolicyNever

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: batchv1.JobSpec{
			Parallelism: new(parallelism),
			Template:    template,
		},
	}
	if completions > 0 {
		job.Spec.Completions = new(completions)
	}

	return job
}

// newCronJob builds a CronJob running a plain one-pod Job on the given
// schedule.
func newCronJob(name, schedule string) *batchv1.CronJob {
	template := newPodTemplate(name, nil)
	template.Spec.RestartPolicy = corev1.RestartPolicyNever

	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: batchv1.CronJobSpec{
			Schedule: schedule,
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{Template: template},
			},
		},
	}
}

// controlledPods returns the pods whose controller owner reference points at
// uid.
func controlledPods(pods []corev1.Pod, uid types.UID) []corev1.Pod {
	var found []corev1.Pod

	for i := range pods {
		if ref := metav1.GetControllerOf(&pods[i]); ref != nil && ref.UID == uid {
			found = append(found, pods[i])
		}
	}

	return found
}

// controlledReplicaSets returns the ReplicaSets whose controller owner
// reference points at uid.
func controlledReplicaSets(rss []appsv1.ReplicaSet, uid types.UID) []appsv1.ReplicaSet {
	var found []appsv1.ReplicaSet

	for i := range rss {
		if ref := metav1.GetControllerOf(&rss[i]); ref != nil && ref.UID == uid {
			found = append(found, rss[i])
		}
	}

	return found
}

// waitForControlledPods waits until exactly want pods controlled by uid exist
// in ns and returns them. The controller reaction is counted by owner UID, so
// pods of other controllers in the same namespace cannot satisfy the wait.
func waitForControlledPods(
	ctx context.Context, t *testing.T, cs kubernetes.Interface, ns string, uid types.UID, want int,
) []corev1.Pod {
	t.Helper()

	var pods []corev1.Pod

	petri.Retry(t, func(context.Context) (bool, error) {
		list, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		pods = controlledPods(list.Items, uid)

		return len(pods) == want, nil
	})

	return pods
}

// waitForControlledReplicaSets waits until exactly want ReplicaSets
// controlled by uid exist in ns and returns them.
func waitForControlledReplicaSets(
	ctx context.Context, t *testing.T, cs kubernetes.Interface, ns string, uid types.UID, want int,
) []appsv1.ReplicaSet {
	t.Helper()

	var rss []appsv1.ReplicaSet

	petri.Retry(t, func(context.Context) (bool, error) {
		list, err := cs.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		rss = controlledReplicaSets(list.Items, uid)

		return len(rss) == want, nil
	})

	return rss
}

// assertAllPending checks that every pod is still Pending, which is the only
// phase a plane without kubelets can produce.
func assertAllPending(t *testing.T, pods []corev1.Pod) {
	t.Helper()

	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodPending {
			t.Errorf("pod %s phase = %q, want Pending", pod.Name, pod.Status.Phase)
		}
	}
}

// TestDeploymentCreatesReplicaSetsAndPendingPods runs the full Deployment
// chain: enable, create, ReplicaSet, Pending Pods, template change, scaling,
// and the garbage collector taking the chain down with the Deployment.
func TestDeploymentCreatesReplicaSetsAndPendingPods(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	// EnableControllers returns only once the informers are synced, so the
	// Deployment created right afterwards is never missed: this is the
	// explicit no-missed-first-object sequence.
	cp.EnableControllers(t, petri.DeploymentController, petri.ReplicaSetController)

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	// Recreate drains the old ReplicaSet before scaling the new one up, a
	// rollout a plane without nodes can still drive: pending Pods delete
	// like any other object, and only the RS controller's status keeps the
	// recreate moving.
	dep := newDeployment("web", corev1.Container{Name: "web", Image: "nginx:1.0"})
	dep.Spec.Replicas = new(int32(3))
	dep.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}

	dep, err = cs.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	rss := waitForControlledReplicaSets(ctx, t, cs, ns, dep.UID, 1)
	oldRS := rss[0]
	if got := *oldRS.Spec.Replicas; got != 3 {
		t.Errorf("ReplicaSet replicas = %d, want 3", got)
	}

	pods := waitForControlledPods(ctx, t, cs, ns, oldRS.UID, 3)
	assertAllPending(t, pods)

	// A template change creates a second ReplicaSet. Patching instead of
	// updating: the controller writes the Deployment's status concurrently,
	// so an update races it on the resource version.
	dep, err = cs.AppsV1().Deployments(ns).Patch(ctx, dep.Name, types.StrategicMergePatchType,
		[]byte(
			`{"spec":{"template":{"spec":{"containers":[{"name":"web","image":"nginx:2.0"}]}}}}`,
		), metav1.PatchOptions{})
	if err != nil {
		t.Fatalf("change deployment template: %v", err)
	}

	rss = waitForControlledReplicaSets(ctx, t, cs, ns, dep.UID, 2)

	var newRS appsv1.ReplicaSet
	for _, rs := range rss {
		if rs.UID != oldRS.UID {
			newRS = rs
		}
	}

	pods = waitForControlledPods(ctx, t, cs, ns, newRS.UID, 3)
	assertAllPending(t, pods)

	// Scaling the Deployment scales the current ReplicaSet and its Pods.
	dep, err = cs.AppsV1().Deployments(ns).Patch(ctx, dep.Name, types.StrategicMergePatchType,
		[]byte(`{"spec":{"replicas":5}}`), metav1.PatchOptions{})
	if err != nil {
		t.Fatalf("scale deployment: %v", err)
	}

	pods = waitForControlledPods(ctx, t, cs, ns, newRS.UID, 5)
	assertAllPending(t, pods)

	// The garbage collector takes the chain down with the Deployment.
	background := metav1.DeletePropagationBackground
	if err = cs.AppsV1().
		Deployments(ns).
		Delete(ctx, dep.Name, metav1.DeleteOptions{PropagationPolicy: &background}); err != nil {
		t.Fatalf("delete deployment: %v", err)
	}

	petri.WaitFor(t, cs.AppsV1().ReplicaSets(ns), newRS.Name, petri.Deleted[*appsv1.ReplicaSet]())
	petri.WaitFor(t, cs.AppsV1().ReplicaSets(ns), oldRS.Name, petri.Deleted[*appsv1.ReplicaSet]())

	petri.Retry(t, func(context.Context) (bool, error) {
		list, lerr := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if lerr != nil {
			return false, lerr
		}

		return len(list.Items) == 0, nil
	})
}

// TestDeploymentAloneDoesNotEnableReplicaSet pins the no-implicit-enabling
// rule: the Deployment controller creates ReplicaSets, and Pods only appear
// once the ReplicaSet controller is enabled too.
func TestDeploymentAloneDoesNotEnableReplicaSet(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cp.EnableControllers(t, petri.DeploymentController)

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	dep := newDeployment("web", corev1.Container{Name: "web", Image: "nginx:1.0"})
	dep.Spec.Replicas = new(int32(2))

	dep, err = cs.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	rss := waitForControlledReplicaSets(ctx, t, cs, ns, dep.UID, 1)
	if got := *rss[0].Spec.Replicas; got != 2 {
		t.Errorf("ReplicaSet replicas = %d, want 2", got)
	}

	// Nothing creates Pods from a ReplicaSet while the RS controller is off,
	// so after a window there are still none: enabling Deployment did not
	// silently enable its dependency.
	time.Sleep(2 * time.Second)

	list, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}

	if len(list.Items) != 0 {
		t.Errorf("found %d pods, want 0: the Deployment controller must not create Pods", len(list.Items))
	}
}

// TestReplicaSetCreatesAndScalesPendingPods checks that the ReplicaSet
// controller realizes spec.replicas as Pending Pods and follows scaling.
func TestReplicaSetCreatesAndScalesPendingPods(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cp.EnableControllers(t, petri.ReplicaSetController)

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	rs, err := cs.AppsV1().ReplicaSets(ns).Create(ctx, newReplicaSet("web", 4), metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create replicaset: %v", err)
	}

	pods := waitForControlledPods(ctx, t, cs, ns, rs.UID, 4)
	assertAllPending(t, pods)

	for _, pod := range pods {
		if pod.Labels["app"] != "web" {
			t.Errorf("pod %s labels = %v, want the template's app=web", pod.Name, pod.Labels)
		}
	}

	// Scaling down deletes the surplus Pods, pending ones included. Patching
	// instead of updating: the controller writes the ReplicaSet's status
	// concurrently, so an update races it on the resource version.
	rs, err = cs.AppsV1().ReplicaSets(ns).Patch(ctx, rs.Name, types.StrategicMergePatchType,
		[]byte(`{"spec":{"replicas":2}}`), metav1.PatchOptions{})
	if err != nil {
		t.Fatalf("scale replicaset: %v", err)
	}

	waitForControlledPods(ctx, t, cs, ns, rs.UID, 2)
}

// TestStatefulSetCreatesPodZeroAndStops checks OrderedReady on a plane
// without nodes: pod-0 exists with its PVC and stays Pending, and pod-1 is
// never created because pod-0 can never become Ready.
func TestStatefulSetCreatesPodZeroAndStops(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cp.EnableControllers(t, petri.StatefulSetController)

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	sts, err := cs.AppsV1().StatefulSets(ns).Create(ctx, newStatefulSet("web", 2), metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create statefulset: %v", err)
	}

	pod := petri.WaitFor(t, cs.CoreV1().Pods(ns), "web-0", petri.Exists[*corev1.Pod]())
	assertAllPending(t, []corev1.Pod{*pod})

	if ref := metav1.GetControllerOf(pod); ref == nil || ref.UID != sts.UID {
		t.Errorf("pod web-0 controller ref = %v, want the statefulset", ref)
	}

	// The PVC comes from the volume claim template with the pod's identity
	// baked into the name. It carries no owner reference under the default
	// retention policy (Retain): the StatefulSet claims it by name.
	petri.WaitFor(
		t,
		cs.CoreV1().PersistentVolumeClaims(ns),
		"data-web-0",
		petri.Exists[*corev1.PersistentVolumeClaim](),
	)

	// OrderedReady waits for web-0 to be Ready before creating web-1, and no
	// kubelet will ever ready it, so the stop at pod 0 is structural, not a
	// matter of timing: a short window after web-0 proves it.
	time.Sleep(2 * time.Second)

	if _, err = cs.CoreV1().Pods(ns).Get(ctx, "web-1", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("web-1 was created; OrderedReady must stop at web-0 while it is not Ready (err: %v)", err)
	}
}

// TestDaemonSetCreatesNoPodsWithoutNodes checks the empty result the brief
// asks for: with zero nodes the DaemonSet controller runs, syncs the
// DaemonSet's status, and creates no Pods, because Pods are per-node.
func TestDaemonSetCreatesNoPodsWithoutNodes(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cp.EnableControllers(t, petri.DaemonSetController)

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	if _, err = cs.AppsV1().DaemonSets(ns).Create(ctx, newDaemonSet("agent"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create daemonset: %v", err)
	}

	// observedGeneration only moves once the controller processed this
	// generation, so the wait proves the controller ran, not merely started.
	petri.WaitFor(t, cs.AppsV1().DaemonSets(ns), "agent", func(ds *appsv1.DaemonSet) (bool, error) {
		if ds == nil {
			return false, nil
		}

		return ds.Status.ObservedGeneration >= ds.Generation, nil
	})

	// No nodes means no daemon Pods, ever: pod creation is keyed on nodes.
	list, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}

	if len(list.Items) != 0 {
		t.Errorf("found %d pods, want 0: a DaemonSet with zero nodes schedules nothing", len(list.Items))
	}
}

// TestJobCreatesParallelismPodsAndNeverCompletes checks that the Job
// controller keeps active Pods at spec.parallelism and that the Job cannot
// complete: nothing ever sets its Pods to Succeeded.
func TestJobCreatesParallelismPodsAndNeverCompletes(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cp.EnableControllers(t, petri.JobController)

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	job, err := cs.BatchV1().Jobs(ns).Create(ctx, newJob("work", 2, 4), metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// parallelism 2 out of completions 4: two active Pending Pods, and the
	// remaining completions wait for Pods that will never succeed.
	pods := waitForControlledPods(ctx, t, cs, ns, job.UID, 2)
	assertAllPending(t, pods)

	petri.WaitFor(t, cs.BatchV1().Jobs(ns), job.Name, func(j *batchv1.Job) (bool, error) {
		if j == nil {
			return false, nil
		}

		return j.Status.Active == 2, nil
	})

	// Completion needs a Pod to succeed, which needs a kubelet: after a
	// window the Job still has no terminal condition.
	time.Sleep(2 * time.Second)

	job, err = cs.BatchV1().Jobs(ns).Get(ctx, job.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobComplete || cond.Type == batchv1.JobFailed {
			t.Errorf("job has terminal condition %q; it must never finish without kubelets", cond.Type)
		}
	}
}

// TestCronJobWorkloadsComeFromTheJobTemplate documents the CronJob story:
// upstream schedules on the wall clock and injects no clock, so a test that
// needs a Job now creates one from the CronJob's jobTemplate itself, owned by
// the CronJob exactly as the controller's own Jobs would be.
func TestCronJobWorkloadsComeFromTheJobTemplate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	// The Job controller is what turns the recipe's Job into Pods.
	cp.EnableControllers(t, petri.CronJobController, petri.JobController)

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	cronJob, err := cs.BatchV1().CronJobs(ns).Create(ctx, newCronJob("tick", "*/5 * * * *"), metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create cronjob: %v", err)
	}

	cronJob, err = cs.BatchV1().CronJobs(ns).Get(ctx, cronJob.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get cronjob: %v", err)
	}

	// The controller would create this same shape at the next schedule tick:
	// the jobTemplate's spec, owned by the CronJob. Creating it by hand is
	// deterministic, where waiting for the wall clock is not.
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "manual-run",
			Labels: cronJob.Spec.JobTemplate.Spec.Template.Labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1",
				Kind:       "CronJob",
				Name:       cronJob.Name,
				UID:        cronJob.UID,
				Controller: new(true),
			}},
		},
		Spec: *cronJob.Spec.JobTemplate.Spec.DeepCopy(),
	}

	job, err = cs.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create job from the cronjob's jobTemplate: %v", err)
	}

	pods := waitForControlledPods(ctx, t, cs, ns, job.UID, 1)
	assertAllPending(t, pods)
}

// TestTTLAfterFinishedDeletesFinishedJobs checks the TTL controller end to
// end: a Job with ttlSecondsAfterFinished that gets a terminal condition is
// deleted after the TTL expires.
func TestTTLAfterFinishedDeletesFinishedJobs(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cp.EnableControllers(t, petri.TTLAfterFinishedController)

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	job := newJob("done", 1, 0)
	job.Spec.TTLSecondsAfterFinished = new(int32(2))

	job, err = cs.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// The controller deletes a finished Job ttlSecondsAfterFinished seconds
	// after its terminal condition's lastTransitionTime. Jobs never finish
	// on their own here, so the test finishes the Job through the status
	// subresource the way an external batch runner would, with the exact
	// status shape the API server's strengthened Job validation requires.
	now := time.Now().UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(
		`{"status":{"startTime":%q,"completionTime":%q,"succeeded":1,"conditions":[`+
			`{"type":"SuccessCriteriaMet","status":"True","lastTransitionTime":%q},`+
			`{"type":"Complete","status":"True","lastTransitionTime":%q}]}}`,
		now, now, now, now)

	job, err = cs.BatchV1().
		Jobs(ns).
		Patch(ctx, job.Name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}, "status")
	if err != nil {
		t.Fatalf("patch job status: %v", err)
	}

	// A two-second TTL plus the controller's recheck fits the default wait.
	petri.WaitFor(t, cs.BatchV1().Jobs(ns), job.Name, petri.Deleted[*batchv1.Job]())
}

// TestEnableControllersIsIdempotentAndParallelSafe enables the whole optional
// controller set — workloads and the five remaining ones — on the shared
// control plane from four parallel tests: the calls coalesce into one start
// per controller, a second call is a no-op, and each test's ReplicaSet
// created right after the enable is reacted to.
func TestEnableControllersIsIdempotentAndParallelSafe(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)

	optional := append(petri.WorkloadControllers(),
		petri.EndpointSliceController,
		petri.ServiceAccountController,
		petri.ServiceAccountTokenController,
		petri.ResourceQuotaController,
		petri.DisruptionController,
	)

	for i := range 4 {
		t.Run(fmt.Sprintf("caller-%d", i), func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			env.EnableControllers(t, optional...)
			env.EnableControllers(t, optional...)

			cs := clientFor(t, env.Config())
			ns := env.Namespace(t)

			rs, err := cs.AppsV1().ReplicaSets(ns).Create(ctx, newReplicaSet("web", 2), metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("create replicaset: %v", err)
			}

			pods := waitForControlledPods(ctx, t, cs, ns, rs.UID, 2)
			assertAllPending(t, pods)
		})
	}
}

// TestEnableControllersWithoutDefaultControllers checks that EnableControllers
// works on a plane that started with the defaults off. The ServiceAccount
// controller doubles as the proof it ran: its default ServiceAccount appears
// in a namespace created right after the enable.
func TestEnableControllersWithoutDefaultControllers(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx, petri.WithoutDefaultControllers())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cp.EnableControllers(t, petri.ReplicaSetController, petri.ServiceAccountController)

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	rs, err := cs.AppsV1().ReplicaSets(ns).Create(ctx, newReplicaSet("web", 2), metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create replicaset: %v", err)
	}

	waitForControlledPods(ctx, t, cs, ns, rs.UID, 2)

	petri.WaitFor(t, cs.CoreV1().ServiceAccounts(ns), "default", petri.Exists[*corev1.ServiceAccount]())
}

// TestEnableControllersAfterObjectsExist covers enabling on a plane that
// already holds objects: the informers replay their stores through the
// constructors' handlers, which must not disturb the constructors, and the
// controllers must take the existing objects into account.
func TestEnableControllersAfterObjectsExist(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	// A bare Pod with no owner: replaying it through the StatefulSet
	// controller's orphan path is what crashed its half-built handler
	// registration before the constructor gate existed.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "bare"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "nginx:1.0"}}},
	}
	if _, err = cs.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pod: %v", err)
	}

	cp.EnableControllers(t, petri.StatefulSetController, petri.ReplicaSetController)

	// The pre-existing Pod is still there and untouched.
	if _, err = cs.CoreV1().Pods(ns).Get(ctx, "bare", metav1.GetOptions{}); err != nil {
		t.Fatalf("get pod created before the enable: %v", err)
	}

	// And both controllers drive new objects created after the enable.
	sts, err := cs.AppsV1().StatefulSets(ns).Create(ctx, newStatefulSet("web", 1), metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create statefulset: %v", err)
	}

	petri.WaitFor(t, cs.CoreV1().Pods(ns), sts.Name+"-0", petri.Exists[*corev1.Pod]())
}
