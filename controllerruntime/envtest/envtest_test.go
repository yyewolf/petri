package envtest_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/yyewolf/petri"
	"github.com/yyewolf/petri/controllerruntime/envtest"
)

// envtestSemanticsWindow is how long the opt-out test waits to confirm that
// nothing happens. The namespace controller delays its first pass by a 5s
// grace period, so a window longer than that proves the controllers are not
// running, not merely slow.
const envtestSemanticsWindow = 6 * time.Second

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

// TestDeletedNamespaceFullyDisappears pins the drop-in's default: petri runs
// the namespace controller, so a deleted namespace is not merely Terminating
// but is removed once its contents are gone, where envtest would leave it.
func TestDeletedNamespaceFullyDisappears(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	ns := createNamespaceForTest(ctx, t, cs, "dropin-defaults")

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "in-ns", Namespace: ns}}
	if _, createErr := cs.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{}); createErr != nil {
		t.Fatalf("create configmap: %v", createErr)
	}

	if delErr := cs.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{}); delErr != nil {
		t.Fatalf("delete namespace: %v", delErr)
	}

	petri.WaitFor(t, cs.CoreV1().Namespaces(), ns, petri.Deleted[*corev1.Namespace]())
	petri.WaitFor(t, cs.CoreV1().ConfigMaps(ns), "in-ns", petri.Deleted[*corev1.ConfigMap]())
}

// TestDisableDefaultControllersMatchesEnvtest starts the drop-in with the
// default controllers off and proves envtest parity: a deleted namespace stays
// Terminating with its contents, and owner references do not cascade.
func TestDisableDefaultControllersMatchesEnvtest(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	env := &envtest.Environment{DisableDefaultControllers: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	ns := createNamespaceForTest(ctx, t, cs, "dropin-optout")

	owner := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: ns}}
	if owner, err = cs.CoreV1().ConfigMaps(ns).Create(ctx, owner, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create owner: %v", err)
	}

	dependent := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dependent",
			Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "ConfigMap", Name: owner.Name, UID: owner.UID,
			}},
		},
	}
	if _, createErr := cs.CoreV1().ConfigMaps(ns).Create(ctx, dependent, metav1.CreateOptions{}); createErr != nil {
		t.Fatalf("create dependent: %v", createErr)
	}

	// A deleted namespace is marked Terminating by the API server itself, but
	// with no namespace controller it is never finalized and its contents
	// stay.
	if delErr := cs.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{}); delErr != nil {
		t.Fatalf("delete namespace: %v", delErr)
	}

	petri.WaitFor(t, cs.CoreV1().Namespaces(), ns, namespaceTerminating)

	// Owner references do not cascade: the owner is really deleted, the
	// dependent is left behind.
	if delErr := cs.CoreV1().ConfigMaps(ns).Delete(ctx, owner.Name, metav1.DeleteOptions{}); delErr != nil {
		t.Fatalf("delete owner: %v", delErr)
	}

	time.Sleep(envtestSemanticsWindow)

	terminating, err := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace vanished with the controllers off: %v", err)
	}
	if terminating.Status.Phase != corev1.NamespaceTerminating {
		t.Errorf("namespace phase = %q, want Terminating", terminating.Status.Phase)
	}

	if _, getErr := cs.CoreV1().ConfigMaps(ns).Get(ctx, "dependent", metav1.GetOptions{}); getErr != nil {
		t.Fatalf("dependent was garbage collected with the controllers off: %v", getErr)
	}
}

// TestEnableControllersRunsWorkloadControllers proves the drop-in's enabling
// path reaches petri's controllers: after enabling the Deployment controller, a
// Deployment gets its ReplicaSet. The controller behaviours themselves are
// tested in the core module, so the test stops at the first reaction.
func TestEnableControllersRunsWorkloadControllers(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	env.EnableControllers(t, petri.DeploymentController)

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	ns := createNamespaceForTest(ctx, t, cs, "dropin-enable")

	labels := map[string]string{"app": "web"}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "web", Image: "nginx:1.0"}},
				},
			},
		},
	}
	if dep, err = cs.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// The controller names the ReplicaSet itself, so the wait matches on the
	// owner UID instead of a name.
	petri.Retry(t, func(context.Context) (bool, error) {
		list, listErr := cs.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{})
		if listErr != nil {
			return false, listErr
		}

		for i := range list.Items {
			if ref := metav1.GetControllerOf(&list.Items[i]); ref != nil && ref.UID == dep.UID {
				return true, nil
			}
		}

		return false, nil
	})
}

// fatalTB is a [testing.TB] stand-in that records Fatal calls instead of ending
// the test, so EnableControllers's failure path is observable. Only Helper and
// Fatal are called on the paths under test; every other method is unused and
// panics through the nil embedded interface.
type fatalTB struct {
	testing.TB

	fatals []string
}

func (f *fatalTB) Helper() {}

func (f *fatalTB) Fatal(args ...any) {
	f.fatals = append(f.fatals, fmt.Sprint(args...))
}

// TestEnableControllersRequiresARunningEnvironment checks that enabling before
// Start or after Stop fails the suite's test instead of dereferencing a control
// plane that is not there.
func TestEnableControllersRequiresARunningEnvironment(t *testing.T) {
	t.Parallel()

	env := &envtest.Environment{}

	tb := &fatalTB{}
	env.EnableControllers(tb, petri.DeploymentController)
	if len(tb.fatals) == 0 || !strings.Contains(tb.fatals[0], "before Start") {
		t.Errorf("EnableControllers before Start did not fail the test: %v", tb.fatals)
	}

	if _, err := env.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	if err := env.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	tb = &fatalTB{}
	env.EnableControllers(tb, petri.DeploymentController)
	if len(tb.fatals) == 0 || !strings.Contains(tb.fatals[0], "after Stop") {
		t.Errorf("EnableControllers after Stop did not fail the test: %v", tb.fatals)
	}
}

// namespaceTerminating holds once the namespace is marked Terminating, which
// the API server does itself on deletion.
func namespaceTerminating(ns *corev1.Namespace) (bool, error) {
	if ns == nil {
		return false, nil
	}

	return ns.Status.Phase == corev1.NamespaceTerminating, nil
}

// createNamespaceForTest creates a namespace with the given name. Every Start
// runs a dedicated control plane, so the name cannot collide with another
// test's.
func createNamespaceForTest(ctx context.Context, t *testing.T, cs *kubernetes.Clientset, name string) string {
	t.Helper()

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if _, err := cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace %s: %v", name, err)
	}

	return name
}
