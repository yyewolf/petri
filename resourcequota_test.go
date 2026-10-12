package petri_test

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

// TestResourceQuotaEnforcementAndStatusTracking checks the quota story end to
// end: a ResourceQuota limiting Pods is enforced by the ResourceQuota
// admission plugin (enabled by default on petri's API server), and the quota
// controller keeps status.used tracking reality.
func TestResourceQuotaEnforcementAndStatusTracking(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cp.EnableControllers(t, petri.ResourceQuotaController)

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	_, err = cs.CoreV1().ResourceQuotas(ns).Create(ctx, &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "pods"},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{ //nolint:exhaustive // One pods limit is the point; the other resource names are irrelevant.
				corev1.ResourcePods: resource.MustParse("2"),
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create resourcequota: %v", err)
	}

	// The controller publishes the spec's hard limits into the status before
	// anything tracks usage; waiting for it proves the controller ran.
	petri.WaitFor(t, cs.CoreV1().ResourceQuotas(ns), "pods", func(q *corev1.ResourceQuota) (bool, error) {
		return q != nil && q.Status.Hard.Pods().String() == "2", nil
	})

	newPod := func(name string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "web", Image: "nginx:1.0"}},
			},
		}
	}

	for _, name := range []string{"first", "second"} {
		if _, err = cs.CoreV1().Pods(ns).Create(ctx, newPod(name), metav1.CreateOptions{}); err != nil {
			t.Fatalf("create pod %s: %v", name, err)
		}
	}

	// status.used tracks reality: two non-terminal Pods are two used pods,
	// driven by the controller's replenishment, not by admission.
	petri.WaitFor(t, cs.CoreV1().ResourceQuotas(ns), "pods", func(q *corev1.ResourceQuota) (bool, error) {
		return q != nil && q.Status.Used.Pods().String() == "2", nil
	})

	_, err = cs.CoreV1().Pods(ns).Create(ctx, newPod("third"), metav1.CreateOptions{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("create pod beyond the quota: error = %v, want Forbidden", err)
	}
	if !strings.Contains(err.Error(), "exceeded quota") {
		t.Errorf("quota error message = %q, want it to name the exceeded quota", err)
	}
}
