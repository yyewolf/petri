package petri_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

func TestDedicatedInstancesAreIsolated(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	var (
		wg     sync.WaitGroup
		planes [2]*petri.ControlPlane
		errs   [2]error
	)
	for i := range planes {
		wg.Go(func() { planes[i], errs[i] = petri.New(ctx) })
	}
	wg.Wait()

	t.Cleanup(func() {
		for _, cp := range planes {
			if cp != nil {
				_ = cp.Stop()
			}
		}
	})

	for i, err := range errs {
		if err != nil {
			t.Fatalf("New #%d: %v", i, err)
		}
	}

	a, b := clientFor(t, planes[0].Config()), clientFor(t, planes[1].Config())

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "only-in-a"}}
	if _, err := a.CoreV1().ConfigMaps(metav1.NamespaceDefault).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create in a: %v", err)
	}

	if _, err := a.CoreV1().ConfigMaps(metav1.NamespaceDefault).Get(ctx, "only-in-a", metav1.GetOptions{}); err != nil {
		t.Fatalf("get from a: %v", err)
	}

	_, err := b.CoreV1().ConfigMaps(metav1.NamespaceDefault).Get(ctx, "only-in-a", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("get from b: got %v, want NotFound", err)
	}

	for i, cp := range planes {
		if err = cp.Stop(); err != nil {
			t.Errorf("Stop #%d: %v", i, err)
		}
		if err = cp.Stop(); err != nil {
			t.Errorf("second Stop #%d: %v", i, err)
		}
	}
}

func TestConfigReturnsIndependentCopies(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	first := cp.Config()
	if first.QPS != 1000 || first.Burst != 10000 {
		t.Errorf("QPS/Burst = %v/%d, want 1000/10000", first.QPS, first.Burst)
	}

	first.Host = "mutated"
	if cp.Config().Host == "mutated" {
		t.Error("mutating one Config() result changed the next one")
	}
}

func TestNewWithCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	start := time.Now()

	_, err := petri.New(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("New(cancelled ctx) error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("New(cancelled ctx) took %v, want under 5s", elapsed)
	}
}

func TestStopReturnsPromptly(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()

	if err = cp.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("Stop took %v, want under 30s", elapsed)
	}
}
