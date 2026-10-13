package placement_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/petri"
)

// dedicatedPlane starts a control plane for one test and stops it when the
// test ends. Nodes only exist on dedicated planes: the shared one refuses them.
func dedicatedPlane(t *testing.T) *petri.ControlPlane {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stopErr := cp.Stop(); stopErr != nil {
			t.Error(stopErr)
		}
	})

	return cp
}
