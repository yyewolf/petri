package example_test

import (
	"context"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/yyewolf/petri"
	"github.com/yyewolf/petri/controllerruntime"
)

// widgetDefaulter is the mutating webhook a kubebuilder scaffold would
// generate for the widget type.
type widgetDefaulter struct{}

func (widgetDefaulter) Default(_ context.Context, widget *Widget) error {
	if widget.Labels == nil {
		widget.Labels = map[string]string{}
	}

	widget.Labels["webhooked"] = "yes"

	return nil
}

// widgetWebhookConfig is the webhook manifest a kubebuilder project ships
// under config/webhook, pointing at a service for the environment to rewrite.
func widgetWebhookConfig() *admissionregistrationv1.MutatingWebhookConfiguration {
	port := int32(443)
	path := "/mutate-example-com-v1-widget"

	return &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "mutate-widgets"},
		Webhooks: []admissionregistrationv1.MutatingWebhook{{
			Name: "mutate-widgets.example.com",
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				Service: &admissionregistrationv1.ServiceReference{
					Namespace: "webhooks", Name: "nowhere", Path: &path, Port: &port,
				},
			},
			// Scoped to the webhook test's namespace: the reconciler test's
			// objects must not hit the webhook server while it is down.
			NamespaceSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "kubernetes.io/metadata.name",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{webhookNamespace},
				}},
			},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
				Rule: admissionregistrationv1.Rule{
					APIGroups:   []string{"example.com"},
					APIVersions: []string{"v1"},
					Resources:   []string{"widgets"},
				},
			}},
			SideEffects:             sideEffectsNone(),
			AdmissionReviewVersions: []string{"v1"},
		}},
	}
}

func sideEffectsNone() *admissionregistrationv1.SideEffectClass {
	none := admissionregistrationv1.SideEffectClassNone

	return &none
}

// TestWidgetWebhookDefaultsLabels runs the suite's webhook server on the
// serving certificate and port the environment filled in, exactly like a
// kubebuilder webhook test does.
func TestWidgetWebhookDefaultsLabels(t *testing.T) {
	t.Parallel()

	opts := testEnv.WebhookInstallOptions
	mgr := controllerruntime.NewManager(t, testConfig,
		controllerruntime.WithScheme(widgetScheme),
		controllerruntime.WithWebhookServer(webhook.NewServer(webhook.Options{
			Host:    opts.LocalServingHost,
			Port:    opts.LocalServingPort,
			CertDir: opts.LocalServingCertDir,
		})),
	)
	if err := builder.WebhookManagedBy(mgr, &Widget{}).
		WithDefaulter(widgetDefaulter{}).
		Complete(); err != nil {
		t.Fatalf("register webhook: %v", err)
	}
	controllerruntime.StartManager(t, mgr)

	if err := k8sClient.Create(t.Context(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: webhookNamespace},
	}); err != nil {
		t.Fatalf("create webhook namespace: %v", err)
	}

	widget := &Widget{
		ObjectMeta: metav1.ObjectMeta{Name: "webhooked", Namespace: webhookNamespace},
	}
	start := time.Now()
	if err := k8sClient.Create(t.Context(), widget); err != nil {
		t.Fatalf("create widget: %v", err)
	}
	t.Logf("widget created after %s", time.Since(start))

	// The webhook labels the widget on its way in; retry past any pickup lag
	// of the configuration.
	got := &Widget{}
	key := client.ObjectKeyFromObject(widget)
	petri.Retry(t, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, key, got); err != nil {
			return false, err
		}

		return got.Labels["webhooked"] == "yes", nil
	})
}
