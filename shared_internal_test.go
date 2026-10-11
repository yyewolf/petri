package petri

import (
	"context"
	"errors"
	"strings"
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

func TestSharedPlanePrintsStartupLogsOnce(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	s := &sharedPlane{start: func(context.Context) (*ControlPlane, error) {
		return nil, &startError{err: boom, logs: "LOG LINE"}
	}}

	_, first := s.get()
	_, second := s.get()

	if !errors.Is(first, boom) || !errors.Is(second, boom) {
		t.Fatalf("get() errors = %v, %v; want both to wrap boom", first, second)
	}

	if !strings.Contains(first.Error(), "LOG LINE") {
		t.Errorf("first error %q does not carry the startup logs", first)
	}

	if strings.Contains(second.Error(), "LOG LINE") {
		t.Errorf("second error %q repeats the startup logs", second)
	}
}
