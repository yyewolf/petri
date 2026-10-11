package petri

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// nameAttempts bounds suffix retries when a generated webhook configuration
// name collides, and re-get retries when a CRD conversion update conflicts.
const nameAttempts = 5

// metadataNameLabel is the label the API server puts on every namespace, which
// webhook namespace selectors can target.
const metadataNameLabel = "kubernetes.io/metadata.name"

// WebhookCABundle returns the PEM-encoded certificate authority the control
// plane generated at startup. The install helpers set it as every webhook's
// caBundle; a webhook whose manifest carries its own URL needs it to have its
// serving certificate trusted.
func (cp *ControlPlane) WebhookCABundle() []byte {
	if cp.webhookCA == nil {
		return nil
	}

	return cp.webhookCA.pem
}

// ServingCertDir writes a serving certificate and key for 127.0.0.1 and
// localhost, signed by the control plane CA, into a directory that dies with
// the test. A controller-runtime webhook server loads the pair directly from
// CertDir; this is petri's only disk use.
func (cp *ControlPlane) ServingCertDir(t testing.TB) string {
	t.Helper()

	dir := t.TempDir()
	if err := cp.WriteServingCert(dir); err != nil {
		t.Fatal(err)
	}

	return dir
}

// WriteServingCert is ServingCertDir for callers that own their directory,
// such as the envtest drop-in.
func (cp *ControlPlane) WriteServingCert(dir string) error {
	if cp.webhookCA == nil {
		return errors.New("petri: control plane has no webhook CA")
	}

	certPEM, keyPEM, err := cp.webhookCA.servingCert()
	if err != nil {
		return err
	}

	if writeErr := os.WriteFile(filepath.Join(dir, "tls.crt"), certPEM, 0o600); writeErr != nil {
		return fmt.Errorf("petri: write tls.crt: %w", writeErr)
	}

	if writeErr := os.WriteFile(filepath.Join(dir, "tls.key"), keyPEM, 0o600); writeErr != nil {
		return fmt.Errorf("petri: write tls.key: %w", writeErr)
	}

	return nil
}

// AdmissionWebhookOptions configures InstallAdmissionWebhooks.
type AdmissionWebhookOptions struct {
	// Host and Port of the local webhook server that service-based webhooks
	// are pointed at.
	Host string
	Port int
	// Namespace limits every webhook to one namespace through a
	// metadata.name label selector, merged into any selector the manifest
	// already had. Leave it empty only for webhooks that must see the whole
	// cluster: on the shared control plane an unscoped webhook fires for
	// every test's objects.
	Namespace string
}

// installedConfig names one webhook configuration the installer created, so
// its cleanup can delete exactly that object.
type installedConfig struct {
	mutating bool
	name     string
}

// InstallAdmissionWebhooks installs Mutating and ValidatingWebhookConfiguration
// manifests from the same kinds of sources InstallCRDs takes, rewires each
// service-based webhook to the local server on Host:Port, keeps the path, and
// sets the control plane CA as the caBundle. Manifests that carry their own URL
// are left untouched.
//
// Each configuration is installed under its manifest name plus a unique
// suffix, so parallel tests can install the same manifests, and the
// configurations are deleted when the test finishes.
func (cp *ControlPlane) InstallAdmissionWebhooks(
	t testing.TB, opts AdmissionWebhookOptions, sources ...any,
) {
	t.Helper()

	cleanup, err := cp.InstallAdmissionWebhooksCtx(t.Context(), opts, sources...)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if removeErr := cleanup(); removeErr != nil && !cp.stoppedAlready() {
			t.Errorf("petri: remove webhook configurations: %v", removeErr)
		}
	})
}

