package petri_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/yyewolf/petri"
)

// mutatingConfig builds a MutatingWebhookConfiguration whose webhooks point at
// a service, the shape manifests normally ship with.
func mutatingConfig(
	name, webhookName string,
	rules []admissionregistrationv1.RuleWithOperations,
) *admissionregistrationv1.MutatingWebhookConfiguration {
	return &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Webhooks: []admissionregistrationv1.MutatingWebhook{{
			Name:                    webhookName,
			ClientConfig:            serviceClientConfig(),
			Rules:                   rules,
			SideEffects:             sideEffectsNone(),
			AdmissionReviewVersions: []string{"v1"},
		}},
	}
}

func validatingConfig(
	name, webhookName string,
	rules []admissionregistrationv1.RuleWithOperations,
) *admissionregistrationv1.ValidatingWebhookConfiguration {
	return &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{{
			Name:                    webhookName,
			ClientConfig:            serviceClientConfig(),
			Rules:                   rules,
			SideEffects:             sideEffectsNone(),
			AdmissionReviewVersions: []string{"v1"},
		}},
	}
}

func serviceClientConfig() admissionregistrationv1.WebhookClientConfig {
	port := int32(443)
	path := "/mutate"

	return admissionregistrationv1.WebhookClientConfig{
		Service: &admissionregistrationv1.ServiceReference{
			Namespace: "webhooks", Name: "nowhere", Path: &path, Port: &port,
		},
	}
}

func sideEffectsNone() *admissionregistrationv1.SideEffectClass {
	none := admissionregistrationv1.SideEffectClassNone

	return &none
}

// podRules matches pod creation.
func podRules() []admissionregistrationv1.RuleWithOperations {
	return []admissionregistrationv1.RuleWithOperations{{
		Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
		Rule: admissionregistrationv1.Rule{
			APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"},
		},
	}}
}

// neverRules match a kind nothing in this suite ever creates, so a
// configuration with a dead endpoint is never consulted.
func neverRules() []admissionregistrationv1.RuleWithOperations {
	return []admissionregistrationv1.RuleWithOperations{{
		Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
		Rule: admissionregistrationv1.Rule{
			APIGroups: []string{"never.example.com"}, APIVersions: []string{"v1"}, Resources: []string{"neverthings"},
		},
	}}
}

// serveWebhooks starts a TLS server on 127.0.0.1 with a free port, using the
// control plane's serving certificate, and returns its port.
func serveWebhooks(t *testing.T, env *petri.ControlPlane, handler http.Handler) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	certDir := env.ServingCertDir(t)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Load the pair the way a controller-runtime webhook server would:
		// straight from the directory, no extra wiring.
		_ = server.ServeTLS(ln, filepath.Join(certDir, "tls.crt"), filepath.Join(certDir, "tls.key"))
	}()
	t.Cleanup(func() {
		_ = server.Close()
		<-done
	})

	return ln.Addr().(*net.TCPAddr).Port
}

// mutateHandler adds a label to every admitted pod.
func mutateHandler(label, value string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		review := &admissionv1.AdmissionReview{}
		if err := json.NewDecoder(r.Body).Decode(review); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		patch := fmt.Sprintf(
			`[{"op":"add","path":"/metadata/labels","value":{"%s":"%s"}}]`, label, value,
		)
		patchType := admissionv1.PatchTypeJSONPatch
		response := &admissionv1.AdmissionReview{
			TypeMeta: metav1.TypeMeta{APIVersion: admissionv1.SchemeGroupVersion.String(), Kind: "AdmissionReview"},
			Response: &admissionv1.AdmissionResponse{
				UID:       review.Request.UID,
				Allowed:   true,
				Patch:     []byte(patch), // encoded as base64 by encoding/json
				PatchType: &patchType,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
}

func newPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "busybox:1.36"}}},
	}
}

