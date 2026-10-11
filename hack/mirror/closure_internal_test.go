package main

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestCollect(t *testing.T) {
	t.Parallel()

	// Two go list -json runs (two platforms) concatenated, as collect sees them.
	const linux = `{"ImportPath":"k8s.io/kubernetes/pkg/a","Dir":"/m/pkg/a",
  "GoFiles":["a.go","a_linux.go"],"IgnoredGoFiles":["a_windows.go","a_windows_test.go"]}
{"ImportPath":"k8s.io/api/core/v1","Dir":"/api/core/v1","GoFiles":["types.go"]}
{"ImportPath":"k8s.io/kubernetesfoo/x","Dir":"/foo/x","GoFiles":["x.go"]}
{"ImportPath":"fmt","Dir":"/goroot/fmt","Standard":true,"GoFiles":["print.go"]}`
	const windows = `{"ImportPath":"k8s.io/kubernetes/pkg/a","Dir":"/m/pkg/a",
  "GoFiles":["a.go","a_windows.go"],"EmbedFiles":["testdata/pod.yaml"]}
{"ImportPath":"k8s.io/kubernetes/pkg/winonly","Dir":"/m/pkg/winonly","GoFiles":["w.go"],"CgoFiles":["c.go"]}`

	pkgs := map[string]*pkgFiles{}
	for _, stream := range []string{linux, windows} {
		if err := collect(strings.NewReader(stream), "k8s.io/kubernetes", pkgs); err != nil {
			t.Fatalf("collect: %v", err)
		}
	}

	if got := slices.Sorted(
		maps.Keys(pkgs),
	); !slices.Equal(
		got,
		[]string{"k8s.io/kubernetes/pkg/a", "k8s.io/kubernetes/pkg/winonly"},
	) {
		t.Fatalf("packages = %v", got)
	}

	a := pkgs["k8s.io/kubernetes/pkg/a"]
	if a.Dir != "/m/pkg/a" {
		t.Errorf("Dir = %q", a.Dir)
	}
	wantA := []string{"a.go", "a_linux.go", "a_windows.go", "testdata/pod.yaml"}
	if got := slices.Sorted(maps.Keys(a.Files)); !slices.Equal(got, wantA) {
		t.Errorf("pkg/a files = %v, want %v", got, wantA)
	}

	wantW := []string{"c.go", "w.go"}
	if got := slices.Sorted(maps.Keys(pkgs["k8s.io/kubernetes/pkg/winonly"].Files)); !slices.Equal(got, wantW) {
		t.Errorf("pkg/winonly files = %v, want %v", got, wantW)
	}
}

func TestCollectRejectsGarbage(t *testing.T) {
	t.Parallel()

	if err := collect(strings.NewReader("{not json"), "k8s.io/kubernetes", map[string]*pkgFiles{}); err == nil {
		t.Fatal("collect accepted malformed go list output")
	}
}
