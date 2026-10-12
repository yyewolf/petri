// Package envtest is a drop-in replacement for
// sigs.k8s.io/controller-runtime/pkg/envtest: it runs a real kube-apiserver
// inside the test process instead of downloading binaries, so a kubebuilder
// test suite switches to petri by changing its import path.
//
// # Differences from envtest
//
// The control plane runs petri's default controllers, the namespace controller
// and the garbage collector, where envtest runs no controllers at all. That is
// deliberate, because running petri's controllers is the drop-in's value:
//
//   - A deleted namespace really goes away. envtest leaves it Terminating
//     forever, and so does an environment started with
//     DisableDefaultControllers.
//   - Deleting an owner cascades to its dependents, with foreground,
//     background and orphan propagation behaving as on a real cluster.
//
// The one surprise this can cause: a namespace whose custom resource carries a
// finalizer nobody removes stays Terminating. That is correct namespace and
// garbage-collection behaviour — the namespace waits for the stuck object —
// not a petri bug. Remove the finalizer in the test's cleanup, or start the
// environment with DisableDefaultControllers for strict envtest parity.
//
// The other petri controllers, the workload controllers among them, can be
// enabled on the environment with EnableControllers.
package envtest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"

	"github.com/yyewolf/petri"
)

// startTimeout bounds starting the control plane and installing the
// manifests.
const startTimeout = 2 * time.Minute

// Environment mirrors the envtest Environment for the fields kubebuilder
// suites actually set. Start returns a rest.Config just like envtest does, and
// Stop releases everything.
//
// Each Start runs a dedicated control plane, as envtest runs a dedicated one.
// Nothing is written to disk except a webhook serving certificate directory,
// which Stop removes.
type Environment struct {
	// CRDDirectoryPaths lists files and directories of CRD manifests to
	// install.
	CRDDirectoryPaths []string
	// CRDs are CRD objects to install, in addition to the manifests.
	CRDs []*apiextensionsv1.CustomResourceDefinition
	// ErrorIfCRDPathMissing exists for envtest compatibility only: petri
	// always fails on a missing or manifest-less CRD path, where envtest
	// silently skips it when this is false.
	ErrorIfCRDPathMissing bool
	// CRDInstallOptions are merged with the fields above.
	CRDInstallOptions CRDInstallOptions
	// WebhookInstallOptions describe the webhook manifests to install and the
	// local webhook server to point them at. Start fills in
	// LocalServingHost, LocalServingPort and LocalServingCertDir.
	WebhookInstallOptions WebhookInstallOptions
	// Scheme is carried for the suite's own clients, as suites commonly wire
	// it into both. It changes nothing about the environment itself.
	Scheme *runtime.Scheme
	// AttachControlPlaneOutput mirrors envtest's field: it makes the control
	// plane log to stderr, like setting PETRI_VERBOSE=1.
	AttachControlPlaneOutput bool
	// DisableDefaultControllers starts the control plane without petri's
	// default controllers, the namespace controller and the garbage
	// collector. The drop-in runs them by default because that is petri's
	// value: deleted namespaces really go away and owner deletion cascades.
	// Set it for tests that rely on envtest semantics, where no controllers
	// run: deleted namespaces then stay Terminating with their contents, and
	// a namespace stuck on a finalizer looks like any other Terminating
	// namespace instead of waiting for its contents to be freed. The zero
	// value keeps the petri defaults, so existing suites are unaffected.
	DisableDefaultControllers bool

	// UseExistingCluster is meaningless here: petri never reuses a cluster.
	// Setting it fails Start, because ignoring it would change what the test
	// tests.
	UseExistingCluster bool
	// BinaryAssetsDirectory is meaningless here: no binaries are used.
	// Setting it fails Start, because the setup-envtest step it belongs to
	// would otherwise do nothing.
	BinaryAssetsDirectory string

	mu          sync.Mutex
	everStarted bool
	cp          *petri.ControlPlane
	cleanup     func() error
	certDir     string
	tmpCert     bool
}