func TestServingCertIsUsableAndChainsToTheCABundle(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	certDir := env.ServingCertDir(t)

	cert, err := tls.LoadX509KeyPair(filepath.Join(certDir, "tls.crt"), filepath.Join(certDir, "tls.key"))
	if err != nil {
		t.Fatalf("load serving pair like a webhook server would: %v", err)
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}

	sawLocalhost, sawLoopback := false, false
	for _, name := range leaf.DNSNames {
		if name == "localhost" {
			sawLocalhost = true
		}
	}
	for _, ip := range leaf.IPAddresses {
		if ip.String() == "127.0.0.1" {
			sawLoopback = true
		}
	}
	if !sawLocalhost || !sawLoopback {
		t.Errorf("serving cert SANs = %v / %v, want localhost and 127.0.0.1", leaf.DNSNames, leaf.IPAddresses)
	}

	roots := x509.NewCertPool()
	if ok := roots.AppendCertsFromPEM(env.WebhookCABundle()); !ok {
		t.Fatal("the CA bundle is not parseable PEM")
	}
	if _, verifyErr := leaf.Verify(x509.VerifyOptions{Roots: roots}); verifyErr != nil {
		t.Errorf("serving cert does not chain to the CA bundle: %v", verifyErr)
	}
}

func TestAdmissionWebhookMutatesOnlyItsNamespace(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())
	ns := env.Namespace(t)
	other, err := env.NewNamespace(t.Context())
	if err != nil {
		t.Fatalf("NewNamespace: %v", err)
	}

	port := serveWebhooks(t, env, mutateHandler("webhook-mutated", "yes"))

	config := mutatingConfig("mutate-pods", "mutate-pods.example.com", podRules())
	env.InstallAdmissionWebhooks(t, petri.AdmissionWebhookOptions{
		Host: "127.0.0.1", Port: port, Namespace: ns,
	}, config)

	// A pod in the scoped namespace is mutated; creation may race the first
	// pickup of the configuration, so retry with fresh pods. Create failures
	// during that window are expected and retried, not fatal.
	podSeq := 0
	createPod := func(ctx context.Context, name string) bool {
		_, createErr := cs.CoreV1().Pods(ns).Create(ctx, newPod(name), metav1.CreateOptions{})

		return createErr == nil
	}
	petri.Retry(t, func(ctx context.Context) (bool, error) {
		podSeq++
		name := fmt.Sprintf("mutated-%d", podSeq)
		if !createPod(ctx, name) {
			return false, nil
		}

		got, getErr := cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return false, getErr
		}

		return got.Labels["webhook-mutated"] == "yes", nil
	})

	// A pod in another namespace passes untouched: the webhook is scoped.
	pod := newPod("unmutated")
	created, err := cs.CoreV1().Pods(other).Create(t.Context(), pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create pod outside the scoped namespace: %v", err)
	}
	if created.Labels["webhook-mutated"] == "yes" {
		t.Error("pod outside the scoped namespace was mutated")
	}
}

//nolint:tparallel // The subtest's cleanups must have run before the parent lists configurations.
func TestAdmissionWebhooksAreRemovedOnCleanup(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())

	t.Run("install and cleanup", func(t *testing.T) {
		port := serveWebhooks(t, env, http.NewServeMux())
		env.InstallAdmissionWebhooks(t, petri.AdmissionWebhookOptions{Host: "127.0.0.1", Port: port},
			mutatingConfig("cleanup-mutating", "cleanup-mutating.example.com", neverRules()),
			validatingConfig("cleanup-validating", "cleanup-validating.example.com", neverRules()),
		)

		for _, kind := range []string{"mutating", "validating"} {
			list, err := webhookConfigs(t, cs, kind)
			if err != nil {
				t.Fatalf("list %s configurations: %v", kind, err)
			}
			if !hasNamePrefix(list, "cleanup-"+kind+"-") {
				t.Fatalf("%s configuration was not installed", kind)
			}
		}
	})

	for _, kind := range []string{"mutating", "validating"} {
		list, err := webhookConfigs(t, cs, kind)
		if err != nil {
			t.Fatalf("list %s configurations after cleanup: %v", kind, err)
		}
		if hasNamePrefix(list, "cleanup-"+kind+"-") {
			t.Errorf("%s configuration survived cleanup", kind)
		}
	}
}

