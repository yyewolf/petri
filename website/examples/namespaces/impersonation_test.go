package namespaces_test

import (
	"context"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/yyewolf/petri"
)

func TestAliceNeedsARoleBinding(t *testing.T) {
	t.Parallel()

	cp := petri.Start(t)
	ns := cp.Namespace(t)

	// Config returns a fresh admin config on every call, so changing it is safe.
	cfg := cp.Config()
	cfg.Impersonate = rest.ImpersonationConfig{UserName: "alice"}
	alice, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}

	_, err = alice.CoreV1().ConfigMaps(ns).List(t.Context(), metav1.ListOptions{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("alice listed ConfigMaps without a binding: %v", err)
	}

	admin := cp.Client().RbacV1()
	_, err = admin.Roles(ns).Create(t.Context(), &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "read-config"},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get", "list"},
		}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.RoleBindings(ns).Create(t.Context(), &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "alice-reads-config"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "read-config"},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: "alice"}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// The RBAC authorizer learns about the binding through a watch, as on a
	// cluster, so the first calls after it may still be refused.
	petri.Retry(t, func(ctx context.Context) (bool, error) {
		_, listErr := alice.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{})

		return listErr == nil, nil
	})
}
