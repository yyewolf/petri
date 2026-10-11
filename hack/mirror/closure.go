package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// pkgFiles is one mirrored package and the union of its files across platforms.
type pkgFiles struct {
	ImportPath string
	Dir        string
	Files      map[string]struct{}
}

type listedPackage struct {
	ImportPath     string   `json:"ImportPath"`
	Dir            string   `json:"Dir"`
	GoFiles        []string `json:"GoFiles"`
	CgoFiles       []string `json:"CgoFiles"`
	IgnoredGoFiles []string `json:"IgnoredGoFiles"`
	EmbedFiles     []string `json:"EmbedFiles"`
	SFiles         []string `json:"SFiles"`
	CFiles         []string `json:"CFiles"`
	HFiles         []string `json:"HFiles"`
}

type platform struct{ goos, goarch string }

func platforms() []platform {
	var out []platform

	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			out = append(out, platform{goos: goos, goarch: goarch})
		}
	}

	return out
}

// collect reads a stream of `go list -json` objects and merges every package
// under module into into, skipping _test.go files.
func collect(r io.Reader, module string, into map[string]*pkgFiles) error {
	dec := json.NewDecoder(r)

	for {
		var p listedPackage

		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("decode go list output: %w", err)
		}

		if p.ImportPath != module && !strings.HasPrefix(p.ImportPath, module+"/") {
			continue
		}

		entry, ok := into[p.ImportPath]
		if !ok {
			entry = &pkgFiles{ImportPath: p.ImportPath, Dir: p.Dir, Files: map[string]struct{}{}}
			into[p.ImportPath] = entry
		}

		for _, list := range [][]string{
			p.GoFiles, p.CgoFiles, p.IgnoredGoFiles, p.EmbedFiles, p.SFiles, p.CFiles, p.HFiles,
		} {
			for _, f := range list {
				if !strings.HasSuffix(f, "_test.go") {
					entry.Files[f] = struct{}{}
				}
			}
		}
	}
}

// closure lists the upstream packages the roots need on every supported
// platform, with and without cgo.
func closure(ctx context.Context, work string, roots []string) (map[string]*pkgFiles, error) {
	pkgs := map[string]*pkgFiles{}

	for _, p := range platforms() {
		for _, cgo := range []string{"0", "1"} {
			env := []string{"GOOS=" + p.goos, "GOARCH=" + p.goarch, "CGO_ENABLED=" + cgo, modModEnv}

			out, err := goCmd(ctx, work, env, append([]string{"list", "-deps", "-json"}, roots...)...)
			if err != nil {
				return nil, err
			}

			if err = collect(bytes.NewReader(out), upstreamModule, pkgs); err != nil {
				return nil, err
			}
		}
	}

	return pkgs, nil
}
