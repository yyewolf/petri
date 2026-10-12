package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// patchRepo creates a git repository holding a staged kine tree with one file
// and returns its root.
func patchRepo(t *testing.T, content string) string {
	t.Helper()

	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	if err := writeFile(filepath.Join(kineSource.stagedDir(repo), "pkg", "a.go"), []byte(content)); err != nil {
		t.Fatal(err)
	}

	return repo
}

func writePatch(t *testing.T, repo, name, body string) {
	t.Helper()

	if err := writeFile(filepath.Join(kineSource.patchDir(repo), name), []byte(body)); err != nil {
		t.Fatal(err)
	}
}

const patchAB = `--- a/pkg/a.go
+++ b/pkg/a.go
@@ -1,2 +1,2 @@
 package a
-const x = 1
+const x = 2
`

func TestApplyPatches(t *testing.T) {
	t.Parallel()

	repo := patchRepo(t, "package a\nconst x = 1\n")
	writePatch(t, repo, "0001-x.patch", patchAB)
	writePatch(t, repo, "0002-y.patch", `--- a/pkg/a.go
+++ b/pkg/a.go
@@ -1,2 +1,3 @@
 package a
 const x = 2
+const y = 3
`)

	if err := applyPatches(context.Background(), repo, kineSource); err != nil {
		t.Fatalf("applyPatches: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(kineSource.stagedDir(repo), "pkg", "a.go"))
	if err != nil {
		t.Fatal(err)
	}

	if want := "package a\nconst x = 2\nconst y = 3\n"; string(got) != want {
		t.Errorf("patched file = %q, want %q (patches applied in name order)", got, want)
	}
}

func TestApplyPatchesFailsWhenUpstreamMoved(t *testing.T) {
	t.Parallel()

	repo := patchRepo(t, "package a\nconst x = 5\n")
	writePatch(t, repo, "0001-x.patch", patchAB)

	err := applyPatches(context.Background(), repo, kineSource)
	if err == nil || !strings.Contains(err.Error(), "0001-x.patch no longer applies") {
		t.Fatalf("applyPatches = %v, want an error naming the stale patch", err)
	}
}

func TestApplyPatchesWithoutPatches(t *testing.T) {
	t.Parallel()

	if err := applyPatches(context.Background(), patchRepo(t, "package a\n"), kubeSource); err != nil {
		t.Fatalf("applyPatches with no patch directory: %v", err)
	}
}
