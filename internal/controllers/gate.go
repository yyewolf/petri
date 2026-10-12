package controllers

import (
	"context"
	"errors"
	"sync"
	"time"

	appsinformers "k8s.io/client-go/informers/apps/v1"
	batchinformers "k8s.io/client-go/informers/batch/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"
	batchlisters "k8s.io/client-go/listers/batch/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// Upstream controllers register their event handlers on their informers and
// assign their listers only after that, an order kube-controller-manager can
// afford because its informers all start with an empty store. petri enables
// controllers on planes that already hold objects, and a started informer
// answers AddEventHandler by replaying its whole store through the new
// handler on listener goroutines while the constructor is still assigning
// fields — a replayed orphan pod crashes the StatefulSet controller's addPod
// on its nil setLister. The build gate buffers the registrations a
// constructor makes and flushes them once that constructor returned, so a
// replay always fires into a whole controller. Build functions wrap every
// informer they hand a constructor with the gated helpers below.

// buildGate buffers event-handler registrations until flush is called.
type buildGate struct {
	mu      sync.Mutex
	pending []func() (cache.ResourceEventHandlerRegistration, error)
	// done marks the flush: later registrations go straight through, so the
	// gate is transparent once the constructor it guards has returned.
	done bool
}

// newBuildGate returns a closed gate: every registration buffers until flush.
func newBuildGate() *buildGate {
	return &buildGate{}
}

// add buffers one registration until flush. A buffered registration has not
// run yet, so it reports success and lets flush surface a failure to the
// manager, which fails the start; the constructors only check the error.
func (g *buildGate) add(
	register func() (cache.ResourceEventHandlerRegistration, error),
) (cache.ResourceEventHandlerRegistration, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.done {
		return register()
	}

	g.pending = append(g.pending, register)

	//nolint:nilnil // A buffered registration has not run yet; flush reports its outcome.
	return nil, nil
}

// flush runs the buffered registrations and opens the gate. It is called
// exactly once, after the guarded constructor returned.
func (g *buildGate) flush() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.done = true

	var errs []error
	for _, register := range g.pending {
		if _, err := register(); err != nil {
			errs = append(errs, err)
		}
	}
	g.pending = nil

	return errors.Join(errs...)
}

// gatedInformer wraps a typed informer so its Informer registers through the
// build gate. The lister passes through untouched.
type gatedInformer[L any] struct {
	informer cache.SharedIndexInformer
	lister   L
	gate     *buildGate
}

// Informer returns the informer with gated handler registration.
func (w gatedInformer[L]) Informer() cache.SharedIndexInformer {
	return gatedSharedIndexInformer{informer: w.informer, gate: w.gate}
}

// Lister returns the wrapped informer's lister.
func (w gatedInformer[L]) Lister() L { return w.lister }

// gatedSharedIndexInformer delegates a shared informer, intercepting only the
// event-handler registrations.
type gatedSharedIndexInformer struct {
	informer cache.SharedIndexInformer
	gate     *buildGate
}

func (w gatedSharedIndexInformer) AddEventHandler(
	handler cache.ResourceEventHandler,
) (cache.ResourceEventHandlerRegistration, error) {
	return w.gate.add(func() (cache.ResourceEventHandlerRegistration, error) {
		return w.informer.AddEventHandler(handler)
	})
}

func (w gatedSharedIndexInformer) AddEventHandlerWithResyncPeriod(
	handler cache.ResourceEventHandler, resyncPeriod time.Duration,
) (cache.ResourceEventHandlerRegistration, error) {
	return w.gate.add(func() (cache.ResourceEventHandlerRegistration, error) {
		return w.informer.AddEventHandlerWithResyncPeriod(handler, resyncPeriod)
	})
}

func (w gatedSharedIndexInformer) AddEventHandlerWithOptions(
	handler cache.ResourceEventHandler, options cache.HandlerOptions,
) (cache.ResourceEventHandlerRegistration, error) {
	return w.gate.add(func() (cache.ResourceEventHandlerRegistration, error) {
		return w.informer.AddEventHandlerWithOptions(handler, options)
	})
}

func (w gatedSharedIndexInformer) RemoveEventHandler(handle cache.ResourceEventHandlerRegistration) error {
	return w.informer.RemoveEventHandler(handle)
}

func (w gatedSharedIndexInformer) GetStore() cache.Store {
	return w.informer.GetStore()
}

