package main

import (
	"fmt"
	"slices"
	"strings"
)

const (
	kindPatch = "patch"
	kindMinor = "minor"
	mainName  = "main"
)

// Branch is a release line and the Kubernetes version its mirror holds.
type Branch struct {
	Name    string
	Version string
}

// Update is one mirror regeneration the workflow should open a PR for.
type Update struct {
	Branch  string `json:"branch"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
}

// Plan decides which branches need a mirror update, given every published
// k8s.io/kubernetes version. Prereleases are ignored. Each branch can get the
// newest patch of its own minor; main instead moves to the next minor, one
// step at a time, once that minor has a stable release.
func Plan(available []string, branches []Branch) ([]Update, error) {
	newest := map[int]version{} // newest stable 1.x release per minor

	for _, s := range available {
		v, ok := parseVersion(s)
		if !ok || !v.stable() || v.Major != 1 {
			continue
		}

		if cur, seen := newest[v.Minor]; !seen || cur.Patch < v.Patch {
			newest[v.Minor] = v
		}
	}

	updates := []Update{}

	for _, b := range branches {
		cur, ok := parseVersion(b.Version)
		if !ok {
			return nil, fmt.Errorf("branch %s: unparseable mirrored version %q", b.Name, b.Version)
		}

		if next, has := newest[cur.Minor+1]; b.Name == mainName && has {
			updates = append(updates, Update{Branch: b.Name, Version: next.String(), Kind: kindMinor})

			continue
		}

		if top, has := newest[cur.Minor]; has && top.Patch > cur.Patch {
			updates = append(updates, Update{Branch: b.Name, Version: top.String(), Kind: kindPatch})
		}
	}

	slices.SortFunc(updates, func(a, b Update) int { return strings.Compare(a.Branch, b.Branch) })

	return updates, nil
}
