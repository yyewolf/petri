package petri

import (
	"errors"
	"slices"
)

// schedulingMode records who places Pods on a dedicated control plane:
// kube-scheduler, or the test through Bind and Pod expectations. Mixing them
// would race: the scheduler would bind the Pods a test expects to place.
type schedulingMode int

const (
	schedulingUnset schedulingMode = iota
	schedulingByScheduler
	schedulingManual
)

var (
	errSchedulerOnShared = errors.New(
		"petri: the scheduler places Pods on Nodes, which only dedicated control planes have; " +
			"use petri.New(ctx)")
	errManualWithScheduler = errors.New(
		"petri: kube-scheduler runs on this control plane, so Pods cannot be placed by hand: " +
			"Bind and Pod expectations need a plane without SchedulerController")
	errSchedulerWithManual = errors.New(
		"petri: this control plane places Pods by hand (Bind or Pod expectations), " +
			"so SchedulerController cannot be enabled on it")
	errManualOnShared = errors.New(
		"petri: placing Pods by hand needs Nodes, which only dedicated control planes have; " +
			"use petri.New(ctx)")
)

// claimScheduling records mode as the plane's way of placing Pods, failing
// when the other one is already in use.
func (cp *ControlPlane) claimScheduling(mode schedulingMode) error {
	if cp.shared {
		if mode == schedulingByScheduler {
			return errSchedulerOnShared
		}

		return errManualOnShared
	}

	cp.nodesMu.Lock()
	defer cp.nodesMu.Unlock()

	switch {
	case cp.scheduling == schedulingUnset || cp.scheduling == mode:
		cp.scheduling = mode

		return nil
	case mode == schedulingManual:
		return errManualWithScheduler
	default:
		return errSchedulerWithManual
	}
}

// claimSchedulerFor claims the scheduler mode when cs names the scheduler.
func (cp *ControlPlane) claimSchedulerFor(cs []Controller) error {
	if !slices.Contains(cs, SchedulerController) {
		return nil
	}

	return cp.claimScheduling(schedulingByScheduler)
}
