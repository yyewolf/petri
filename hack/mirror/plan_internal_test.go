package main

import (
	"slices"
	"testing"
)

func TestPlan(t *testing.T) {
	t.Parallel()

	base := []string{
		"v1.36.0", "v1.36.4", "v1.36.5-rc.0",
		"v1.37.0", "v1.37.1", "v1.37.2",
		"v1.38.0-alpha.2", "v1.38.0-rc.1",
		"garbage",
	}
	with := func(extra ...string) []string { return append(slices.Clone(base), extra...) }

	cases := []struct {
		name      string
		available []string
		branches  []Branch
		want      []Update
	}{
		{
			name:      "patch on main",
			available: base,
			branches:  []Branch{{Name: "main", Version: "v1.37.1"}},
			want:      []Update{{Branch: "main", Version: "v1.37.2", Kind: kindPatch}},
		},
		{
			name:      "main already current",
			available: base,
			branches:  []Branch{{Name: "main", Version: "v1.37.2"}},
			want:      nil,
		},
		{
			name:      "prereleases are ignored",
			available: base,
			branches:  []Branch{{Name: "release-1.36", Version: "v1.36.4"}},
			want:      nil,
		},
		{
			name:      "release branch gets newest patch",
			available: base,
			branches:  []Branch{{Name: "release-1.36", Version: "v1.36.0"}},
			want:      []Update{{Branch: "release-1.36", Version: "v1.36.4", Kind: kindPatch}},
		},
		{
			name:      "new minor on main replaces the patch update",
			available: with("v1.38.0", "v1.38.1"),
			branches:  []Branch{{Name: "main", Version: "v1.37.1"}},
			want:      []Update{{Branch: "main", Version: "v1.38.1", Kind: kindMinor}},
		},
		{
			name:      "release branch never gets a minor",
			available: with("v1.38.0"),
			branches:  []Branch{{Name: "release-1.37", Version: "v1.37.2"}},
			want:      nil,
		},
		{
			name:      "minor steps one at a time",
			available: with("v1.38.0", "v1.39.0"),
			branches:  []Branch{{Name: "main", Version: "v1.37.2"}},
			want:      []Update{{Branch: "main", Version: "v1.38.0", Kind: kindMinor}},
		},
		{
			name:      "several branches come out sorted",
			available: base,
			branches: []Branch{
				{Name: "release-1.36", Version: "v1.36.0"},
				{Name: "main", Version: "v1.37.0"},
			},
			want: []Update{
				{Branch: "main", Version: "v1.37.2", Kind: kindPatch},
				{Branch: "release-1.36", Version: "v1.36.4", Kind: kindPatch},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Plan(tc.available, tc.branches)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if got == nil {
				t.Fatal("Plan returned nil; want a non-nil slice so the JSON matrix is []")
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("Plan = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPlanRejectsUnparseableBranchVersion(t *testing.T) {
	t.Parallel()

	if _, err := Plan([]string{"v1.37.1"}, []Branch{{Name: "main", Version: "garbage"}}); err == nil {
		t.Fatal("Plan accepted a branch with an unparseable mirrored version")
	}
}
