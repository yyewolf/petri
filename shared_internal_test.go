package petri

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSharedPlaneStartsOnceAndCachesFailure(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")

	var calls atomic.Int32

	s := &sharedPlane{start: func(context.Context) (*ControlPlane, error) {
		calls.Add(1)

		return nil, boom
	}}

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, err := s.get(); !errors.Is(err, boom) {
				t.Errorf("get() error = %v, want boom", err)
			}
		})
	}
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Errorf("start called %d times, want 1", n)
	}
}

func TestSharedPlaneStartContextHasDeadline(t *testing.T) {
	t.Parallel()

	s := &sharedPlane{start: func(ctx context.Context) (*ControlPlane, error) {
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("no deadline")
		}

		return &ControlPlane{}, nil
	}}

	if _, err := s.get(); err != nil {
		t.Fatalf("get(): %v", err)
	}
}
