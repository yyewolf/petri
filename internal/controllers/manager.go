// Package controllers runs petri's built-in controllers on a control plane.
// One manager per control plane owns the shared informer factories and the
// RESTMapper, and enables controllers from its registry by name.
package controllers

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	metadatainformer "k8s.io/client-go/metadata/metadatainformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

const (
	// informerResyncPeriod matches kube-controller-manager's default factory
	// resync. A test control plane finishes long before a resync fires, but
	// upstream controllers treat periodic resyncs as a correctness backstop,
	// and the namespace controller takes the period as an argument.
	informerResyncPeriod = 10 * time.Hour

	// sharedInformersAgent names the informer factories in the API server.
	// They serve every enabled controller, so they carry a user agent of
	// their own instead of any single controller's.
	sharedInformersAgent = "shared-informers"

	// discoveryAgent names the RESTMapper's discovery client, which caches
	// the API server's resource list for every controller.
	discoveryAgent = "discovery"
)

// Manager runs petri's built-in controllers on one control plane. It owns the
// shared informer factories, the RESTMapper and the registry the controllers
// are enabled from. One manager belongs to one control plane: controllers run
// on its context and Stop releases everything the manager started.
type Manager struct {
	ctx    context.Context
	cancel context.CancelFunc

	// config is the admin config controllers build their own clients from,
	// so every controller carries a user agent of its own.
	config *rest.Config

	kubeFactory informers.SharedInformerFactory
	metaFactory metadatainformer.SharedInformerFactory
	mapper      meta.ResettableRESTMapper

	// informersStarted is closed once the informers of the first enabled
	// controller are running. Upstream constructors take it as an argument
	// and wait for it before wiring their event handlers, so they do not see
	// events on work queues that are not set up yet.
	informersStarted chan struct{}
	startedOnce      sync.Once

	wg sync.WaitGroup

	// buildMu serializes spec.Build calls, and build holds the gate of the
	// constructor in flight while it does. The upstream constructors are
	// neither safe to run concurrently (five of them add the same indexer to
	// the shared Pod informer through a check-then-add, and the Deployment
	// and Job constructors fail their whole start when a concurrent build
	// loses that race) nor safe against a started informer replaying its
	// store into their half-assigned fields (see gate.go); serializing and
	// gating restores kube-controller-manager's implicit ordering without
	// touching the constructors.
	buildMu sync.Mutex
	build   *buildGate

	mu     sync.Mutex
	specs  map[string]*Spec
	starts map[string]*controllerStart
	// stopped is set by Stop before it cancels, so an Enable that acquires
	// the lock afterwards refuses to start anything the Wait below could
	// miss.
	stopped bool

	// serviceAccountSigningKey is the private key the API server signs
	// service-account tokens with, shared through WithServiceAccountSigningKey
	// so the token controller's Secret tokens verify against the API server's
	// public keys. It is nil on managers wired without it, which only the
	// token controller's build notices.
	serviceAccountSigningKey *ecdsa.PrivateKey

	// optionalOnce registers every optional controller's specs with the
	// registry on the first EnableControllers call, and optionalErr records
	// the outcome: the specs are static, so a Register failure there is a
	// petri bug.
	optionalOnce sync.Once
	optionalErr  error
}

// Option adjusts how the manager is wired to its control plane.
type Option func(*Manager)

// WithServiceAccountSigningKey shares the control plane's in-memory
// service-account signing key with the manager. The token controller needs it:
// its Secret tokens must verify against the API server's public keys, which
// are the public half of this key (see apiserver.go, which generates one key
// per plane and configures the API server with it).
func WithServiceAccountSigningKey(key *ecdsa.PrivateKey) Option {
	return func(m *Manager) { m.serviceAccountSigningKey = key }
}

