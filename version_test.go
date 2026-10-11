package petri_test

import (
	"regexp"
	"testing"

	"github.com/yyewolf/petri"
)

func TestKubernetesVersionFormat(t *testing.T) {
	t.Parallel()

	if !regexp.MustCompile(`^1\.[0-9]+$`).MatchString(petri.KubernetesVersion) {
		t.Fatalf("KubernetesVersion = %q, want a Kubernetes minor such as \"1.37\"", petri.KubernetesVersion)
	}
}
