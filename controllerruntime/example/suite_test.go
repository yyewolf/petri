// Package example_test is a kubebuilder-style test suite, ported to petri by
// changing only the import of sigs.k8s.io/controller-runtime/pkg/envtest to
// github.com/yyewolf/petri/controllerruntime/envtest. It shows a reconciler
// and a webhook running against a real control plane.
package example_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/yyewolf/petri/controllerruntime/envtest"
)

// webhookNamespace is the only namespace the example's webhook is scoped to,
// so the reconciler test's objects never reach the webhook server while it is
// down between the suite starting and the webhook test running.
const webhookNamespace = "example-webhooks"

var (
	testEnv    *envtest.Environment //nolint:gochecknoglobals // Suite state, as every kubebuilder suite_test.go carries.
	testConfig *rest.Config         //nolint:gochecknoglobals // Suite state.
	k8sClient  client.Client        //nolint:gochecknoglobals // Suite state.
)

// The example's API types, which a kubebuilder project would generate under
// api/v1 together with deepcopy code.
var (
	widgetScheme        = runtime.NewScheme()                           //nolint:gochecknoglobals // Scheme wiring.
	widgetSchemeBuilder = runtime.NewSchemeBuilder(registerWidgetTypes) //nolint:gochecknoglobals // Scheme wiring.
)

func registerWidgetTypes(s *runtime.Scheme) error {
	// The group-version a kubebuilder project generates under api/v1.
	gv := schema.GroupVersion{Group: "example.com", Version: "v1"}

	s.AddKnownTypes(gv, &Widget{}, &WidgetList{})
	metav1.AddToGroupVersion(s, gv)

	return nil
}

func init() { //nolint:gochecknoinits // Scheme registration, as generated kubebuilder code does.
	utilruntime.Must(clientgoscheme.AddToScheme(widgetScheme))
	utilruntime.Must(widgetSchemeBuilder.AddToScheme(widgetScheme))
}

// TestMain is the suite setup a kubebuilder scaffold carries: start the
// environment once, run the tests, stop the environment.
func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "testdata", "crds")},
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			MutatingWebhooks: []*admissionregistrationv1.MutatingWebhookConfiguration{widgetWebhookConfig()},
		},
	}

	cfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start test environment: %v\n", err)
		os.Exit(1)
	}
	testConfig = cfg

	k8sClient, err = client.New(testConfig, client.Options{Scheme: widgetScheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "build client: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if stopErr := testEnv.Stop(); stopErr != nil {
		fmt.Fprintf(os.Stderr, "stop test environment: %v\n", stopErr)
	}

	os.Exit(code)
}
