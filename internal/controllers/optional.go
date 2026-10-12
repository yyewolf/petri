package controllers

import (
	"fmt"
	"slices"
)

// OptionalSpecs returns the registry specs of every controller petri can
// enable by name: the workload set (workloads.go), the EndpointSlice
// controller, the ServiceAccount and token controllers, ResourceQuota and
// disruption. None of them run by default — the default set is the namespace
// controller and the garbage collector — and enabling one never enables
// another: a ServiceAccount without the token controller creates no token
// Secrets, and a ResourceQuota without the quota controller is not enforced
// by anything but admission's live usage check.
func OptionalSpecs() []Spec {
	return slices.Concat(
		WorkloadSpecs(),
		EndpointSliceSpecs(),
		ServiceAccountSpecs(),
		ResourceQuotaSpecs(),
		DisruptionSpecs(),
	)
}

// EnableControllers registers every optional controller's specs with the
// registry once and enables the named ones in order, returning once the last
// one's informers are synced. Enabling a controller that already runs is a
// no-op and the calls are safe to make concurrently: the registry coalesces
// starts, as it does for every controller.
//
// Registering the specs is inert — nothing is built until its name is
// enabled — so the union is registered however small the request.
func (m *Manager) EnableControllers(names ...string) error {
	m.optionalOnce.Do(func() {
		m.optionalErr = m.Register(OptionalSpecs()...)
	})
	if m.optionalErr != nil {
		// The spec list is a compile-time constant; reaching this means
		// petri registered a duplicate name by mistake.
		return fmt.Errorf("petri: register controllers: %w", m.optionalErr)
	}

	for _, name := range names {
		if err := m.Enable(name); err != nil {
			return err
		}
	}

	return nil
}
