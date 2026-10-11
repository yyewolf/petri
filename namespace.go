package petri

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	maxNamespaceLen = 63
	suffixLen       = 5
	suffixAlphabet  = "abcdefghijklmnopqrstuvwxyz0123456789"
)

// namespaceName derives a valid namespace name from a test name: lowercase
// alphanumerics, runs of anything else collapsed to one dash, trimmed so the
// name plus "-" plus suffix fits in 63 characters.
func namespaceName(testName, suffix string) string {
	var b strings.Builder

	dash := false

	for _, r := range strings.ToLower(testName) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)

			dash = false

			continue
		}

		if !dash && b.Len() > 0 {
			b.WriteByte('-')

			dash = true
		}
	}

	base := strings.TrimRight(b.String(), "-")
	if maxBase := maxNamespaceLen - len(suffix) - 1; len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}

	if base == "" {
		return suffix
	}

	return base + "-" + suffix
}

func randomSuffix() string {
	b := make([]byte, suffixLen)
	for i := range b {
		b[i] = suffixAlphabet[rand.IntN(len(suffixAlphabet))] //nolint:gosec // Uniqueness, not secrecy.
	}

	return string(b)
}

const (
	namespaceCleanupTimeout = 30 * time.Second
	// namespaceAttempts bounds retries when a random suffix collides with an
	// existing namespace, which stays Terminating once deleted.
	namespaceAttempts = 5
)

// Namespace creates a namespace named after the test plus a random suffix and
// deletes it when the test finishes. Until petri runs the namespace
// controller, a deleted namespace stays Terminating with its contents.
func (cp *ControlPlane) Namespace(t testing.TB) string {
	t.Helper()

	client := cp.Client()

	name, err := createNamespace(t.Context(), client, t.Name(), randomSuffix)
	if err != nil {
		t.Fatalf("petri: create namespace for %s: %v", t.Name(), err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), namespaceCleanupTimeout)
		defer cancel()

		background := metav1.DeletePropagationBackground

		delErr := client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &background})
		if delErr != nil && !apierrors.IsNotFound(delErr) && !cp.stoppedAlready() {
			t.Errorf("petri: delete namespace %q: %v", name, delErr)
		}
	})

	return name
}

// createNamespace creates a namespace named after testName plus a suffix from
// suffix, drawing a new suffix when the name is already taken.
func createNamespace(
	ctx context.Context, client kubernetes.Interface, testName string, suffix func() string,
) (string, error) {
	var err error

	for range namespaceAttempts {
		name := namespaceName(testName, suffix())
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}

		_, err = client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		if err == nil {
			return name, nil
		}

		if !apierrors.IsAlreadyExists(err) {
			return "", fmt.Errorf("create namespace %q: %w", name, err)
		}
	}

	return "", fmt.Errorf("no free namespace name after %d attempts: %w", namespaceAttempts, err)
}
