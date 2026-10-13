package webhooks_test

import (
	"context"
	"embed"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

//go:embed testdata/manifests.yaml
var manifests embed.FS

const denied = "ConfigMaps need an owner label"

func TestConfigMapsNeedAnOwner(t *testing.T) {
	t.Parallel()

	cp := petri.Start(t)
	ns := cp.Namespace(t)
	port := serve(t, cp, http.HandlerFunc(requireOwner))

	// Points the manifest's webhook at 127.0.0.1:port with the plane's CA, and
	// scopes it to this test's namespace. Removed when the test ends.
	cp.InstallAdmissionWebhooks(t, petri.AdmissionWebhookOptions{
		Host: "127.0.0.1", Port: port, Namespace: ns,
	}, manifests)

	configMaps := cp.Client().CoreV1().ConfigMaps(ns)

	// The API server picks up a new webhook configuration within moments, so
	// retry until a ConfigMap without an owner is refused by the webhook.
	petri.Retry(t, func(ctx context.Context) (bool, error) {
		_, err := configMaps.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "orphan-"},
		}, metav1.CreateOptions{})

		return err != nil && strings.Contains(err.Error(), denied), nil
	})

	_, err := configMaps.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "owned", Labels: map[string]string{"owner": "team-a"}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("a ConfigMap with an owner was refused: %v", err)
	}
}

// requireOwner is the webhook: it refuses ConfigMaps without an owner label.
func requireOwner(w http.ResponseWriter, r *http.Request) {
	review := &admissionv1.AdmissionReview{}
	if err := json.NewDecoder(r.Body).Decode(review); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	cm := &corev1.ConfigMap{}
	if err := json.Unmarshal(review.Request.Object.Raw, cm); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	response := &admissionv1.AdmissionResponse{UID: review.Request.UID, Allowed: cm.Labels["owner"] != ""}
	if !response.Allowed {
		response.Result = &metav1.Status{Message: denied}
	}

	review.Response = response
	review.Request = nil
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(review)
}

// serve runs handler over TLS on a free port with the plane's serving
// certificate, the pair a controller-runtime webhook server would load from
// its CertDir.
func serve(t *testing.T, cp *petri.ControlPlane, handler http.Handler) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	certDir := cp.ServingCertDir(t)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		_ = server.ServeTLS(ln, filepath.Join(certDir, "tls.crt"), filepath.Join(certDir, "tls.key"))
	}()
	t.Cleanup(func() { _ = server.Close() })

	return ln.Addr().(*net.TCPAddr).Port
}
