package petri_test

import (
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// controllerruntimeGoMod reads the sub-module's go.mod.
func controllerruntimeGoMod(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile("controllerruntime/go.mod")
	if err != nil {
		t.Fatalf("read controllerruntime/go.mod: %v", err)
	}

	return string(data)
}

// TestControllerruntimeGoModSanity guards the sub-module's go.mod state.
// Before petri's first release, the go.mod cannot require
// github.com/yyewolf/petri at all: any version that does not resolve yet
// poisons the whole workspace graph, and workspace tidy drops it anyway. The
// release flow adds the requirement after the petri tag exists, and
// check-release-tag.sh enforces it at tag time. What must hold at all times is
// that whatever requirements are there are well-formed and the workspace wiring
// is intact.
func TestControllerruntimeGoModSanity(t *testing.T) {
	t.Parallel()

	gomod := controllerruntimeGoMod(t)

	wellFormed := regexp.MustCompile(`github.com/yyewolf/petri\s+(v\S+)`)
	for _, m := range wellFormed.FindAllStringSubmatch(gomod, -1) {
		version := m[1]
		resolvable := regexp.MustCompile(`^v\d+\.\d+\.\d+(-[\w.-]+)?$`)
		if !resolvable.MatchString(version) {
			t.Errorf("petri requirement %q is not a version consumers can resolve", version)
		}
	}

	if !strings.Contains(gomod, "sigs.k8s.io/controller-runtime") {
		t.Error("controllerruntime/go.mod does not require sigs.k8s.io/controller-runtime")
	}
}

// TestBuildGraphStaysLean fails when a module petri deliberately keeps out of
// its users' graphs is built again. kine is mirrored so its go directive cannot
// raise the users' Go floor; its other storage backends and etcd's server only
// come back through upstream changes the mirror would otherwise take in
// silently. go.mod is not checked: tidy keeps etcd's server there for a test of
// k8s.io/apiserver, which users never build.
func TestBuildGraphStaysLean(t *testing.T) {
	t.Parallel()

	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{with .Module}}{{.Path}}{{end}}", ".").
		Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}

	built := strings.Fields(string(out))

	for _, banned := range []string{
		"github.com/k3s-io/kine",         // mirrored into internal/kine
		"go.etcd.io/etcd/server/v3",      // patched out by hack/mirror/patches/kine
		"go.etcd.io/raft/v3",             // etcd server
		"go.etcd.io/bbolt",               // etcd server
		"github.com/nats-io/",            // kine's NATS backend
		"github.com/jackc/pgx/",          // kine's Postgres backend
		"github.com/go-sql-driver/mysql", // kine's MySQL backend
		"github.com/mattn/go-sqlite3",    // kine's SQLite backend (cgo)
		"modernc.org/sqlite",             // kine's SQLite backend
		"github.com/minio/minio-go/",     // kine's S3 snapshots
		"github.com/cockroachdb/pebble",  // kine's t4 backend
		"github.com/t4db/t4",             // kine's t4 backend
	} {
		if slices.ContainsFunc(built, func(m string) bool { return strings.HasPrefix(m, banned) }) {
			t.Errorf("petri builds %s again; `go mod why -m` names the import that brought it back", banned)
		}
	}
}

// TestWorkspaceWiringSanity guards the go.work wiring both modules and the
// genproto stand-in depend on.
func TestWorkspaceWiringSanity(t *testing.T) {
	t.Parallel()

	work, err := os.ReadFile("go.work")
	if err != nil {
		t.Fatalf("read go.work: %v", err)
	}

	for _, want := range []string{"use (", ".", "./controllerruntime", "hack/genproto-stub"} {
		if !strings.Contains(string(work), want) {
			t.Errorf("go.work is missing %q", want)
		}
	}
}
