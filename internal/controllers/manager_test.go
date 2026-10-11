package controllers_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/yyewolf/petri/internal/controllers"
)

// newTestManager builds a manager that never talks to an API server: the
// stubs register no informers, so the factories start nothing and stay
// offline.
func newTestManager(t *testing.T) *controllers.Manager {
	t.Helper()

	mgr, err := controllers.New(t.Context(), &rest.Config{Host: "127.0.0.1:1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(mgr.Stop)

	return mgr
}

// noopStub returns a spec for a controller that runs and exits immediately.
func noopStub() controllers.Spec {
	return controllers.Spec{
		Name: "stub",
		Build: func(context.Context, *controllers.Manager) (controllers.Run, error) {
			return func(context.Context) {}, nil
		},
	}
}

// countingStub returns a spec whose build counts its calls.
func countingStub(name string, builds *atomic.Int64) controllers.Spec {
	return controllers.Spec{
		Name: name,
		Build: func(context.Context, *controllers.Manager) (controllers.Run, error) {
			builds.Add(1)

			return func(context.Context) {}, nil
		},
	}
}

func TestEnableRunsTheControllerAndClosesInformersStarted(t *testing.T) {
	t.Parallel()

	mgr := newTestManager(t)

	ran := make(chan struct{})

	spec := noopStub()
	spec.Build = func(context.Context, *controllers.Manager) (controllers.Run, error) {
		return func(context.Context) { close(ran) }, nil
	}

	if err := mgr.Register(spec); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := mgr.Enable(spec.Name); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("controller did not run after Enable returned")
	}

	select {
	case <-mgr.InformersStarted():
	default:
		t.Error("InformersStarted still open after an enable")
	}
}

func TestEnableIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	mgr := newTestManager(t)

	const callers = 8

	var builds atomic.Int64

	// Build blocks until the test releases it: the first caller owns the
	// start and the rest must wait for it instead of racing their own.
	started := make(chan struct{})
	release := make(chan struct{})

	spec := noopStub()
	spec.Build = func(context.Context, *controllers.Manager) (controllers.Run, error) {
		builds.Add(1)
		close(started)
		<-release

		return func(context.Context) {}, nil
	}

	if err := mgr.Register(spec); err != nil {
		t.Fatalf("Register: %v", err)
	}

	var wg sync.WaitGroup

	errs := make([]error, callers)
	for i := range callers {
		wg.Go(func() {
			errs[i] = mgr.Enable(spec.Name)
		})
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("no caller started the controller")
	}

	close(release)
	wg.Wait()

	if builds.Load() != 1 {
		t.Errorf("Build ran %d times, want exactly 1", builds.Load())
	}

	for i, err := range errs {
		if err != nil {
			t.Errorf("Enable #%d: %v", i, err)
		}
	}
}

func TestEnableTwiceBuildsOnce(t *testing.T) {
	t.Parallel()

	mgr := newTestManager(t)

	var builds atomic.Int64

	if err := mgr.Register(countingStub("stub", &builds)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := mgr.Enable("stub"); err != nil {
		t.Fatalf("first Enable: %v", err)
	}

	if err := mgr.Enable("stub"); err != nil {
		t.Fatalf("second Enable: %v", err)
	}

	if builds.Load() != 1 {
		t.Errorf("Build ran %d times, want 1", builds.Load())
	}
}

func TestEnableRetriesAfterAFailedStart(t *testing.T) {
	t.Parallel()

	mgr := newTestManager(t)

	var attempts atomic.Int64

	spec := countingStub("stub", &attempts)
	spec.Build = func(context.Context, *controllers.Manager) (controllers.Run, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("boom")
		}

		return func(context.Context) {}, nil
	}

	if err := mgr.Register(spec); err != nil {
		t.Fatalf("Register: %v", err)
	}

	err := mgr.Enable("stub")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("first Enable error = %v, want the build failure", err)
	}

	if err = mgr.Enable("stub"); err != nil {
		t.Fatalf("second Enable: %v", err)
	}

	if attempts.Load() != 2 {
		t.Errorf("Build ran %d times, want 2 (one failure, one retry)", attempts.Load())
	}
}

