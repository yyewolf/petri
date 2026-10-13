package placement_test

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

func TestJobCompletesWhenItsPodSucceeds(t *testing.T) {
	t.Parallel()

	cp := dedicatedPlane(t)
	ns := cp.Namespace(t)
	jobs := cp.Client().BatchV1().Jobs(ns)
	node := cp.AddNode(t, petri.NodeOptions{})
	cp.EnableControllers(t, petri.JobController)

	job, err := jobs.Create(t.Context(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "migrate"},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{{Name: "migrate", Image: "migrate"}},
				},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	pod := cp.ExpectPod(t, petri.OwnedBy(job))
	cp.Bind(t, pod, node)

	// No container runs, so the test decides how the Pod ends: CompletePod
	// succeeds it, FailPod fails it with an exit code.
	petri.WaitFor(t, cp.Client().CoreV1().Pods(ns), pod.Name, func(p *corev1.Pod) (bool, error) {
		return p != nil && p.Status.Phase == corev1.PodRunning, nil
	})
	cp.CompletePod(t, pod)

	petri.WaitFor(t, jobs, "migrate",
		petri.StatusCondition[*batchv1.Job]("Complete", metav1.ConditionTrue))
}
