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
	stopServer context.CancelFunc // stops kube-apiserver
	stopKine   context.CancelFunc // stops kine; only after kube-apiserver exits, so its shutdown can still reach storage
	kineWG     *sync.WaitGroup

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
		return nil, fmt.Errorf("petri: start control plane: %w\n--- recent control plane logs ---\n%s", err, logs)
	}

	return cp, nil
}

// Config returns a new admin (system:masters) client config for the control
// plane on every call, with high client-side rate limits.
func (cp *ControlPlane) Config() *rest.Config {
	cfg := rest.CopyConfig(cp.config)
	cfg.QPS = clientQPS
	cfg.Burst = clientBurst

	return cfg
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

func start(ctx context.Context) (*ControlPlane, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("before startup: %w", err)
	}

	serverCtx, stopServer := context.WithCancel(context.Background())
	kineCtx, stopKine := context.WithCancel(context.Background())
	cp := &ControlPlane{stopServer: stopServer, stopKine: stopKine, kineWG: &sync.WaitGroup{}}

	if err := cp.boot(ctx, serverCtx, kineCtx); err != nil {
		return nil, errors.Join(err, cp.Stop())
	}

	return cp, nil
}

// boot starts kine under kineCtx and kube-apiserver under serverCtx, then
// waits, bounded by ctx, until the API server is ready.
func (cp *ControlPlane) boot(ctx, serverCtx, kineCtx context.Context) error {
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
	client, err := kubernetes.NewForConfig(cp.config)
	if err != nil {
		return fmt.Errorf("readiness client: %w", err)
	}

	readyCtx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()

	err = wait.PollUntilContextCancel(readyCtx, readyPollInterval, true, func(pollCtx context.Context) (bool, error) {
		select {
		case <-cp.done:
			if cp.serveErr != nil {
				return false, fmt.Errorf("kube-apiserver exited during startup: %w", cp.serveErr)
			}

			return false, errors.New("kube-apiserver exited during startup")
		default:
		}

		var code int

		client.Discovery().RESTClient().Get().AbsPath("/readyz").Do(pollCtx).StatusCode(&code)

		if code != http.StatusOK {
			return false, nil
		}

		_, getErr := client.CoreV1().Namespaces().Get(pollCtx, metav1.NamespaceDefault, metav1.GetOptions{})

		return getErr == nil, nil
	})
	if err != nil {
		return fmt.Errorf("wait for kube-apiserver readiness: %w", err)
	}

	return nil
}
