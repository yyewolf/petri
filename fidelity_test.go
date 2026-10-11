package petri_test

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"

	"github.com/yyewolf/petri"
)

const (
	httpPort        = 80
	eventTimeout    = 10 * time.Second
	pollInterval    = 100 * time.Millisecond
	defaultHistory  = 10
	defaultDeadline = 600
)

func TestDeploymentGetsUpstreamDefaults(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())
	ns := env.Namespace(t)

	dep := newDeployment("web", corev1.Container{Name: "web", Image: "nginx:1.27"})

	got, err := cs.AppsV1().Deployments(ns).Create(t.Context(), dep, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if got.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Errorf("strategy = %q, want RollingUpdate", got.Spec.Strategy.Type)
	}
	if ru := got.Spec.Strategy.RollingUpdate; ru == nil || ru.MaxSurge.String() != "25%" ||
		ru.MaxUnavailable.String() != "25%" {
		t.Errorf("rollingUpdate = %+v, want maxSurge and maxUnavailable 25%%", ru)
	}
	if got.Spec.RevisionHistoryLimit == nil || *got.Spec.RevisionHistoryLimit != defaultHistory {
		t.Errorf("revisionHistoryLimit = %v, want 10", got.Spec.RevisionHistoryLimit)
	}
	if got.Spec.ProgressDeadlineSeconds == nil || *got.Spec.ProgressDeadlineSeconds != defaultDeadline {
		t.Errorf("progressDeadlineSeconds = %v, want 600", got.Spec.ProgressDeadlineSeconds)
	}

	c := got.Spec.Template.Spec.Containers[0]
	if c.TerminationMessagePath != corev1.TerminationMessagePathDefault ||
		c.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("container defaults = %q/%q, want %q/IfNotPresent",
			c.TerminationMessagePath, c.ImagePullPolicy, corev1.TerminationMessagePathDefault)
	}
}

func TestInvalidPodIsRejectedByUpstreamValidation(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())
	ns := env.Namespace(t)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "bad"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "Bad_Name", Image: "busybox:1.36"}}},
	}

	_, err := cs.CoreV1().Pods(ns).Create(t.Context(), pod, metav1.CreateOptions{})
	if !apierrors.IsInvalid(err) {
		t.Fatalf("create: got %v, want Invalid", err)
	}
	if !strings.Contains(err.Error(), "spec.containers[0].name") {
		t.Errorf("error %q does not name spec.containers[0].name", err)
	}
}

func TestStrategicMergePatchMergesContainersByName(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())
	ns := env.Namespace(t)

	dep := newDeployment("smp",
		corev1.Container{Name: "a", Image: "busybox:1"},
		corev1.Container{Name: "b", Image: "busybox:1"},
	)
	if _, err := cs.AppsV1().Deployments(ns).Create(t.Context(), dep, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create: %v", err)
	}

	patch := []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"b","image":"busybox:2"}]}}}}`)

	got, err := cs.AppsV1().
		Deployments(ns).
		Patch(t.Context(), "smp", types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}

	images := map[string]string{}
	for _, c := range got.Spec.Template.Spec.Containers {
		images[c.Name] = c.Image
	}
	if len(images) != 2 || images["a"] != "busybox:1" || images["b"] != "busybox:2" {
		t.Errorf("containers after patch = %v, want a=busybox:1 b=busybox:2", images)
	}
}

func TestServiceGetsClusterIPFromServiceCIDR(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())
	ns := env.Namespace(t)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "web"},
			Ports:    []corev1.ServicePort{{Port: httpPort}},
		},
	}

	got, err := cs.CoreV1().Services(ns).Create(t.Context(), svc, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ip, err := netip.ParseAddr(got.Spec.ClusterIP)
	if err != nil || !netip.MustParsePrefix("10.0.0.0/16").Contains(ip) {
		t.Errorf("clusterIP = %q, want an address in 10.0.0.0/16", got.Spec.ClusterIP)
	}
}

func TestRBACIsEnforced(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	admin := clientFor(t, env.Config())
	ns := env.Namespace(t)

	aliceCfg := env.Config()
	aliceCfg.Impersonate = rest.ImpersonationConfig{UserName: "alice"}
	alice := clientFor(t, aliceCfg)

	_, err := alice.CoreV1().ConfigMaps(ns).List(t.Context(), metav1.ListOptions{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("list as alice before binding: got %v, want Forbidden", err)
	}

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "cm-reader"},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"list"},
		}},
	}
	if _, err = admin.RbacV1().Roles(ns).Create(t.Context(), role, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create role: %v", err)
	}

	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "alice-cm-reader"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: "alice"}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "cm-reader"},
	}
	if _, err = admin.RbacV1().RoleBindings(ns).Create(t.Context(), binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create rolebinding: %v", err)
	}

	err = wait.PollUntilContextTimeout(
		t.Context(),
		pollInterval,
		eventTimeout,
		true,
		func(ctx context.Context) (bool, error) {
			_, listErr := alice.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{})

			return listErr == nil, nil
		},
	)
	if err != nil {
		t.Fatalf("alice still cannot list configmaps after the binding: %v", err)
	}
}

func TestWatchDeliversEvents(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())
	ns := env.Namespace(t)

	w, err := cs.CoreV1().ConfigMaps(ns).Watch(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer w.Stop()

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "watched"}}
	if _, err = cs.CoreV1().ConfigMaps(ns).Create(t.Context(), cm, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create: %v", err)
	}

	select {
	case ev := <-w.ResultChan():
		got, ok := ev.Object.(*corev1.ConfigMap)
		if ev.Type != watch.Added || !ok || got.Name != "watched" {
			t.Errorf("event = %s %T, want ADDED ConfigMap watched", ev.Type, ev.Object)
		}
	case <-time.After(eventTimeout):
		t.Fatal("no watch event within 10s")
	}
}

//nolint:tparallel // The subtest, cleanups included, must finish before the parent inspects them.
func TestNamespaceIsCreatedAndCleanedUp(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	cs := clientFor(t, env.Config())

	var name string

	t.Run("sub/case", func(t *testing.T) {
		name = env.Namespace(t)
		if !strings.HasPrefix(name, "testnamespaceiscreatedandcleanedup-sub-case-") {
			t.Errorf("namespace name = %q, want the sanitized test name as prefix", name)
		}

		ns, err := cs.CoreV1().Namespaces().Get(t.Context(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get namespace: %v", err)
		}
		if ns.Status.Phase != corev1.NamespaceActive {
			t.Errorf("phase = %q, want Active", ns.Status.Phase)
		}
	})

	ns, err := cs.CoreV1().Namespaces().Get(t.Context(), name, metav1.GetOptions{})

	switch {
	case apierrors.IsNotFound(err):
		// deleted outright
	case err != nil:
		t.Fatalf("get namespace after cleanup: %v", err)
	case ns.DeletionTimestamp == nil:
		t.Errorf("namespace %q was not deleted by cleanup", name)
	}
}
