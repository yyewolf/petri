package petri

import (
	"regexp"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func TestNamespaceName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, test, want string
	}{
		{name: "plain", test: "TestFoo", want: "testfoo-abcde"},
		{name: "subtest", test: "TestFoo/sub_case#01", want: "testfoo-sub-case-01-abcde"},
		{name: "unicode", test: "TestÜber", want: "test-ber-abcde"},
		{name: "only symbols", test: "///", want: "abcde"},
		{name: "trailing symbol", test: "Test!", want: "test-abcde"},
		{name: "long", test: strings.Repeat("a", 100), want: strings.Repeat("a", 57) + "-abcde"},
		{name: "cut lands on dash", test: strings.Repeat("a", 56) + "/bbbb", want: strings.Repeat("a", 56) + "-abcde"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := namespaceName(tc.test, "abcde")
			if got != tc.want {
				t.Errorf("namespaceName(%q) = %q, want %q", tc.test, got, tc.want)
			}
			if len(got) > maxNamespaceLen || !dnsLabel.MatchString(got) {
				t.Errorf("namespaceName(%q) = %q is not a valid namespace name", tc.test, got)
			}
		})
	}
}

func TestRandomSuffix(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for range 50 {
		s := randomSuffix()
		if len(s) != suffixLen || !dnsLabel.MatchString(s) {
			t.Fatalf("randomSuffix() = %q, want %d chars of [a-z0-9]", s, suffixLen)
		}
		seen[s] = true
	}

	if len(seen) < 45 {
		t.Errorf("only %d distinct suffixes in 50 draws", len(seen))
	}
}

func TestCreateNamespaceRetriesOnNameCollision(t *testing.T) {
	t.Parallel()

	taken := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "testfoo-aaaaa"}}
	client := fake.NewClientset(taken)

	suffixes := []string{"aaaaa", "bbbbb"}
	next := func() string {
		s := suffixes[0]
		suffixes = suffixes[1:]

		return s
	}

	name, err := createNamespace(t.Context(), client, "TestFoo", next)
	if err != nil {
		t.Fatalf("createNamespace: %v", err)
	}
	if name != "testfoo-bbbbb" {
		t.Errorf("name = %q, want testfoo-bbbbb after the first suffix collided", name)
	}
}

func TestCreateNamespaceGivesUpAfterRepeatedCollisions(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "testfoo-aaaaa"}})

	_, err := createNamespace(t.Context(), client, "TestFoo", func() string { return "aaaaa" })
	if !apierrors.IsAlreadyExists(err) {
		t.Fatalf("err = %v, want AlreadyExists once retries run out", err)
	}
}
