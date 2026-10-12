package petri_test

import (
	"context"
	"testing"
	"time"

	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/yyewolf/petri"
)

// TestDisruptionControllerPopulatesPodDisruptionBudgetStatus checks that the
// disruption controller drives a PDB's status from Pod facts. Without ready
// Pods — nothing ever readies on a plane without kubelets —
// disruptionsAllowed is the safe zero and the DisruptionAllowed condition is
// False, while the scale of the owning controller still populates
// expectedPods.
func TestDisruptionControllerPopulatesPodDisruptionBudgetStatus(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	// The ReplicaSet controller gives the PDB real Pods to look at; without
	// it the status would be all zeros for want of matching Pods.
	cp.EnableControllers(t, petri.DisruptionController, petri.ReplicaSetController)

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	rs, err := cs.AppsV1().ReplicaSets(ns).Create(ctx, newReplicaSet("workload", 2), metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create replicaset: %v", err)
	}

	// The PDB's Pods exist and are Pending: healthy count zero, forever.
	pods := waitForControlledPods(ctx, t, cs, ns, rs.UID, 2)
	assertAllPending(t, pods)

	pdb, err := cs.PolicyV1().PodDisruptionBudgets(ns).Create(ctx, &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "protect"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: new(intstr.FromInt32(1)),
			// The PDB selects the ReplicaSet's Pods through the shared
			// label selector.
			Selector: rs.Spec.Selector,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create poddisruptionbudget: %v", err)
	}

	// observedGeneration only moves once the controller processed this
	// generation, so the wait proves the controller ran, not merely started.
	pdb = petri.WaitFor(
		t,
		cs.PolicyV1().PodDisruptionBudgets(ns),
		pdb.Name,
		func(p *policyv1.PodDisruptionBudget) (bool, error) {
			if p == nil {
				return false, nil
			}

			return p.Status.ObservedGeneration >= p.Generation, nil
		},
	)
	// currentHealthy counts Ready Pods (the status does populate from Pod
	// facts), and a Pending Pod is never Ready on a nodeless plane.
	if got := pdb.Status.CurrentHealthy; got != 0 {
		t.Errorf("pdb status.currentHealthy = %d, want 0 (no Pod can become Ready)", got)
	}

	if got := pdb.Status.DesiredHealthy; got != 1 {
		t.Errorf("pdb status.desiredHealthy = %d, want 1 (minAvailable)", got)
	}

	// expectedPods comes from the controller chain the Pods belong to, which
	// the disruption controller resolves through the ReplicaSet lister.
	if got := pdb.Status.ExpectedPods; got != 2 {
		t.Errorf("pdb status.expectedPods = %d, want 2 (the ReplicaSet's scale)", got)
	}

	if got := pdb.Status.DisruptionsAllowed; got != 0 {
		t.Errorf("pdb status.disruptionsAllowed = %d, want 0 (the nodeless safe value)", got)
	}

	petri.WaitFor(t, cs.PolicyV1().PodDisruptionBudgets(ns), pdb.Name,
		petri.StatusCondition[*policyv1.PodDisruptionBudget]("DisruptionAllowed", metav1.ConditionFalse))
}
