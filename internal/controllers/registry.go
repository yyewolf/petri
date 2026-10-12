package controllers

import (
	"context"
	"errors"
	"fmt"
)

// Spec describes one controller the manager can enable.
type Spec struct {
	// Name keys the controller in the registry and becomes its user agent.
	Name string

	// Build constructs the controller against the manager. It registers the
	// informers the controller needs on the manager's factories and returns
	// the function that runs it. It must not start anything itself, and it
	// must return once ctx is cancelled.
	Build func(ctx context.Context, m *Manager) (Run, error)
}

// Run runs a constructed controller until its context is cancelled. It
// matches the upstream controllers' Run methods, which log their own errors
// and return nothing.
type Run func(ctx context.Context)

// controllerStart tracks one start of a controller. The first Enable call
// owns it; callers that arrived meanwhile wait on done for the outcome.
type controllerStart struct {
	done chan struct{}
	err  error // the start's outcome, read after done closes
}

// Register adds controllers to the registry so Enable can start them by name.
// It fails when two controllers share a name, which would make enabling
// ambiguous, or when a spec is incomplete.
func (m *Manager) Register(specs ...Spec) error {
	for _, spec := range specs {
		switch {
		case spec.Name == "":
			return errors.New("petri: controller name is empty")
		case spec.Build == nil:
			return fmt.Errorf("petri: controller %q has no build function", spec.Name)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	seen := make(map[string]struct{}, len(specs))

	for _, spec := range specs {
		if _, dup := m.specs[spec.Name]; dup {
			return fmt.Errorf("petri: controller %q is registered twice", spec.Name)
		}

		if _, dup := seen[spec.Name]; dup {
			return fmt.Errorf("petri: controller %q is registered twice", spec.Name)
		}

		seen[spec.Name] = struct{}{}
	}

	for i := range specs {
		// Copy each spec into its own pointer: Register is variadic, so
		// storing &specs[i] would alias the caller's backing array and let a
		// later mutation silently change the registry.
		spec := specs[i]
		m.specs[spec.Name] = &spec
	}

	return nil
}

// Enable starts the named controller and returns once its informers are
// synced, so objects created right after it returns are seen by the
// controller. Enabling one controller never starts another.
//
// Calls coalesce: while a controller is starting or running, later Enable
// calls neither build a second copy nor disturb the first one. They wait for
// the start in flight, or return at once for a running controller. A start
// that failed is forgotten, so a later call retries it from scratch. Enable
// after Stop fails: the manager starts nothing it would not wait for.
func (m *Manager) Enable(name string) error {
	m.mu.Lock()
	spec, ok := m.specs[name]
	if !ok {
		m.mu.Unlock()

		return fmt.Errorf("petri: unknown controller %q", name)
	}

	start, ok := m.starts[name]
	if ok {
		m.mu.Unlock()

		// The controller is starting or running: leave it alone and share
		// its outcome.
		<-start.done

		return start.err
	}

	if m.stopped {
		m.mu.Unlock()

		return errors.New("petri: control plane is stopping")
	}

	start = &controllerStart{done: make(chan struct{})}
	m.starts[name] = start
	// Reserve the manager WaitGroup slot under the lock that publishes the
	// start: Stop's Wait then cannot overtake the controller goroutine,
	// however the start ends.
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()

		run, err := m.buildAndStart(spec)
		if err != nil {
			start.err = err

			// A failed start is retryable: forget it, so the next Enable
			// call starts from scratch.
			m.mu.Lock()
			delete(m.starts, name)
			m.mu.Unlock()

			close(start.done)

			return
		}

		close(start.done)

		// This goroutine runs the controller until Stop cancels its context;
		// the WaitGroup slot tracks it for the whole run.
		run(m.ctx)
	}()

	<-start.done

	return start.err
}

// buildAndStart builds the controller, starts the informers it registered and
// waits for their caches. It returns the function that runs the controller,
// or the reason it never started.
func (m *Manager) buildAndStart(spec *Spec) (Run, error) {
	if err := m.ctx.Err(); err != nil {
		return nil, fmt.Errorf("petri: control plane is stopping: %w", err)
	}

	// The constructors run serialized under the build lock and behind the
	// build gate; see Manager.buildMu and gate.go for why. Everything after
	// — factory start, sync wait — runs concurrently, as informers and
	// caches are safe for it.
	m.buildMu.Lock()
	gate := newBuildGate()
	m.build = gate

	run, err := spec.Build(m.ctx, m)
	if err == nil {
		// The constructor is complete: register its buffered handlers now,
		// so a started informer's replay fires into a whole controller.
		err = gate.flush()
	}

	m.build = nil
	m.buildMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("petri: build %s controller: %w", spec.Name, err)
	}

	// The constructor registered its informers: start them before the
	// controller runs, and close the informers-started signal so upstream
	// constructors waiting on it (the garbage collector's graph builder)
	// proceed against running informers.
	m.kubeFactory.StartWithContext(m.ctx)
	m.metaFactory.Start(m.ctx.Done())

	m.startedOnce.Do(func() { close(m.informersStarted) })

	err = m.waitForSync()
	if err != nil {
		return nil, fmt.Errorf("petri: %s controller informers: %w", spec.Name, err)
	}

	// A Stop that raced the start shut the factories down before their
	// informers ran; the controller would never sync anything.
	err = m.ctx.Err()
	if err != nil {
		return nil, fmt.Errorf("petri: control plane is stopping: %w", err)
	}

	return run, nil
}

// waitForSync waits for every started informer's cache and fails fast when
// the manager's context is cancelled.
func (m *Manager) waitForSync() error {
	if err := m.kubeFactory.WaitForCacheSyncWithContext(m.ctx).AsError(); err != nil {
		return err
	}

	for gvr, synced := range m.metaFactory.WaitForCacheSync(m.ctx.Done()) {
		if !synced {
			if cause := context.Cause(m.ctx); cause != nil {
				return cause
			}

			return fmt.Errorf("%s never synced", gvr)
		}
	}

	return nil
}
