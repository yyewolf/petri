package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	goDirectivePattern = regexp.MustCompile(`^go\s+(\S+)\s*$`)
	stagingPattern     = regexp.MustCompile(`^(?:replace\s+)?(\S+)\s+=>\s+\./staging/src/\S+\s*$`)
)

// parseUpstreamGoMod returns the go directive of an upstream go.mod and the
// modules it replaces with ./staging/src directories, in file order (only
// k8s.io/kubernetes has any).
func parseUpstreamGoMod(data []byte) (string, []string, error) {
	var (
		goVersion string
		staging   []string
	)

	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if m := goDirectivePattern.FindStringSubmatch(line); m != nil {
			goVersion = m[1]
		}

		if m := stagingPattern.FindStringSubmatch(line); m != nil {
			staging = append(staging, m[1])
		}
	}

	if err := sc.Err(); err != nil {
		return "", nil, fmt.Errorf("scan upstream go.mod: %w", err)
	}

	if goVersion == "" {
		return "", nil, errors.New("upstream go.mod has no go directive")
	}

	return goVersion, staging, nil
}

// renderWorkGoMod writes the go.mod of the scratch module that requires the
// mirrored upstreams ("path version" each) with every Kubernetes staging module
// pinned to its published version.
func renderWorkGoMod(goVersion string, require, staging []string, stagingVersion string) []byte {
	var b strings.Builder

	fmt.Fprintf(&b, "module petrimirror\n\ngo %s\n\nrequire (\n", goVersion)

	for _, r := range require {
		fmt.Fprintf(&b, "\t%s\n", r)
	}

	b.WriteString(")\n\nreplace (\n")

	for _, mod := range staging {
		fmt.Fprintf(&b, "\t%s => %s %s\n", mod, mod, stagingVersion)
	}

	b.WriteString(")\n")

	return []byte(b.String())
}