// InstallAdmissionWebhooksCtx is InstallAdmissionWebhooks for callers that
// manage their own lifecycle, such as the envtest drop-in: it returns the
// cleanup that deletes the installed configurations.
func (cp *ControlPlane) InstallAdmissionWebhooksCtx(
	ctx context.Context, opts AdmissionWebhookOptions, sources ...any,
) (func() error, error) {
	parsed, err := parseWebhookConfigurations(sources)
	if err != nil {
		return nil, err
	}
	if parsed.empty() {
		return func() error { return nil }, nil
	}

	client := cp.Client()
	caBundle := cp.WebhookCABundle()
	var created []installedConfig

	for _, config := range parsed.mutating {
		rewireMutatingWebhooks(config, opts, caBundle)
		if createErr := createWithSuffix(&config.ObjectMeta, func() error {
			_, createErr := client.AdmissionregistrationV1().MutatingWebhookConfigurations().
				Create(ctx, config, metav1.CreateOptions{})

			return createErr
		}); createErr != nil {
			// Do not leak the configurations created before the failure: on
			// the shared plane they would outlive the dead local server and
			// break other tests' matching writes.
			return nil, errors.Join(createErr, removeAdmissionConfigs(cp, created))
		}

		created = append(created, installedConfig{mutating: true, name: config.Name})
	}

	for _, config := range parsed.validating {
		rewireValidatingWebhooks(config, opts, caBundle)
		if createErr := createWithSuffix(&config.ObjectMeta, func() error {
			_, createErr := client.AdmissionregistrationV1().ValidatingWebhookConfigurations().
				Create(ctx, config, metav1.CreateOptions{})

			return createErr
		}); createErr != nil {
			return nil, errors.Join(createErr, removeAdmissionConfigs(cp, created))
		}

		created = append(created, installedConfig{mutating: false, name: config.Name})
	}

	return admissionCleanup(cp, created), nil
}

// rewireMutatingWebhooks points a configuration's webhooks at the local server
// and scopes them.
func rewireMutatingWebhooks(
	config *admissionregistrationv1.MutatingWebhookConfiguration, opts AdmissionWebhookOptions, caBundle []byte,
) {
	for i := range config.Webhooks {
		pointWebhookAtLocalServer(&config.Webhooks[i].ClientConfig, opts.Host, opts.Port, caBundle)
		scopeWebhookToNamespace(&config.Webhooks[i].NamespaceSelector, opts.Namespace)
	}
}

// rewireValidatingWebhooks is rewireMutatingWebhooks for validating ones.
func rewireValidatingWebhooks(
	config *admissionregistrationv1.ValidatingWebhookConfiguration, opts AdmissionWebhookOptions, caBundle []byte,
) {
	for i := range config.Webhooks {
		pointWebhookAtLocalServer(&config.Webhooks[i].ClientConfig, opts.Host, opts.Port, caBundle)
		scopeWebhookToNamespace(&config.Webhooks[i].NamespaceSelector, opts.Namespace)
	}
}

// pointWebhookAtLocalServer rewrites a service-based clientConfig to a URL for
// the local server, keeping the path and setting the caBundle.
func pointWebhookAtLocalServer(
	cc *admissionregistrationv1.WebhookClientConfig, host string, port int, caBundle []byte,
) {
	if cc.Service == nil {
		return
	}

	path := "/"
	if cc.Service.Path != nil && *cc.Service.Path != "" {
		path = *cc.Service.Path
	}

	url := "https://" + net.JoinHostPort(host, strconv.Itoa(port)) + path
	cc.URL = &url
	cc.Service = nil
	cc.CABundle = caBundle
}

// scopeWebhookToNamespace merges a metadata.name selector term into the
// webhook's namespace selector, leaving any selector the manifest already had
// in place. An empty namespace means the opt-out: the webhook stays
// cluster-wide.
func scopeWebhookToNamespace(selector **metav1.LabelSelector, namespace string) {
	if namespace == "" {
		return
	}

	if *selector == nil {
		*selector = &metav1.LabelSelector{}
	}

	(*selector).MatchExpressions = append((*selector).MatchExpressions, metav1.LabelSelectorRequirement{
		Key:      metadataNameLabel,
		Operator: metav1.LabelSelectorOpIn,
		Values:   []string{namespace},
	})
}

// createWithSuffix names the object under a unique suffix and creates it,
// redrawing the suffix on a name collision so parallel tests can install the
// same manifests. The name is written back through meta so the caller's create
// closure sees it.
func createWithSuffix(meta *metav1.ObjectMeta, create func() error) error {
	base := meta.Name
	if base == "" {
		base = "petri"
	}

	var lastErr error
	for range nameAttempts {
		meta.Name = base + "-" + randomSuffix()

		err := create()
		if err == nil {
			return nil
		}

		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("petri: create %s: %w", base, err)
		}

		lastErr = err
	}

	return fmt.Errorf("petri: no free name for %s after %d attempts: %w", base, nameAttempts, lastErr)
}

// admissionCleanup deletes the created configurations. CRDs are never deleted,
// but webhook configurations are private to the test that installed them.
func admissionCleanup(cp *ControlPlane, created []installedConfig) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()

		return removeAdmissionConfigsWithCtx(ctx, cp, created)
	}
}

