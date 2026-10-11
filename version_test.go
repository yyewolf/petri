package petri

import (
	"regexp"
	"testing"
)

func TestKubernetesVersionFormat(t *testing.T) {
	if !regexp.MustCompile(`^1\.[0-9]+$`).MatchString(KubernetesVersion) {
		t.Fatalf("KubernetesVersion = %q, want a Kubernetes minor such as \"1.37\"", KubernetesVersion)
	}
}