// Start starts a dedicated control plane, installs the CRDs and webhook
// configurations, and returns the admin rest.Config, like envtest does.
func (e *Environment) Start() (*rest.Config, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.cp != nil {
		return nil, errors.New("petri envtest: environment is already started")
	}
	if e.UseExistingCluster {
		return nil, errors.New(
			"petri envtest: UseExistingCluster is not supported; petri always starts its own control plane",
		)
	}
	if e.BinaryAssetsDirectory != "" {
		return nil, errors.New(
			"petri envtest: BinaryAssetsDirectory is not supported; petri runs no binaries, so setup-envtest can be dropped",
		)
	}
	if e.CRDInstallOptions.CleanUpAfterUse {
		return nil, errors.New(
			"petri envtest: CleanUpAfterUse is not supported; CRDs are never deleted, because other tests on a shared control plane may still use them",
		)
	}

	if e.AttachControlPlaneOutput {
		// petri reads this at control plane startup.
		if err := os.Setenv("PETRI_VERBOSE", "1"); err != nil {
			return nil, fmt.Errorf("petri envtest: enable control plane output: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()

	// The zero value keeps today's behaviour: the petri defaults run, so an
	// existing suite never changes. Only an explicit opt-out turns them off.
	var opts []petri.Option
	if e.DisableDefaultControllers {
		opts = append(opts, petri.WithoutDefaultControllers())
	}

	cp, err := petri.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("petri envtest: start control plane: %w", err)
	}
	e.cp = cp
	e.everStarted = true

	if installErr := e.installCRDs(ctx); installErr != nil {
		return nil, errors.Join(installErr, e.release())
	}

	cleanup, err := e.installWebhooks(ctx)
	if err != nil {
		return nil, errors.Join(err, e.release())
	}

	e.cleanup = cleanup

	return cp.Config(), nil
}

// Stop removes the installed webhook configurations, shuts the control plane
// down and drops the certificate directory. It is safe to call more than once:
// later calls do nothing. Stop without a Start fails, since it would hide a
// suite that never started its environment.
func (e *Environment) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.everStarted {
		return errors.New("petri envtest: environment was not started")
	}

	if e.cp == nil {
		return nil
	}

	return e.release()
}

// EnableControllers enables petri's built-in controllers on the environment's
// control plane, like ControlPlane.EnableControllers on a plane the suite
// owns. It exists because Start hands out only a rest.Config: without it, a
// drop-in suite could never reach petri's workload controllers. It fails the
// test when the environment is not running.
func (e *Environment) EnableControllers(t testing.TB, cs ...petri.Controller) {
	t.Helper()

	e.mu.Lock()
	cp := e.cp
	e.mu.Unlock()

	if cp == nil {
		t.Fatal("petri envtest: enable controllers before Start or after Stop")

		return
	}

	cp.EnableControllers(t, cs...)
}

// release stops the control plane and undoes everything Start set up. The
// caller holds the mutex.
func (e *Environment) release() error {
	var errs []error

	if e.cleanup != nil {
		if err := e.cleanup(); err != nil {
			errs = append(errs, err)
		}

		e.cleanup = nil
	}

	if err := e.cp.Stop(); err != nil {
		errs = append(errs, fmt.Errorf("petri envtest: stop control plane: %w", err))
	}
	e.cp = nil

	if e.tmpCert && e.certDir != "" {
		if err := os.RemoveAll(e.certDir); err != nil {
			errs = append(errs, fmt.Errorf("petri envtest: remove certificate dir: %w", err))
		}

		e.tmpCert = false
	}
	e.certDir = ""

	return errors.Join(errs...)
}

// installCRDs installs the CRDs from every source.
func (e *Environment) installCRDs(ctx context.Context) error {
	sources := make(
		[]any, 0, len(e.CRDDirectoryPaths)+len(e.CRDs)+len(e.CRDInstallOptions.Paths)+len(e.CRDInstallOptions.CRDs))
	for _, path := range e.CRDDirectoryPaths {
		sources = append(sources, path)
	}
	for _, path := range e.CRDInstallOptions.Paths {
		sources = append(sources, path)
	}
	if len(e.CRDs) > 0 {
		sources = append(sources, e.CRDs)
	}
	if len(e.CRDInstallOptions.CRDs) > 0 {
		sources = append(sources, e.CRDInstallOptions.CRDs)
	}

	if err := e.cp.InstallCRDsCtx(ctx, sources...); err != nil {
		return fmt.Errorf("petri envtest: install crds: %w", err)
	}

	return nil
}

// installWebhooks writes the serving certificate, allocates the port and
// installs the webhook configurations, returning their cleanup. Without
// webhook options it is a no-op.
func (e *Environment) installWebhooks(ctx context.Context) (func() error, error) {
	opts := &e.WebhookInstallOptions

	sources := make([]any, 0, len(opts.Paths)+len(opts.MutatingWebhooks)+len(opts.ValidatingWebhooks))
	sources = append(sources, opts.Paths)
	sources = append(sources, opts.MutatingWebhooks)
	sources = append(sources, opts.ValidatingWebhooks)
	if len(sources) == 0 {
		return func() error { return nil }, nil
	}

	certDir := opts.LocalServingCertDir
	if certDir == "" {
		var err error

		certDir, err = os.MkdirTemp("", "petri-envtest-certs-")
		if err != nil {
			return nil, fmt.Errorf("petri envtest: create certificate dir: %w", err)
		}

		e.certDir, e.tmpCert = certDir, true
	}

	if err := e.cp.WriteServingCert(certDir); err != nil {
		return nil, err
	}

	host := opts.LocalServingHost
	if host == "" {
		host = "127.0.0.1"
	}

	port := opts.LocalServingPort
	if port == 0 {
		allocated, err := freePort(host)
		if err != nil {
			return nil, err
		}

		port = allocated
	}

	opts.LocalServingHost = host
	opts.LocalServingPort = port
	opts.LocalServingCertDir = certDir

	cleanup, err := e.cp.InstallAdmissionWebhooksCtx(ctx, petri.AdmissionWebhookOptions{
		Host: host,
		Port: port,
	}, sources...)
	if err != nil {
		return nil, fmt.Errorf("petri envtest: install webhooks: %w", err)
	}

	return cleanup, nil
}

// freePort finds a free TCP port on host, the way envtest allocates its
// webhook port.
func freePort(host string) (int, error) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, fmt.Errorf("petri envtest: allocate webhook port: %w", err)
	}

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("petri envtest: unexpected listener address %v", ln.Addr())
	}

	if closeErr := ln.Close(); closeErr != nil {
		return 0, fmt.Errorf("petri envtest: allocate webhook port: %w", closeErr)
	}

	return addr.Port, nil
}
