package petri

import (
	"math/rand/v2"
	"strings"
)

const (
	maxNamespaceLen = 63
	suffixLen       = 5
	suffixAlphabet  = "abcdefghijklmnopqrstuvwxyz0123456789"
)

// namespaceName derives a valid namespace name from a test name: lowercase
// alphanumerics, runs of anything else collapsed to one dash, trimmed so the
// name plus "-" plus suffix fits in 63 characters.
func namespaceName(testName, suffix string) string {
	var b strings.Builder

	dash := false

	for _, r := range strings.ToLower(testName) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)

			dash = false

			continue
		}

		if !dash && b.Len() > 0 {
			b.WriteByte('-')

			dash = true
		}
	}

	base := strings.TrimRight(b.String(), "-")
	if maxBase := maxNamespaceLen - len(suffix) - 1; len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}

	if base == "" {
		return suffix
	}

	return base + "-" + suffix
}

func randomSuffix() string {
	b := make([]byte, suffixLen)
	for i := range b {
		b[i] = suffixAlphabet[rand.IntN(len(suffixAlphabet))] //nolint:gosec // Uniqueness, not secrecy.
	}

	return string(b)
}
