package petri

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"sync"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	aggregatorapiserver "k8s.io/kube-aggregator/pkg/apiserver"

	"github.com/yyewolf/petri/internal/kubernetes/cmd/kube-apiserver/app"
	"github.com/yyewolf/petri/internal/kubernetes/cmd/kube-apiserver/app/options"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/serviceaccount"
)

const (
	serviceAccountIssuer   = "https://petri.local"
	serviceClusterIPRange  = "10.0.0.0/16"
	endpointReconcilerNone = "none"
)

// setupMu serializes option completion and server construction, which touch
// process-wide registries (component globals, feature gates, metrics).
var setupMu sync.Mutex //nolint:gochecknoglobals // Guards process-wide kube-apiserver state.

// newServerOptions returns kube-apiserver options for an in-memory, loopback
// control plane backed by etcdServers, and the service-account signing key.
func newServerOptions(ctx context.Context, etcdServers []string) (*options.ServerRunOptions, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate service-account key: %w", err)
	}

	publicKeys, err := serviceaccount.StaticPublicKeysGetter([]any{&key.PublicKey})
	if err != nil {
		return nil, nil, fmt.Errorf("service-account public keys: %w", err)
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("listen: %w", err)
	}

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return nil, nil, errors.Join(fmt.Errorf("unexpected listener address %v", ln.Addr()), ln.Close())
	}

	loopback := net.ParseIP("127.0.0.1")

	s := options.NewServerRunOptions()
	s.SecureServing.Listener = ln
	s.SecureServing.BindAddress = loopback
	s.SecureServing.BindPort = addr.Port
	s.SecureServing.ExternalAddress = loopback
	s.SecureServing.ServerCert.CertDirectory = "" // empty dir and pair name: generate the cert in memory
	s.SecureServing.ServerCert.PairName = ""
	s.GenericServerRunOptions.AdvertiseAddress = loopback
	s.Etcd.StorageConfig.Transport.ServerList = etcdServers
	s.ServiceClusterIPRanges = serviceClusterIPRange
	s.EndpointReconcilerType = endpointReconcilerNone
	s.AllowPrivileged = true
	s.Authorization.Modes = []string{"Node", "RBAC"}
	// Without the service-account controller no namespace has a default
	// ServiceAccount, so this plugin would reject every Pod. envtest disables it too.
	s.Admission.GenericAdmission.DisablePlugins = []string{"ServiceAccount"}
	s.Authentication.ServiceAccounts.Issuers = []string{serviceAccountIssuer}
	s.Authentication.ServiceAccounts.ExternalPublicKeysGetter = publicKeys

	return s, key, nil
}

// buildServer completes and validates the options and builds the server chain.
func buildServer(
	ctx context.Context, s *options.ServerRunOptions, key *ecdsa.PrivateKey,
) (*aggregatorapiserver.APIAggregator, error) {
	setupMu.Lock()
	defer setupMu.Unlock()

	if err := s.GenericServerRunOptions.ComponentGlobalsRegistry.Set(); err != nil {
		return nil, fmt.Errorf("set component globals: %w", err)
	}

	completed, err := s.Complete(ctx)
	if err != nil {
		return nil, fmt.Errorf("complete options: %w", err)
	}

	completed.ServiceAccountIssuer, err = serviceaccount.JWTTokenGenerator(serviceAccountIssuer, key)
	if err != nil {
		return nil, fmt.Errorf("service-account token generator: %w", err)
	}

	if errs := completed.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("validate options: %w", utilerrors.NewAggregate(errs))
	}

	config, err := app.NewConfig(completed)
	if err != nil {
		return nil, fmt.Errorf("build config: %w", err)
	}

	completedConfig, err := config.Complete()
	if err != nil {
		return nil, fmt.Errorf("complete config: %w", err)
	}

	server, err := app.CreateServerChain(completedConfig)
	if err != nil {
		return nil, fmt.Errorf("create server chain: %w", err)
	}

	return server, nil
}
