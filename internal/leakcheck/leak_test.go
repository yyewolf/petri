// Package leakcheck_test checks that petri control planes release their
// goroutines. It is its own test binary so no other test's goroutines skew the
// counts.
package leakcheck_test

import (
	"context"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/petri"
)

const (
	cycles       = 3
	slack        = 10
	pollInterval = 250 * time.Millisecond
	// stableFor outlasts kube-apiserver's 3s watch-cache wakeup timers, which
	// exit on their own after Stop.
	stableFor     = 4 * time.Second
	settleTimeout = 30 * time.Second
	startTimeout  = 2 * time.Minute
	maxDumpBytes  = 20000
)

func TestRepeatedNewAndStopDoNotLeakGoroutines(t *testing.T) {
	t.Parallel()

	// The first control plane starts process-wide goroutines (klog's flush
	// daemon, shared caches) that legitimately outlive it.
	cycle(t)

	baseline := stableCount(t)

	for range cycles {
		cycle(t)
	}

	if n := waitAtMost(t, baseline+slack); n > baseline+slack {
		t.Fatalf("%d goroutines after %d New/Stop cycles, want at most %d (baseline %d + %d)\n%s",
			n, cycles, baseline+slack, baseline, slack, goroutineDump())
	}
}

func cycle(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), startTimeout)
	defer cancel()

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err = cp.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// stableCount waits until the goroutine count has not changed for stableFor
// and returns it.
func stableCount(t *testing.T) int {
	t.Helper()

	deadline := time.Now().Add(settleTimeout)
	last, since := runtime.NumGoroutine(), time.Now()

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		if n := runtime.NumGoroutine(); n != last {
			last, since = n, time.Now()
		} else if time.Since(since) >= stableFor {
			return n
		}
	}

	t.Fatalf("goroutine count never settled; last %d\n%s", last, goroutineDump())

	return last
}

// waitAtMost waits up to settleTimeout for the goroutine count to drop to
// limit and returns the last count seen.
func waitAtMost(t *testing.T, limit int) int {
	t.Helper()

	deadline := time.Now().Add(settleTimeout)

	n := runtime.NumGoroutine()
	for n > limit && time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		n = runtime.NumGoroutine()
	}

	return n
}

func goroutineDump() string {
	var b strings.Builder

	_ = pprof.Lookup("goroutine").WriteTo(&b, 1)

	s := b.String()
	if len(s) > maxDumpBytes {
		s = s[:maxDumpBytes] + "\n... (truncated)"
	}

	return s
}
