package petri

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

	once     sync.Once
	cp       *ControlPlane
	err      error
	reported atomic.Bool // whether a caller already got err with the startup logs
}

// Start returns the control plane shared by every test in the binary,
// starting it on first use. If startup fails, every caller fails; only the
// first one prints the startup logs. The shared control plane lives until the
// test binary exits.
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
		if s.cp != nil {
			// Mark before the plane is handed out: conversion webhooks
			// rewrite CRDs for every test, so they are refused here.
			s.cp.shared = true
		}
	})

	if s.err == nil || !s.reported.Swap(true) {
		return s.cp, s.err
	}

	// The logs went to the first caller; repeating them in every test buries
	// the output.
	cause := s.err
	if se, ok := errors.AsType[*startError](s.err); ok {
		cause = se.err
	}

	return nil, fmt.Errorf(
		"petri: shared control plane failed to start (logs printed by the first test that called Start): %w", cause)
}