// removeAdmissionConfigs deletes the created configurations with a fresh
// cleanup-time context.
func removeAdmissionConfigs(cp *ControlPlane, created []installedConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	return removeAdmissionConfigsWithCtx(ctx, cp, created)
}

func removeAdmissionConfigsWithCtx(ctx context.Context, cp *ControlPlane, created []installedConfig) error {
	client := cp.Client()
	background := metav1.DeletePropagationBackground

	var errs []error
	for _, config := range created {
		var err error
		if config.mutating {
			err = client.AdmissionregistrationV1().MutatingWebhookConfigurations().
				Delete(ctx, config.name, metav1.DeleteOptions{PropagationPolicy: &background})
		} else {
			err = client.AdmissionregistrationV1().ValidatingWebhookConfigurations().
				Delete(ctx, config.name, metav1.DeleteOptions{PropagationPolicy: &background})
		}

		if err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("petri: delete webhook configuration %s: %w", config.name, err))
		}
	}

	return errors.Join(errs...)
}

// ConversionWebhookOptions configures InstallConversionWebhooks.
type ConversionWebhookOptions struct {
	// Host and Port of the local conversion webhook server.
	Host string
	Port int
}

// InstallConversionWebhooks patches the named CRDs' webhook conversion to
// point at the local server on Host:Port, keeping the path and setting the
// control plane CA as the caBundle. Manifests that carry their own URL are
// left untouched.
//
// A CRD applies to the whole control plane, so conversion webhooks are only
// allowed on a dedicated control plane from New: on the shared one, a CRD
// rewrite would change what every other test sees.
func (cp *ControlPlane) InstallConversionWebhooks(
	t testing.TB, opts ConversionWebhookOptions, crdNames ...string,
) {
	t.Helper()

	if err := cp.InstallConversionWebhooksCtx(t.Context(), opts, crdNames...); err != nil {
		t.Fatal(err)
	}
}

// InstallConversionWebhooksCtx is InstallConversionWebhooks for callers that
// need the error instead of a failed test.
func (cp *ControlPlane) InstallConversionWebhooksCtx(
	ctx context.Context, opts ConversionWebhookOptions, crdNames ...string,
) error {
	if cp.shared {
		return errors.New(
			"petri: conversion webhooks rewrite CRDs for the whole control plane; use petri.New(ctx) for a dedicated one",
		)
	}

	client, err := apiextensionsclient.NewForConfig(cp.Config())
	if err != nil {
		return fmt.Errorf("petri: build apiextensions client: %w", err)
	}

	caBundle := cp.WebhookCABundle()

	for _, name := range crdNames {
		if rewireErr := rewireConversion(ctx, client, name, opts.Host, opts.Port, caBundle); rewireErr != nil {
			return rewireErr
		}
	}

	return nil
}

// rewireConversion points one CRD's service-based conversion webhook at the
// local server, retrying the update on resource conflicts.
func rewireConversion(
	ctx context.Context, client apiextensionsclient.Interface, name, host string, port int, caBundle []byte,
) error {
	for range nameAttempts {
		crd, err := client.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("petri: get crd %s: %w", name, err)
		}

		conversion := crd.Spec.Conversion
		if conversion == nil || conversion.Strategy != apiextensionsv1.WebhookConverter ||
			conversion.Webhook == nil || conversion.Webhook.ClientConfig == nil {
			return fmt.Errorf("petri: crd %s does not declare a webhook conversion", name)
		}

		cc := conversion.Webhook.ClientConfig
		if cc.Service == nil {
			// The manifest already carries its own URL; leave it alone.
			return nil
		}

		path := "/"
		if cc.Service.Path != nil && *cc.Service.Path != "" {
			path = *cc.Service.Path
		}

		url := "https://" + net.JoinHostPort(host, strconv.Itoa(port)) + path
		cc.URL = &url
		cc.Service = nil
		cc.CABundle = caBundle

		_, err = client.ApiextensionsV1().CustomResourceDefinitions().Update(ctx, crd, metav1.UpdateOptions{})
		if err == nil {
			return nil
		}

		if !apierrors.IsConflict(err) {
			return fmt.Errorf("petri: update crd %s: %w", name, err)
		}
	}

	return fmt.Errorf("petri: update crd %s: still conflicting after %d attempts", name, nameAttempts)
}
