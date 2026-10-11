package envtest_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/yyewolf/petri/controllerruntime/envtest"
)

// podWebhookConfig is a service-based webhook manifest, the shape a
// kubebuilder config/webhook manifest ships with.
func podWebhookConfig() *admissionregistrationv1.MutatingWebhookConfiguration {
	port := int32(443)
	path := "/mutate--v1-pod"

	return &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "envtest-pods"},
		Webhooks: []admissionregistrationv1.MutatingWebhook{{
			Name: "envtest-pods.example.com",
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				Service: &admissionregistrationv1.ServiceReference{
					Namespace: "webhooks", Name: "nowhere", Path: &path, Port: &port,
				},
			},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
				Rule: admissionregistrationv1.Rule{
					APIGroups:   []string{""},
					APIVersions: []string{"v1"},
					Resources:   []string{"pods"},
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

func TestEnvironmentStartMirrorsEnvtest(t *testing.T) {
	t.Parallel()

	env := &envtest.Environment{
		CRDDirectoryPaths: []string{"../../testdata/crds"},
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			MutatingWebhooks: []*admissionregistrationv1.MutatingWebhookConfiguration{podWebhookConfig()},
		},
	}

	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if cfg == nil || cfg.Host == "" {
		t.Fatal("Start returned no config")
	}

	// The webhook options are filled in for the suite's webhook server, the
	// way envtest fills them in.
	opts := env.WebhookInstallOptions
	if opts.LocalServingPort == 0 {
		t.Error("LocalServingPort was not allocated")
	}
	if opts.LocalServingHost != "127.0.0.1" {
		t.Errorf("LocalServingHost = %q, want 127.0.0.1", opts.LocalServingHost)
	}
	for _, name := range []string{"tls.crt", "tls.key"} {
		if _, statErr := os.Stat(filepath.Join(opts.LocalServingCertDir, name)); statErr != nil {
			t.Errorf("serving %s was not written: %v", name, statErr)
		}
	}

	// The CRDs from the directory are established and discoverable.
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	_ = cs
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		t.Fatalf("build discovery client: %v", err)
	}
	groups, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		t.Fatalf("discover groups: %v", err)
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groups)
	if _, mapErr := mapper.RESTMapping(schema.GroupKind{Group: "example.com", Kind: "Widget"}, "v1"); mapErr != nil {
		t.Errorf("widget kind not discoverable after Start: %v", mapErr)
	}

	// The webhook configuration was installed under a unique name.
	list, err := k8sWebhookConfigs(t, cfg)
	if err != nil {
		t.Fatalf("list webhook configurations: %v", err)
	}
	if len(list) != 1 || !strings.HasPrefix(list[0], "envtest-pods-") {
		t.Errorf("installed webhook configurations = %v, want one envtest-pods-<suffix>", list)
	}

	if stopErr := env.Stop(); stopErr != nil {
		t.Fatalf("Stop: %v", stopErr)
	}

	// The webhook configurations are removed with the environment.
	list, err = k8sWebhookConfigs(t, cfg)
	if err != nil && !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("list after Stop: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("webhook configurations after Stop = %v, want none", list)
	}

	// Stop is safe to call again.
	if stopErr := env.Stop(); stopErr != nil {
		t.Errorf("second Stop: %v", stopErr)
	}
}

func k8sWebhookConfigs(t *testing.T, cfg *rest.Config) ([]string, error) {
	t.Helper()

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	list, err := cs.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		names = append(names, item.Name)
	}

	return names, nil
}

func TestEnvironmentObjectCRDs(t *testing.T) {
	t.Parallel()

	env := &envtest.Environment{
		CRDs: []*apiextensionsv1.CustomResourceDefinition{gadgetCRD()},
	}

	if _, err := env.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := env.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// gadgetCRD is a minimal in-memory CRD object source.
func gadgetCRD() *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "gadgets.objects.example.com"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "objects.example.com",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "gadgets", Singular: "gadget", Kind: "Gadget",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: "v1", Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type:                   "object",
						XPreserveUnknownFields: new(true),
					},
				},
			}},
		},
	}
}

func TestEnvironmentRejectsMeaninglessFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*envtest.Environment)
		want  string
	}{
		{
			name:  "UseExistingCluster",
			setup: func(e *envtest.Environment) { e.UseExistingCluster = true },
			want:  "UseExistingCluster",
		},
		{
			name: "BinaryAssetsDirectory",
			setup: func(e *envtest.Environment) {
				e.BinaryAssetsDirectory = t.TempDir()
			},
			want: "BinaryAssetsDirectory",
		},
		{
			name: "CleanUpAfterUse",
			setup: func(e *envtest.Environment) {
				e.CRDInstallOptions.CleanUpAfterUse = true
			},
			want: "CleanUpAfterUse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := &envtest.Environment{}
			tt.setup(env)

			_, err := env.Start()
			if err == nil {
				_ = env.Stop()
				t.Fatalf("Start succeeded, want an error about %s", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %s", err, tt.want)
			}
		})
	}
}

func TestEnvironmentStopWithoutStart(t *testing.T) {
	t.Parallel()

	env := &envtest.Environment{}
	if err := env.Stop(); err == nil {
		t.Fatal("Stop without Start succeeded, want an error")
	}
}
