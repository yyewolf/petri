package petri

import (
	"context"
	"sync"
	"testing"
	"time"
)

const sharedStartTimeout = 2 * time.Minute

// shared is the control plane Start hands out: one per test binary.
//
//nolint:gochecknoglobals // Start shares one control plane per test binary by design.
var shared = &sharedPlane{start: New}

// sharedPlane starts a control plane on first use and caches the outcome,
// failure included, for every later caller.
type sharedPlane struct {
	start func(context.Context) (*ControlPlane, error)

	once sync.Once
	cp   *ControlPlane
	err  error
}

// Start returns the control plane shared by every test in the binary,
// starting it on first use. If startup fails, every caller fails with the same
// error. The shared control plane lives until the test binary exits.
func Start(t testing.TB) *ControlPlane {
	t.Helper()

	cp, err := shared.get()
	if err != nil {
		t.Fatal(err)
	}

	return cp
}

func (s *sharedPlane) get() (*ControlPlane, error) {
	s.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), sharedStartTimeout)
		defer cancel()

		s.cp, s.err = s.start(ctx)
	})

	return s.cp, s.err
}
