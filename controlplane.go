package petri

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	aggregatorapiserver "k8s.io/kube-aggregator/pkg/apiserver"
)

const (
	clientQPS         = 1000
	clientBurst       = 10000
	readyPollInterval = 100 * time.Millisecond
	readyTimeout      = 60 * time.Second
)

// ControlPlane is a kube-apiserver running in this process over an in-memory
// store. Get the shared one with Start, or a dedicated one with New.
type ControlPlane struct {
	config     *rest.Config
	client     *kubernetes.Clientset
	stopServer context.CancelFunc // stops kube-apiserver
	stopKine   context.CancelFunc // stops kine; only after kube-apiserver exits, so its shutdown can still reach storage
	kineWG     *sync.WaitGroup

	webhookCA *webhookCA // generated at startup; signs webhook serving certificates
	shared    bool       // set on the plane Start hands out; conversion webhooks are refused there

	done     chan struct{} // closed when the server goroutine returns; nil until it starts
	serveErr error         // written before done is closed

	stopOnce sync.Once
	stopErr  error
	stopped  atomic.Bool
}

// New starts a dedicated control plane. ctx bounds startup only; call Stop to
// shut it down.
func New(ctx context.Context) (*ControlPlane, error) {
	logs := processLog()

	cp, err := start(ctx)
	if err != nil {
		return nil, &startError{err: err, logs: logs.String()}
	}

	return cp, nil
}

// startError is a startup failure with the control plane logs leading up to it.
type startError struct {
	err  error
	logs string
}

func (e *startError) Error() string {
	return fmt.Sprintf("petri: start control plane: %v\n--- recent control plane logs ---\n%s", e.err, e.logs)
}

func (e *startError) Unwrap() error {
	return e.err
}

// Config returns a new admin (system:masters) client config for the control
// plane on every call, with high client-side rate limits. Like envtest, it
// leaves the content type empty so clients default to JSON, which every type,
// CRDs included, can encode.
func (cp *ControlPlane) Config() *rest.Config {
	cfg := cp.clientConfig()
	cfg.ContentType = ""
	cfg.AcceptContentTypes = ""

	return cfg
}

// Client returns an admin (system:masters) clientset for the control plane. It
// is built once and shared; clientsets are safe for concurrent use.
func (cp *ControlPlane) Client() *kubernetes.Clientset {
	return cp.client
}

// Stop shuts the control plane down and waits for it. It is safe to call more
// than once and from several goroutines.
func (cp *ControlPlane) Stop() error {
	cp.stopOnce.Do(func() {
		cp.stopped.Store(true)
		cp.stopServer()

		if cp.done != nil {
			<-cp.done
			cp.stopErr = cp.serveErr
		}

		cp.stopKine()
		cp.kineWG.Wait()
	})

	return cp.stopErr
}

// clientConfig copies the loopback config, which negotiates protobuf, with
// high client-side rate limits. Client uses it: built-in types encode as protobuf.
func (cp *ControlPlane) clientConfig() *rest.Config {
	cfg := rest.CopyConfig(cp.config)
	cfg.QPS = clientQPS
	cfg.Burst = clientBurst

	return cfg
}

func (cp *ControlPlane) stoppedAlready() bool {
	return cp.stopped.Load()
}

func start(ctx context.Context) (*ControlPlane, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("before startup: %w", err)
	}

	serverCtx, stopServer := context.WithCancel(context.Background())
	kineCtx, stopKine := context.WithCancel(context.Background())
	cp := &ControlPlane{stopServer: stopServer, stopKine: stopKine, kineWG: &sync.WaitGroup{}}

	if err := cp.boot(ctx, serverCtx, kineCtx); err != nil {
		return nil, withStopError(err, cp.Stop())
	}

	return cp, nil
}

// boot starts kine under kineCtx and kube-apiserver under serverCtx, then
// waits, bounded by ctx, until the API server is ready.
func (cp *ControlPlane) boot(ctx, serverCtx, kineCtx context.Context) error {
	ca, err := newWebhookCA()
	if err != nil {
		return err
	}

	cp.webhookCA = ca

	servers, err := startKine(kineCtx, cp.kineWG)
	if err != nil {
		return err
	}

	opts, key, err := newServerOptions(ctx, servers)
	if err != nil {
		return err
	}

	server, err := buildServer(serverCtx, opts, key)
	if err != nil {
		return errors.Join(err, opts.SecureServing.Listener.Close())
	}

	cp.config = rest.CopyConfig(server.GenericAPIServer.LoopbackClientConfig)

	cp.client, err = kubernetes.NewForConfig(cp.clientConfig())
	if err != nil {
		return errors.Join(fmt.Errorf("build client: %w", err), opts.SecureServing.Listener.Close())
	}

	cp.done = make(chan struct{})

	go cp.serve(serverCtx, server)

	return cp.waitReady(ctx)
}

func (cp *ControlPlane) serve(ctx context.Context, server *aggregatorapiserver.APIAggregator) {
	defer close(cp.done)

	prepared, err := server.PrepareRun()
	if err != nil {
		cp.serveErr = fmt.Errorf("prepare kube-apiserver: %w", err)

		return
	}

	cp.serveErr = prepared.Run(ctx)
}

// waitReady polls /readyz, then waits for the default namespace, failing fast
// if the server goroutine exits.
func (cp *ControlPlane) waitReady(ctx context.Context) error {
	readyCtx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()

	err := wait.PollUntilContextCancel(readyCtx, readyPollInterval, true, func(pollCtx context.Context) (bool, error) {
		select {
		case <-cp.done:
			if cp.serveErr != nil {
				return false, fmt.Errorf("kube-apiserver exited during startup: %w", cp.serveErr)
			}

			return false, errors.New("kube-apiserver exited during startup")
		default:
		}

		var code int

		cp.client.Discovery().RESTClient().Get().AbsPath("/readyz").Do(pollCtx).StatusCode(&code)

		if code != http.StatusOK {
			return false, nil
		}

		_, getErr := cp.client.CoreV1().Namespaces().Get(pollCtx, metav1.NamespaceDefault, metav1.GetOptions{})

		return getErr == nil, nil
	})
	if err != nil {
		return fmt.Errorf("wait for kube-apiserver readiness: %w", err)
	}

	return nil
}

// withStopError adds the error Stop returned after a failed boot, unless err
// already carries it: when kube-apiserver exits during startup, the readiness
// error wraps the same serve error that Stop returns.
func withStopError(err, stopErr error) error {
	if stopErr == nil || errors.Is(err, stopErr) {
		return err
	}

	return errors.Join(err, stopErr)
}
