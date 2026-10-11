package main

import "testing"

func TestProxyURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in, want string
	}{
		{in: "", want: "https://proxy.golang.org"},
		{in: "direct", want: "https://proxy.golang.org"},
		{in: "off", want: "https://proxy.golang.org"},
		{in: "https://goproxy.example/,direct", want: "https://goproxy.example"},
		{in: "direct|https://second.example", want: "https://second.example"},
		{in: "  https://a.example  ,https://b.example", want: "https://a.example"},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			if got := proxyURL(tc.in); got != tc.want {
				t.Errorf("proxyURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestReleaseBranch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ref    string
		want   string
		wantOK bool
	}{
		{ref: "origin/main", want: "main", wantOK: true},
		{ref: "origin/release-1.36", want: "release-1.36", wantOK: true},
		{ref: "origin/HEAD", wantOK: false},
		{ref: "origin", wantOK: false},
		{ref: "origin/mirror/v1.37.2", wantOK: false},
		{ref: "origin/release-1.x", wantOK: false},
		{ref: "upstream/main", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			t.Parallel()

			got, ok := releaseBranch(tc.ref, "origin")
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("releaseBranch(%q) = %q, %v; want %q, %v", tc.ref, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
