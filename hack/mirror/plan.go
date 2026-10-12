package main

import (
	"fmt"
	"slices"
	"strings"
)

const (
	kindPatch = "patch"
	kindMinor = "minor"
	// kindKine is a kine patch update on a branch whose Kubernetes is current.
	kindKine = "kine"
	mainName = "main"
)

// Branch is a release line and the versions its mirrors hold. Kine is empty on
// branches cut before kine was mirrored.
type Branch struct {
	Name    string
	Version string
	Kine    string
}

// Update is one mirror regeneration the workflow should open a PR for. Version
// is the Kubernetes version to mirror, unchanged on a kindKine update; Kine is
// set when kine moves too.
type Update struct {
	Branch  string `json:"branch"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
	Kine    string `json:"kine,omitempty"`
}

// Plan decides which branches need a mirror update, given every published
// k8s.io/kubernetes and kine version. Prereleases are ignored. Each branch can
// get the newest patch of its own Kubernetes minor; main instead moves to the
// next minor, one step at a time, once that minor has a stable release. kine
// only gets patch updates within its minor: moving it to a new minor is a
// deliberate `make mirror KINE_VERSION=...`.
func Plan(available, kineAvailable []string, branches []Branch) ([]Update, error) {
	newest := newestPerMinor(available, 1)
	newestKine := newestPerMinor(kineAvailable, 0)

	updates := []Update{}

	for _, b := range branches {
		u, err := planBranch(b, newest, newestKine)
		if err != nil {
			return nil, err
		}

		if u.Kind != "" {
			updates = append(updates, u)
		}
	}

	slices.SortFunc(updates, func(a, b Update) int { return strings.Compare(a.Branch, b.Branch) })

	return updates, nil
}

// planBranch returns b's update, with an empty Kind when b is current.
func planBranch(b Branch, newest, newestKine map[int]version) (Update, error) {
	cur, ok := parseVersion(b.Version)
	if !ok {
		return Update{}, fmt.Errorf("branch %s: unparseable mirrored version %q", b.Name, b.Version)
	}

	u := Update{Branch: b.Name}

	if next, has := newest[cur.Minor+1]; b.Name == mainName && has {
		u.Version, u.Kind = next.String(), kindMinor
	} else if top, hasPatch := newest[cur.Minor]; hasPatch && top.Patch > cur.Patch {
		u.Version, u.Kind = top.String(), kindPatch
	}

	if b.Kine == "" {
		return u, nil
	}

	kine, ok := parseVersion(b.Kine)
	if !ok {
		return Update{}, fmt.Errorf("branch %s: unparseable mirrored kine version %q", b.Name, b.Kine)
	}

	if top, has := newestKine[kine.Minor]; has && top.Patch > kine.Patch {
		u.Kine = top.String()
		if u.Kind == "" {
			u.Version, u.Kind = b.Version, kindKine
		}
	}

	return u, nil
}

// newestPerMinor returns the newest stable release of each minor among the
// versions with the given major.
func newestPerMinor(available []string, major int) map[int]version {
	newest := map[int]version{}

	for _, s := range available {
		v, ok := parseVersion(s)
		if !ok || !v.stable() || v.Major != major {
			continue
		}

		if cur, seen := newest[v.Minor]; !seen || cur.Patch < v.Patch {
			newest[v.Minor] = v
		}
	}

	return newest
}