// New builds a manager for the control plane reachable through cfg, which
// must be petri's admin config. Controllers run on ctx; Stop cancels and
// releases everything the manager started.
//
// Construction starts no goroutines and makes no API calls: informers start
// when a controller is enabled and the RESTMapper discovers on first use, so
// building the manager at control-plane start costs nothing measurable.
func New(ctx context.Context, cfg *rest.Config, opts ...Option) (*Manager, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &Manager{
		ctx:              ctx,
		cancel:           cancel,
		config:           cfg,
		informersStarted: make(chan struct{}),
		specs:            make(map[string]*Spec),
		starts:           make(map[string]*controllerStart),
	}

	for _, opt := range opts {
		opt(m)
	}

	factoryCfg := m.controllerConfig(sharedInformersAgent)

	kubeClient, err := kubernetes.NewForConfig(factoryCfg)
	if err != nil {
		cancel()

		return nil, fmt.Errorf("petri: build shared-informers client: %w", err)
	}

	m.kubeFactory = informers.NewSharedInformerFactory(kubeClient, informerResyncPeriod)

	metaClient, err := metadata.NewForConfig(factoryCfg)
	if err != nil {
		cancel()

		return nil, fmt.Errorf("petri: build shared metadata client: %w", err)
	}

	m.metaFactory = metadatainformer.NewSharedInformerFactory(metaClient, informerResyncPeriod)

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(m.controllerConfig(discoveryAgent))
	if err != nil {
		cancel()

		return nil, fmt.Errorf("petri: build discovery client: %w", err)
	}

	// The deferred mapper discovers on first use and can be Reset when the
	// served API changes, which the garbage collector relies on.
	m.mapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient))

	return m, nil
}

// Stop cancels the manager's context and waits for everything it started:
// the enabled controllers and the informer goroutines the factories run.
// Calling Stop more than once is safe.
func (m *Manager) Stop() {
	// Publish the stop under the registry lock before cancelling: an Enable
	// that acquires the lock afterwards must not reserve a WaitGroup slot
	// the Wait below would not see.
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()

	m.cancel()

	// Shutdown joins the informer goroutines, which exit because the context
	// above closed their stop channels.
	m.kubeFactory.Shutdown()
	m.metaFactory.Shutdown()

	m.wg.Wait()
}

// KubeClient returns an admin clientset for the named controller, stamped
// with a user agent that names it. Controllers share no client: the API
// server can attribute each controller's requests.
func (m *Manager) KubeClient(name string) (kubernetes.Interface, error) {
	client, err := kubernetes.NewForConfig(m.controllerConfig(name))
	if err != nil {
		return nil, fmt.Errorf("petri: build %s controller client: %w", name, err)
	}

	return client, nil
}

// MetaClient returns an admin metadata client for the named controller. The
// garbage collector and the namespace controller read object metadata through
// it instead of full typed clients.
func (m *Manager) MetaClient(name string) (metadata.Interface, error) {
	client, err := metadata.NewForConfig(m.controllerConfig(name))
	if err != nil {
		return nil, fmt.Errorf("petri: build %s controller metadata client: %w", name, err)
	}

	return client, nil
}

// KubeFactory returns the typed informer factory the manager starts informers
// on. Controllers register the informers they need while they are built.
func (m *Manager) KubeFactory() informers.SharedInformerFactory {
	return m.kubeFactory
}

// MetaFactory returns the metadata informer factory, which the garbage
// collector's graph builder needs for resources without a typed informer.
func (m *Manager) MetaFactory() metadatainformer.SharedInformerFactory {
	return m.metaFactory
}

// Mapper returns the shared RESTMapper, which maps resources to kinds from
// the API server's discovery documents.
func (m *Manager) Mapper() meta.ResettableRESTMapper {
	return m.mapper
}

// InformersStarted returns the signal upstream constructors take. It closes
// once the informers of the first enabled controller are running, before that
// controller runs.
func (m *Manager) InformersStarted() <-chan struct{} {
	return m.informersStarted
}

// constructorGate returns the gate of the build in flight. Build functions
// wrap every informer they hand a constructor with it, so no event can fire
// into a half-built controller; it is nil outside a build.
func (m *Manager) constructorGate() *buildGate {
	return m.build
}

// controllerConfig copies the admin config and stamps a user agent naming the
// client, so the API server can attribute its requests, as upstream
// controllers do with their component names.
func (m *Manager) controllerConfig(name string) *rest.Config {
	cfg := rest.CopyConfig(m.config)
	cfg.UserAgent = "petri/" + name

	return cfg
}