// TestAdmissionWebhooksPartialFailureCleansUp pins the behavior when one of
// several configurations fails to create: the ones already created must not
// leak, since a leaked unscoped configuration outlives the dead local server
// and breaks other tests' matching writes.
func TestAdmissionWebhooksPartialFailureCleansUp(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())

	// The second configuration's name is not a DNS subdomain, so the API
	// server rejects it after the first one was created.
	broken := mutatingConfig("Bad-Uppercase-Name", "broken.example.com", neverRules())

	cleanup, err := env.InstallAdmissionWebhooksCtx(t.Context(),
		petri.AdmissionWebhookOptions{Host: "127.0.0.1", Port: 1},
		mutatingConfig("partial-failure", "partial-failure.example.com", neverRules()),
		broken,
	)
	if err == nil {
		_ = cleanup()
		t.Fatal("installing a configuration with an invalid name succeeded, want an error")
	}

	list, lerr := webhookConfigs(t, cs, "mutating")
	if lerr != nil {
		t.Fatalf("list configurations: %v", lerr)
	}
	if hasNamePrefix(list, "partial-failure-") {
		t.Errorf("configurations created before the failure leaked: %v", list)
	}
}

func TestAdmissionWebhooksParallelInstallSameManifest(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)

	const installers = 3
	names := make([][]string, installers)
	errs := make([]error, installers)

	var wg sync.WaitGroup
	for i := range installers {
		wg.Go(func() {
			port := serveWebhooks(t, env, http.NewServeMux())
			_, errs[i] = env.InstallAdmissionWebhooksCtx(t.Context(),
				petri.AdmissionWebhookOptions{Host: "127.0.0.1", Port: port},
				mutatingConfig("parallel-webhooks", "parallel-webhooks.example.com", neverRules()),
			)
			if errs[i] != nil {
				return
			}

			names[i] = installedMutatingNames(t, env, "parallel-webhooks-")
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("installer #%d: %v", i, err)
		}
	}

	seen := map[string]bool{}
	for _, list := range names {
		for _, name := range list {
			seen[name] = true
		}
	}
	if len(seen) < installers {
		t.Errorf("parallel installs produced names %v, want %d distinct configurations", seen, installers)
	}
}

func TestConversionWebhookConvertsOnDedicatedPlane(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	env, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	// A CRD declaring service-based webhook conversion, the shape manifests
	// ship with. Both versions share one schema, so the converter only has to
	// re-stamp apiVersion.
	crd := conversionCRD("converts.example.com", "convertibles")
	env.InstallCRDs(t, crd)

	port := serveWebhooks(t, env, convertHandler())
	env.InstallConversionWebhooks(t, petri.ConversionWebhookOptions{Host: "127.0.0.1", Port: port},
		"convertibles.converts.example.com")

	dyn, err := dynamic.NewForConfig(env.Config())
	if err != nil {
		t.Fatalf("build dynamic client: %v", err)
	}
	ns := env.Namespace(t)

	v1 := schema.GroupVersionResource{Group: "converts.example.com", Version: "v1", Resource: "convertibles"}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "converts.example.com/v1",
		"kind":       "Convertible",
		"metadata":   map[string]any{"name": "c", "namespace": ns},
		"spec":       map[string]any{"size": "large"},
	}}
	if _, createErr := dyn.Resource(v1).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{}); createErr != nil {
		t.Fatalf("create v1 object: %v", createErr)
	}

	v2 := schema.GroupVersionResource{Group: "converts.example.com", Version: "v2", Resource: "convertibles"}
	got, err := dyn.Resource(v2).Namespace(ns).Get(ctx, "c", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get as v2: %v", err)
	}
	if got.GetAPIVersion() != "converts.example.com/v2" {
		t.Errorf("object served as %q, want the v2 conversion webhook to have run", got.GetAPIVersion())
	}
}

func TestConversionWebhooksRefusedOnSharedPlane(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)

	err := env.InstallConversionWebhooksCtx(
		t.Context(), petri.ConversionWebhookOptions{Host: "127.0.0.1", Port: 1}, "whatever.example.com",
	)
	if err == nil {
		t.Fatal("conversion webhooks on the shared control plane succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "petri.New") {
		t.Errorf("refusal %q does not point at petri.New", err)
	}
}

