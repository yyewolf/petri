package controllers

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/klog/v2"

	sacontroller "github.com/yyewolf/petri/internal/kubernetes/pkg/controller/serviceaccount"
	serviceaccount "github.com/yyewolf/petri/internal/kubernetes/pkg/serviceaccount"
)

const (
	// ServiceAccountName keys the serviceaccount controller in the registry,
	// after upstream's --controllers value.
	ServiceAccountName = "serviceaccount"
	// ServiceAccountTokenName keys the legacy token controller in the
	// registry, after upstream's --controllers alias for the
	// serviceaccount-token controller. In 1.37 its code lives in the
	// serviceaccount package, but the flag value keeps the two controllers
	// apart.
	ServiceAccountTokenName = "serviceaccount-token"
)

// ServiceAccountSpecs returns the registry specs of the two
// service-account controllers petri can enable by name: the ServiceAccount
// controller, which keeps a default ServiceAccount in every namespace, and
// the token controller, which fills the tokens of legacy service-account
// token Secrets.
//
// The ServiceAccount admission plugin stays disabled on petri's API server
// (see apiserver.go). It applies to the whole API server, and enabling it
// would make Pod creation race against the default ServiceAccount's creation
// in new namespaces; the controller itself is opt-in like every built-in, so
// Pods without a ServiceAccount stay valid until a test enables it.
func ServiceAccountSpecs() []Spec {
	return []Spec{
		{Name: ServiceAccountName, Build: buildServiceAccountController},
		{Name: ServiceAccountTokenName, Build: buildServiceAccountTokenController},
	}
}

const (
	// serviceAccountWorkers is what upstream passes to the ServiceAccount
	// controller's Run: one worker, hardcoded there.
	serviceAccountWorkers = 1
	// serviceAccountTokenWorkers matches kube-controller-manager's
	// --concurrent-serviceaccount-token-syncs default.
	serviceAccountTokenWorkers = 5
)

// buildServiceAccountController constructs the upstream ServiceAccount
// controller with the default options, which ensure exactly one ServiceAccount
// named "default" per namespace, like startServiceAccountController does.
func buildServiceAccountController(ctx context.Context, m *Manager) (Run, error) {
	client, err := m.KubeClient(ServiceAccountName)
	if err != nil {
		return nil, err
	}

	factory := m.KubeFactory()
	gate := m.constructorGate()

	controller, err := sacontroller.NewServiceAccountsController(
		klog.FromContext(ctx),
		gatedServiceAccounts(factory.Core().V1().ServiceAccounts(), gate),
		gatedNamespaces(factory.Core().V1().Namespaces(), gate),
		client,
		sacontroller.DefaultServiceAccountsControllerOptions(),
	)
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) { controller.Run(ctx, serviceAccountWorkers) }, nil
}

// buildServiceAccountTokenController constructs the upstream token controller,
// wired like startServiceAccountTokensController: it signs with the legacy
// issuer and the key the --service-account-private-key-file flag would carry.
//
// That key must be petri's API server signing key, shared through
// WithServiceAccountSigningKey: kube-apiserver builds a legacy-token
// authenticator over the public half of exactly that key, so only tokens
// signed with it authenticate on TokenReview. A different key would create
// Secret tokens the API server rejects.
//
// In 1.37 the controller no longer mints a Secret per ServiceAccount (removed
// with bound tokens): it fills the token of Secrets of type
// kubernetes.io/service-account-token that name a ServiceAccount by
// annotation, deletes them with the ServiceAccount, and removes their
// references from the ServiceAccount when they are deleted.
func buildServiceAccountTokenController(ctx context.Context, m *Manager) (Run, error) {
	key := m.serviceAccountSigningKey
	if key == nil {
		return nil, errors.New(
			"petri: no service-account signing key: build the manager with WithServiceAccountSigningKey",
		)
	}

	client, err := m.KubeClient(ServiceAccountTokenName)
	if err != nil {
		return nil, err
	}

	tokenGenerator, err := serviceaccount.JWTTokenGenerator(serviceaccount.LegacyIssuer, key)
	if err != nil {
		return nil, fmt.Errorf("petri: build %s token generator: %w", ServiceAccountTokenName, err)
	}

	factory := m.KubeFactory()
	gate := m.constructorGate()

	controller, err := sacontroller.NewTokensController(
		klog.FromContext(ctx),
		gatedServiceAccounts(factory.Core().V1().ServiceAccounts(), gate),
		gatedSecrets(factory.Core().V1().Secrets(), gate),
		client,
		sacontroller.TokensControllerOptions{
			TokenGenerator: tokenGenerator,
			// Upstream embeds the CA of its root client config so in-cluster
			// clients can verify the API server; petri's controllers talk to
			// the API server through the loopback config, whose CAData is
			// that CA.
			RootCA: m.config.TLSClientConfig.CAData,
		},
	)
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) { controller.Run(ctx, serviceAccountTokenWorkers) }, nil
}