func (w gatedSharedIndexInformer) GetController() cache.Controller {
	return w.informer.GetController()
}

func (w gatedSharedIndexInformer) GetIndexer() cache.Indexer {
	return w.informer.GetIndexer()
}

func (w gatedSharedIndexInformer) AddIndexers(indexers cache.Indexers) error {
	return w.informer.AddIndexers(indexers)
}

func (w gatedSharedIndexInformer) Run(stopCh <-chan struct{}) {
	w.informer.Run(stopCh)
}

func (w gatedSharedIndexInformer) RunWithContext(ctx context.Context) {
	w.informer.RunWithContext(ctx)
}

func (w gatedSharedIndexInformer) HasSynced() bool {
	return w.informer.HasSynced()
}

func (w gatedSharedIndexInformer) HasSyncedChecker() cache.DoneChecker {
	return w.informer.HasSyncedChecker()
}

func (w gatedSharedIndexInformer) LastSyncResourceVersion() string {
	return w.informer.LastSyncResourceVersion()
}

func (w gatedSharedIndexInformer) SetWatchErrorHandler(handler cache.WatchErrorHandler) error {
	return w.informer.SetWatchErrorHandler(handler)
}

func (w gatedSharedIndexInformer) SetWatchErrorHandlerWithContext(
	handler cache.WatchErrorHandlerWithContext,
) error {
	return w.informer.SetWatchErrorHandlerWithContext(handler)
}

func (w gatedSharedIndexInformer) SetTransform(handler cache.TransformFunc) error {
	return w.informer.SetTransform(handler)
}

func (w gatedSharedIndexInformer) IsStopped() bool {
	return w.informer.IsStopped()
}

// The gated helpers below wrap one typed informer each, for the build
// functions to hand their constructors.

func gatedPods(in coreinformers.PodInformer, g *buildGate) coreinformers.PodInformer {
	return gatedInformer[corelisters.PodLister]{informer: in.Informer(), lister: in.Lister(), gate: g}
}

func gatedNodes(in coreinformers.NodeInformer, g *buildGate) coreinformers.NodeInformer {
	return gatedInformer[corelisters.NodeLister]{informer: in.Informer(), lister: in.Lister(), gate: g}
}

func gatedPersistentVolumeClaims(
	in coreinformers.PersistentVolumeClaimInformer, g *buildGate,
) coreinformers.PersistentVolumeClaimInformer {
	return gatedInformer[corelisters.PersistentVolumeClaimLister]{
		informer: in.Informer(), lister: in.Lister(), gate: g,
	}
}

func gatedDeployments(in appsinformers.DeploymentInformer, g *buildGate) appsinformers.DeploymentInformer {
	return gatedInformer[appslisters.DeploymentLister]{informer: in.Informer(), lister: in.Lister(), gate: g}
}

func gatedReplicaSets(in appsinformers.ReplicaSetInformer, g *buildGate) appsinformers.ReplicaSetInformer {
	return gatedInformer[appslisters.ReplicaSetLister]{informer: in.Informer(), lister: in.Lister(), gate: g}
}

func gatedStatefulSets(in appsinformers.StatefulSetInformer, g *buildGate) appsinformers.StatefulSetInformer {
	return gatedInformer[appslisters.StatefulSetLister]{informer: in.Informer(), lister: in.Lister(), gate: g}
}

func gatedDaemonSets(in appsinformers.DaemonSetInformer, g *buildGate) appsinformers.DaemonSetInformer {
	return gatedInformer[appslisters.DaemonSetLister]{informer: in.Informer(), lister: in.Lister(), gate: g}
}

func gatedControllerRevisions(
	in appsinformers.ControllerRevisionInformer, g *buildGate,
) appsinformers.ControllerRevisionInformer {
	return gatedInformer[appslisters.ControllerRevisionLister]{
		informer: in.Informer(), lister: in.Lister(), gate: g,
	}
}

func gatedJobs(in batchinformers.JobInformer, g *buildGate) batchinformers.JobInformer {
	return gatedInformer[batchlisters.JobLister]{informer: in.Informer(), lister: in.Lister(), gate: g}
}

func gatedCronJobs(in batchinformers.CronJobInformer, g *buildGate) batchinformers.CronJobInformer {
	return gatedInformer[batchlisters.CronJobLister]{informer: in.Informer(), lister: in.Lister(), gate: g}
}