func TestRegisterCopiesTheSpecs(t *testing.T) {
	t.Parallel()

	mgr := newTestManager(t)

	var builds atomic.Int64

	// Register is variadic: the registry must hold copies, not pointers into
	// the caller's backing array, so mutating a spec afterwards cannot change
	// what Enable runs.
	specs := []controllers.Spec{countingStub("stub", &builds)}

	if err := mgr.Register(specs...); err != nil {
		t.Fatalf("Register: %v", err)
	}

	specs[0].Build = nil

	if err := mgr.Enable("stub"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	if builds.Load() != 1 {
		t.Errorf("Build ran %d times, want 1 (the registry must keep its own copy)", builds.Load())
	}
}

func TestEnableUnknownControllerFails(t *testing.T) {
	t.Parallel()

	mgr := newTestManager(t)

	err := mgr.Enable("nope")
	if err == nil || !strings.Contains(err.Error(), `unknown controller "nope"`) {
		t.Fatalf("Enable error = %v, want an unknown-controller failure", err)
	}
}

func TestEnableAfterStopFails(t *testing.T) {
	t.Parallel()

	mgr := newTestManager(t)

	var builds atomic.Int64

	if err := mgr.Register(countingStub("stub", &builds)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	mgr.Stop()

	if err := mgr.Enable("stub"); err == nil {
		t.Fatal("Enable after Stop succeeded, want a stopping error")
	}

	if builds.Load() != 0 {
		t.Errorf("Build ran %d times after Stop, want 0", builds.Load())
	}
}

func TestStopWaitsForTheControllerToExit(t *testing.T) {
	t.Parallel()

	mgr := newTestManager(t)

	runStarted := make(chan struct{})
	runDone := make(chan struct{})
	keepRunning := make(chan struct{})

	spec := noopStub()
	spec.Build = func(context.Context, *controllers.Manager) (controllers.Run, error) {
		return func(context.Context) {
			close(runStarted)
			<-keepRunning
			close(runDone)
		}, nil
	}

	if err := mgr.Register(spec); err != nil {
		t.Fatalf("Register: %v", err)
	}

	enabled := make(chan error, 1)
	go func() { enabled <- mgr.Enable("stub") }()

	<-runStarted

	if err := <-enabled; err != nil {
		t.Fatalf("Enable: %v", err)
	}

	stopped := make(chan struct{})
	go func() { mgr.Stop(); close(stopped) }()

	select {
	case <-stopped:
		t.Fatal("Stop returned while the controller was still running")
	case <-time.After(50 * time.Millisecond):
	}

	close(keepRunning)

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return after the controller exited")
	}

	select {
	case <-runDone:
	default:
		t.Error("controller still running after Stop returned")
	}
}

func TestEnableRacingStopNeverOutlivesIt(t *testing.T) {
	t.Parallel()

	mgr := newTestManager(t)

	// Build honours the manager context, as the Spec contract requires, so
	// the start it owns winds down when Stop cancels.
	spec := noopStub()
	spec.Build = func(ctx context.Context, _ *controllers.Manager) (controllers.Run, error) {
		<-ctx.Done()

		return nil, ctx.Err()
	}

	if err := mgr.Register(spec); err != nil {
		t.Fatalf("Register: %v", err)
	}

	const callers = 8

	var wg sync.WaitGroup

	errs := make([]error, callers)
	for i := range callers {
		wg.Go(func() {
			errs[i] = mgr.Enable(spec.Name)
		})
	}

	stopped := make(chan struct{})
	go func() { mgr.Stop(); close(stopped) }()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return while enables were in flight")
	}

	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Errorf("Enable #%d succeeded across a Stop, want an error", i)
		}
	}
}

func TestRegisterValidatesSpecs(t *testing.T) {
	t.Parallel()

	mgr := newTestManager(t)

	var builds atomic.Int64

	if err := mgr.Register(controllers.Spec{}); err == nil {
		t.Error("Register with an empty spec succeeded, want an error")
	}

	if err := mgr.Register(controllers.Spec{Name: "buildless"}); err == nil {
		t.Error("Register without a build function succeeded, want an error")
	}

	if err := mgr.Register(countingStub("dup", &builds), countingStub("dup", &builds)); err == nil {
		t.Error("Register with a duplicate name succeeded, want an error")
	}

	err := mgr.Enable("dup")
	if err == nil || !strings.Contains(err.Error(), `unknown controller "dup"`) {
		t.Errorf("Enable after a rejected Register error = %v, want an unknown-controller failure", err)
	}
}
