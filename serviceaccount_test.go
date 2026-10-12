package petri_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

// TestServiceAccountControllerCreatesDefaultServiceAccounts checks the
// ServiceAccount controller on a plane whose defaults run: a namespace that
// already existed when the controller was enabled gets its default
// ServiceAccount through the informer replay, and a namespace created right
// after EnableControllers returned does not miss the first object either.
func TestServiceAccountControllerCreatesDefaultServiceAccounts(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	// Exists before the enable: only a replay through the namespace informer
	// can create its default ServiceAccount.
	before := newNamespace(ctx, t, cp)

	// EnableControllers returns once the informers are synced, so the
	// namespace created right afterwards is seen by the controller.
	cp.EnableControllers(t, petri.ServiceAccountController)

	cs := clientFor(t, cp.Config())

	petri.WaitFor(t, cs.CoreV1().ServiceAccounts(before), "default", petri.Exists[*corev1.ServiceAccount]())

	after := newNamespace(ctx, t, cp)
	petri.WaitFor(t, cs.CoreV1().ServiceAccounts(after), "default", petri.Exists[*corev1.ServiceAccount]())
}

// TestTokenControllerSignsTokensTheAPIServerVerifies proves the token
// controller shares the API server's signing key: it fills the token of a
// legacy service-account-token Secret, and a TokenReview against the API
// server authenticates that token as the ServiceAccount — which only works
// when the signature verifies against the API server's own public keys.
func TestTokenControllerSignsTokensTheAPIServerVerifies(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cp.EnableControllers(t, petri.ServiceAccountController, petri.ServiceAccountTokenController)

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)
	petri.WaitFor(t, cs.CoreV1().ServiceAccounts(ns), "default", petri.Exists[*corev1.ServiceAccount]())

	// In 1.37 the token controller no longer mints a Secret per ServiceAccount:
	// it fills the token of a Secret of type service-account-token that names
	// one by annotation, the shape of a manually created token Secret.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "default-token",
			Annotations: map[string]string{corev1.ServiceAccountNameKey: "default"},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}
	if _, err = cs.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create token secret: %v", err)
	}

	filled := petri.WaitFor(t, cs.CoreV1().Secrets(ns), "default-token", func(s *corev1.Secret) (bool, error) {
		return s != nil && len(s.Data[corev1.ServiceAccountTokenKey]) > 0, nil
	})

	// The namespace and CA data accompany the token, as upstream's root-CA
	// wiring fills them alongside it.
	if len(filled.Data[corev1.ServiceAccountRootCAKey]) == 0 {
		t.Error("token secret has no ca.crt data; the controller should embed its client config's CA")
	}
	if got := string(filled.Data[corev1.ServiceAccountNamespaceKey]); got != ns {
		t.Errorf("token secret namespace data = %q, want %q", got, ns)
	}

	// The key proof: the API server authenticates the token. Its legacy
	// service-account authenticator verifies the JWT signature against the
	// same in-memory key the API server was configured with, so this only
	// passes when the token controller signed with that key.
	review, err := cs.AuthenticationV1().TokenReviews().Create(ctx, &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: string(filled.Data[corev1.ServiceAccountTokenKey])},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("tokenreview: %v", err)
	}

	if !review.Status.Authenticated {
		t.Fatalf("token was not authenticated: %v", review.Status.Error)
	}

	want := fmt.Sprintf("system:serviceaccount:%s:default", ns)
	if review.Status.User.Username != want {
		t.Errorf("token authenticates as %q, want %q", review.Status.User.Username, want)
	}
}
