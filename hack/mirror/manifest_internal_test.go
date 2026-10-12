package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestManifestRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "MIRROR.json")
	want := manifest{Kubernetes: "v1.37.1", Roots: []string{"k8s.io/kubernetes/cmd/kube-apiserver/app"}}

	if err := want.write(path); err != nil {
		t.Fatalf("write: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	const wantRaw = "{\n  \"kubernetes\": \"v1.37.1\",\n  \"roots\": [\n" +
		"    \"k8s.io/kubernetes/cmd/kube-apiserver/app\"\n  ]\n}\n"
	if string(raw) != wantRaw {
		t.Errorf("file contents = %q, want %q", raw, wantRaw)
	}

	got, err := readManifest(path)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if got.Kubernetes != want.Kubernetes || !slices.Equal(got.Roots, want.Roots) {
		t.Errorf("readManifest = %+v, want %+v", got, want)
	}
}

func TestManifestOmitsTheOtherSourcesVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "MIRROR.json")
	if err := (manifest{Kine: "v0.17.2", Roots: []string{"github.com/k3s-io/kine/pkg/server"}}).write(
		path,
	); err != nil {
		t.Fatalf("write: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	const wantRaw = "{\n  \"kine\": \"v0.17.2\",\n  \"roots\": [\n" +
		"    \"github.com/k3s-io/kine/pkg/server\"\n  ]\n}\n"
	if string(raw) != wantRaw {
		t.Errorf("file contents = %q, want %q", raw, wantRaw)
	}
}

func TestReadManifestRequiresRoots(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "MIRROR.json")
	if err := os.WriteFile(path, []byte(`{"kubernetes":"v1.37.1","roots":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := readManifest(path); err == nil {
		t.Fatal("readManifest accepted a manifest with no roots")
	}
}
