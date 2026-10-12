package main

import (
	"slices"
	"testing"
)

func TestParseUpstreamGoMod(t *testing.T) {
	t.Parallel()

	const src = `// This is a generated file. Do not edit directly.

module k8s.io/kubernetes

go 1.26.0

require (
	k8s.io/api v0.0.0
	k8s.io/client-go v0.0.0
)

replace (
	k8s.io/api => ./staging/src/k8s.io/api
	k8s.io/client-go => ./staging/src/k8s.io/client-go
	github.com/example/fork => github.com/example/fork v1.2.3
)

replace k8s.io/kms => ./staging/src/k8s.io/kms
`

	goVersion, staging, err := parseUpstreamGoMod([]byte(src))
	if err != nil {
		t.Fatalf("parseUpstreamGoMod: %v", err)
	}
	if goVersion != "1.26.0" {
		t.Errorf("go version = %q, want 1.26.0", goVersion)
	}
	if want := []string{"k8s.io/api", "k8s.io/client-go", "k8s.io/kms"}; !slices.Equal(staging, want) {
		t.Errorf("staging = %v, want %v", staging, want)
	}
}

func TestParseUpstreamGoModRequiresGoDirective(t *testing.T) {
	t.Parallel()

	if _, _, err := parseUpstreamGoMod([]byte("module k8s.io/kubernetes\n")); err == nil {
		t.Fatal("parseUpstreamGoMod accepted a go.mod with no go directive")
	}
}

func TestRenderWorkGoMod(t *testing.T) {
	t.Parallel()

	got := string(renderWorkGoMod(
		"1.26.5",
		[]string{"k8s.io/kubernetes v1.37.1", "github.com/k3s-io/kine v0.17.2"},
		[]string{"k8s.io/api", "k8s.io/client-go"},
		"v0.37.1",
	))

	const want = "module petrimirror\n\ngo 1.26.5\n\nrequire (\n" +
		"\tk8s.io/kubernetes v1.37.1\n\tgithub.com/k3s-io/kine v0.17.2\n)\n\nreplace (\n" +
		"\tk8s.io/api => k8s.io/api v0.37.1\n\tk8s.io/client-go => k8s.io/client-go v0.37.1\n)\n"
	if got != want {
		t.Errorf("renderWorkGoMod =\n%s\nwant\n%s", got, want)
	}
}