// convertHandler answers ConversionReviews by re-stamping apiVersion.
func convertHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		review := &apiextensionsv1.ConversionReview{}
		if err := json.NewDecoder(r.Body).Decode(review); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		converted := make([]runtime.RawExtension, 0, len(review.Request.Objects))
		for _, raw := range review.Request.Objects {
			obj := map[string]any{}
			if err := json.Unmarshal(raw.Raw, &obj); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)

				return
			}
			obj["apiVersion"] = review.Request.DesiredAPIVersion
			encoded, err := json.Marshal(obj)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)

				return
			}
			converted = append(converted, runtime.RawExtension{Raw: encoded})
		}

		response := &apiextensionsv1.ConversionReview{
			TypeMeta: metav1.TypeMeta{APIVersion: "apiextensions.k8s.io/v1", Kind: "ConversionReview"},
			Response: &apiextensionsv1.ConversionResponse{
				UID:              review.Request.UID,
				Result:           metav1.Status{Status: metav1.StatusSuccess},
				ConvertedObjects: converted,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
}

// webhookConfigs lists Mutating or ValidatingWebhookConfigurations ("mutating"
// or "validating") and returns their names.
func webhookConfigs(
	t *testing.T, cs *kubernetes.Clientset, kind string,
) ([]string, error) {
	t.Helper()

	switch kind {
	case "mutating":
		list, err := cs.AdmissionregistrationV1().MutatingWebhookConfigurations().
			List(t.Context(), metav1.ListOptions{})
		if err != nil {
			return nil, err
		}

		names := make([]string, 0, len(list.Items))
		for _, item := range list.Items {
			names = append(names, item.Name)
		}

		return names, nil
	case "validating":
		list, err := cs.AdmissionregistrationV1().ValidatingWebhookConfigurations().
			List(t.Context(), metav1.ListOptions{})
		if err != nil {
			return nil, err
		}

		names := make([]string, 0, len(list.Items))
		for _, item := range list.Items {
			names = append(names, item.Name)
		}

		return names, nil
	default:
		return nil, fmt.Errorf("unknown webhook configuration kind %q", kind)
	}
}

func hasNamePrefix(names []string, prefix string) bool {
	for _, name := range names {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	return false
}

// installedMutatingNames lists mutating configurations whose name starts with
// prefix, without failing the test.
func installedMutatingNames(t *testing.T, env *petri.ControlPlane, prefix string) []string {
	t.Helper()

	cs, err := kubernetes.NewForConfig(env.Config())
	if err != nil {
		t.Errorf("build client: %v", err)

		return nil
	}

	list, err := cs.AdmissionregistrationV1().MutatingWebhookConfigurations().
		List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Errorf("list mutating configurations: %v", err)

		return nil
	}

	var names []string
	for _, item := range list.Items {
		if strings.HasPrefix(item.Name, prefix) {
			names = append(names, item.Name)
		}
	}

	return names
}

// conversionCRD builds a two-version CRD with service-based webhook conversion.
func conversionCRD(group, plural string) *apiextensionsv1.CustomResourceDefinition {
	port := int32(443)
	path := "/convert"

	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: plural + "." + group},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: plural, Singular: plural[:len(plural)-1], Kind: "Convertible", ListKind: "ConvertibleList",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Conversion: &apiextensionsv1.CustomResourceConversion{
				Strategy: apiextensionsv1.WebhookConverter,
				Webhook: &apiextensionsv1.WebhookConversion{
					ClientConfig: &apiextensionsv1.WebhookClientConfig{
						Service: &apiextensionsv1.ServiceReference{
							Namespace: "webhooks", Name: "nowhere", Path: &path, Port: &port,
						},
					},
					ConversionReviewVersions: []string{"v1"},
				},
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name: "v1", Served: true, Storage: true,
					Schema: preservedSchema(),
				},
				{
					Name: "v2", Served: true,
					Schema: preservedSchema(),
				},
			},
		},
	}
}

func preservedSchema() *apiextensionsv1.CustomResourceValidation {
	return &apiextensionsv1.CustomResourceValidation{
		OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
			Type:                   "object",
			XPreserveUnknownFields: new(true),
		},
	}
}
