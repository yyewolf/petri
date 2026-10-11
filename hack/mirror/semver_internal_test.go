package main

import "testing"

func TestParseVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in     string
		want   version
		wantOK bool
	}{
		{in: "v1.37.1", want: version{Major: 1, Minor: 37, Patch: 1}, wantOK: true},
		{in: "v1.38.0-alpha.2", want: version{Major: 1, Minor: 38, Pre: "alpha.2"}, wantOK: true},
		{in: "v0.37.1", want: version{Minor: 37, Patch: 1}, wantOK: true},
		{in: "1.37.1", wantOK: false},
		{in: "v1.37", wantOK: false},
		{in: "v1.37.1+meta", wantOK: false},
		{in: "", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			got, ok := parseVersion(tc.in)
			if ok != tc.wantOK || (ok && got != tc.want) {
				t.Fatalf("parseVersion(%q) = %+v, %v; want %+v, %v", tc.in, got, ok, tc.want, tc.wantOK)
			}
			if ok && got.String() != tc.in {
				t.Errorf("String() = %q, want %q", got.String(), tc.in)
			}
		})
	}
}

func TestGoVersionLess(t *testing.T) {
	t.Parallel()

	cases := []struct {
		a, b string
		want bool
	}{
		{a: "1.26.0", b: "1.26.5", want: true},
		{a: "1.26.5", b: "1.26.0", want: false},
		{a: "1.26", b: "1.26.0", want: false},
		{a: "1.26", b: "1.27", want: true},
		{a: "1.9", b: "1.10", want: true},
		{a: "1.26.0", b: "1.26.0", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.a+"<"+tc.b, func(t *testing.T) {
			t.Parallel()

			if got := goVersionLess(tc.a, tc.b); got != tc.want {
				t.Errorf("goVersionLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
