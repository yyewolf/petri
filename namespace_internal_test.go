package petri

import (
	"regexp"
	"strings"
	"testing"
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
