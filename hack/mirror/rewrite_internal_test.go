package main

import "testing"

func TestRewriteImports(t *testing.T) {
	t.Parallel()

	const (
		oldPrefix = "k8s.io/kubernetes"
		newPrefix = "github.com/yyewolf/petri/internal/kubernetes"
	)

	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "single import",
			src:  "package a\n\nimport \"k8s.io/kubernetes/pkg/api\"\n",
			want: "package a\n\nimport \"github.com/yyewolf/petri/internal/kubernetes/pkg/api\"\n",
		},
		{
			name: "block with mixed paths keeps others and formatting",
			src: "package a\n\nimport (\n\t\"fmt\"\n\n\t\"k8s.io/api/core/v1\"\n" +
				"\t\"k8s.io/kubernetes/pkg/apis/core\" // trailing comment\n)\n\nvar _ = fmt.Sprint\n",
			want: "package a\n\nimport (\n\t\"fmt\"\n\n\t\"k8s.io/api/core/v1\"\n" +
				"\t\"github.com/yyewolf/petri/internal/kubernetes/pkg/apis/core\" // trailing comment\n)\n\nvar _ = fmt.Sprint\n",
		},
		{
			name: "aliased, dot and blank imports",
			src: "package a\n\nimport (\n\tapi \"k8s.io/kubernetes/pkg/api\"\n" +
				"\t. \"k8s.io/kubernetes/pkg/dot\"\n\t_ \"k8s.io/kubernetes/pkg/blank\"\n)\n",
			want: "package a\n\nimport (\n\tapi \"github.com/yyewolf/petri/internal/kubernetes/pkg/api\"\n" +
				"\t. \"github.com/yyewolf/petri/internal/kubernetes/pkg/dot\"\n" +
				"\t_ \"github.com/yyewolf/petri/internal/kubernetes/pkg/blank\"\n)\n",
		},
		{
			name: "build-tagged file",
			src:  "//go:build windows\n\npackage a\n\nimport \"k8s.io/kubernetes/pkg/util\"\n",
			want: "//go:build windows\n\npackage a\n\nimport \"github.com/yyewolf/petri/internal/kubernetes/pkg/util\"\n",
		},
		{
			name: "module root import",
			src:  "package a\n\nimport \"k8s.io/kubernetes\"\n",
			want: "package a\n\nimport \"github.com/yyewolf/petri/internal/kubernetes\"\n",
		},
		{
			name: "look-alike prefix is untouched",
			src:  "package a\n\nimport \"k8s.io/kubernetesfoo/pkg\"\n",
			want: "package a\n\nimport \"k8s.io/kubernetesfoo/pkg\"\n",
		},
		{
			name: "raw string import",
			src:  "package a\n\nimport `k8s.io/kubernetes/pkg/raw`\n",
			want: "package a\n\nimport \"github.com/yyewolf/petri/internal/kubernetes/pkg/raw\"\n",
		},
		{
			name: "no imports stays byte-identical",
			src:  "// Package a.\npackage a\n\nconst X = \"k8s.io/kubernetes/pkg/notanimport\"\n",
			want: "// Package a.\npackage a\n\nconst X = \"k8s.io/kubernetes/pkg/notanimport\"\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := rewriteImports("a.go", []byte(tc.src), oldPrefix, newPrefix)
			if err != nil {
				t.Fatalf("rewriteImports: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("rewriteImports mismatch\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestRewriteImportsRejectsInvalidGo(t *testing.T) {
	t.Parallel()

	if _, err := rewriteImports("bad.go", []byte("not go"), "a", "b"); err == nil {
		t.Fatal("rewriteImports accepted a file that does not parse")
	}
}
