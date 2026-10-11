package petri_test

import (
	"os"
	"regexp"
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
