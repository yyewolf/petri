// Package leakcheck_test checks that a control plane with a Manager and a
// webhook server running releases every goroutine. It is its own test binary
// so no other test's goroutines skew the counts. The counting helpers mirror
// the petri module's internal/leakcheck, whose test-only package cannot be
// imported across modules.
package leakcheck_test

import (
	"context"
	"net"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/yyewolf/petri"
	"github.com/yyewolf/petri/controllerruntime"
)

const (
	slack        = 10
	pollInterval = 250 * time.Millisecond
	// stableFor outlasts kube-apiserver's 3s watch-cache wakeup timers, which
	// exit on their own after Stop.
	stableFor     = 4 * time.Second
	settleTimeout = 30 * time.Second
	startTimeout  = 2 * time.Minute
	maxDumpBytes  = 20000
)

//nolint:tparallel // The subtest's cleanups, manager stop included, must finish before the parent counts.
func TestManagerAndWebhookServerDoNotLeakGoroutines(t *testing.T) {
	t.Parallel()

	before := stableCount(t)

	// The cycle runs in a subtest so its cleanups stop the manager, the
	// webhook server and the control plane before the count below.
	t.Run("cycle", func(t *testing.T) {
		runManagerCycle(t)
	})

	after := waitAtMost(t, before+slack)
	if after > before+slack {
		t.Fatalf("goroutines after run: %d, before: %d\n%s", after, before, goroutineDump())
	}
}

// runManagerCycle runs one dedicated control plane with a Manager whose
// webhook server serves a pod defaulter, exercises the webhook end to end,
// and lets the test's cleanups stop everything.
func runManagerCycle(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()

	env, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Registered first so its cleanup runs last, after the manager stopped.
	t.Cleanup(func() { _ = env.Stop() })

	c, err := client.New(env.Config(), client.Options{Scheme: clientgoscheme.Scheme})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}

	mgr := controllerruntime.NewManager(t, env, controllerruntime.WithWebhookServer(
		webhook.NewServer(webhook.Options{
			Host:    "127.0.0.1",
			Port:    port,
			CertDir: env.ServingCertDir(t),
		}),
	))

	// Registering the webhook is what makes the Manager actually start its
	// webhook server.
	if err := builder.WebhookManagedBy(mgr, &corev1.Pod{}).
		WithDefaulter(podDefaulter{}).
		Complete(); err != nil {
		t.Fatalf("register webhook: %v", err)
	}

	ns, err := env.NewNamespace(ctx)
	if err != nil {
		t.Fatalf("NewNamespace: %v", err)
	}
	env.InstallAdmissionWebhooks(t, petri.AdmissionWebhookOptions{
		Host: "127.0.0.1", Port: port, Namespace: ns,
	}, mutatingConfigForPods())

	controllerruntime.StartManager(t, mgr)

	// A pod created in the scoped namespace comes back defaulted: the whole
	// chain ran through the controller-runtime webhook server.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "webhooked", Namespace: ns}}
	pod.Spec.Containers = []corev1.Container{{Name: "c", Image: "busybox:1.36"}}
	if err := c.Create(t.Context(), pod); err != nil {
		t.Fatalf("create pod: %v", err)
	}

	got := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "webhooked", Namespace: ns}}
	key := client.ObjectKeyFromObject(got)
	petri.Retry(t, func(pollCtx context.Context) (bool, error) {
		if err := c.Get(pollCtx, key, got); err != nil {
			return false, err
		}

		return got.Labels["webhooked"] == "yes", nil
	})
}

// podDefaulter adds a label to every pod.
type podDefaulter struct{}

func (podDefaulter) Default(_ context.Context, pod *corev1.Pod) error {
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}

	pod.Labels["webhooked"] = "yes"

	return nil
}

// mutatingConfigForPods builds a MutatingWebhookConfiguration whose webhook
// points at a service on the path controller-runtime serves the pod defaulter
// at, the shape a kubebuilder manifest ships with.
func mutatingConfigForPods() *admissionregistrationv1.MutatingWebhookConfiguration {
	port := int32(443)
	path := "/mutate--v1-pod" // the empty core group leaves two dashes

	return &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "leak-pods"},
		Webhooks: []admissionregistrationv1.MutatingWebhook{{
			Name: "leak-pods.example.com",
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				Service: &admissionregistrationv1.ServiceReference{
					Namespace: "webhooks", Name: "nowhere", Path: &path, Port: &port,
				},
			},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
				Rule: admissionregistrationv1.Rule{
					APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"},
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

// stableCount waits until the goroutine count has not changed for stableFor
// and returns it.
func stableCount(t *testing.T) int {
	t.Helper()

	deadline := time.Now().Add(settleTimeout)
	last, since := runtime.NumGoroutine(), time.Now()

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		if n := runtime.NumGoroutine(); n != last {
			last, since = n, time.Now()
		} else if time.Since(since) >= stableFor {
			return n
		}
	}

	t.Fatalf("goroutine count never settled; last %d\n%s", last, goroutineDump())

	return last
}

// waitAtMost waits up to settleTimeout for the goroutine count to drop to
// limit and returns the last count seen.
func waitAtMost(t *testing.T, limit int) int {
	t.Helper()

	deadline := time.Now().Add(settleTimeout)

	n := runtime.NumGoroutine()
	for n > limit && time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		n = runtime.NumGoroutine()
	}

	return n
}

func goroutineDump() string {
	var b strings.Builder

	_ = pprof.Lookup("goroutine").WriteTo(&b, 1)

	s := b.String()
	if len(s) > maxDumpBytes {
		s = s[:maxDumpBytes] + "\n... (truncated)"
	}

	return s
}
