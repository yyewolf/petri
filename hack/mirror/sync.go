package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	upstreamModule = "k8s.io/kubernetes"
	mirrorModule   = "github.com/yyewolf/petri/internal/kubernetes"
	mirrorDir      = "internal/kubernetes"
	manifestName   = "MIRROR.json"
	stagedDir      = "internal/.kubernetes-new"
	retiredDir     = "internal/.kubernetes-old"
	// modModEnv lets the scratch module resolve and update its go.mod freely.
	modModEnv = "GOFLAGS=-mod=mod"
)

var (
	kubeVersionLine = regexp.MustCompile(`(?m)^const KubernetesVersion = "1\.\d+"$`)
	repoGoDirective = regexp.MustCompile(`(?m)^go\s+(\S+)\s*$`)
)

type downloadedModule struct {
	Dir   string `json:"Dir"`
	GoMod string `json:"GoMod"`
}

type listedModule struct {
	Path    string        `json:"Path"`
	Version string        `json:"Version"`
	Main    bool          `json:"Main"`
	Replace *listedModule `json:"Replace"`
}

// runSync regenerates internal/kubernetes for Kubernetes version ver (or the
// version MIRROR.json records when ver is empty) and pins go.mod to match.
func runSync(ctx context.Context, repo, ver string) error {
	m, err := readManifest(filepath.Join(repo, mirrorDir, manifestName))
	if err != nil {
		return err
	}

	if ver == "" {
		ver = m.Kubernetes
	}

	v, ok := parseVersion(ver)
	if !ok || !v.stable() || v.Major != 1 {
		return fmt.Errorf("invalid Kubernetes version %q: want a stable v1.N.P", ver)
	}

	work, err := os.MkdirTemp("", "petri-mirror-")
	if err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	defer os.RemoveAll(work)

	upstream, upstreamGo, err := resolve(ctx, work, v, m.Roots)
	if err != nil {
		return err
	}

	pkgs, err := closure(ctx, work, m.Roots)
	if err != nil {
		return err
	}

	staged := filepath.Join(repo, stagedDir)
	if err = writeTree(staged, upstream.Dir, pkgs, manifest{Kubernetes: v.String(), Roots: m.Roots}); err != nil {
		return err
	}

	if err = swapAndPin(ctx, repo, work, upstreamGo); err != nil {
		return err
	}

	if err = setKubernetesVersion(filepath.Join(repo, "version.go"), v.Minor); err != nil {
		return err
	}

	if _, err = goCmd(ctx, repo, nil, "build", "./..."); err != nil {
		return fmt.Errorf("verify build: %w", err)
	}

	return nil
}

// resolve creates the scratch module in work and returns the downloaded
// upstream module and its go directive.
func resolve(ctx context.Context, work string, v version, roots []string) (downloadedModule, string, error) {
	out, err := goCmd(ctx, work, nil, "mod", "download", "-json", upstreamModule+"@"+v.String())
	if err != nil {
		return downloadedModule{}, "", err
	}

	var upstream downloadedModule
	if err = json.Unmarshal(out, &upstream); err != nil {
		return downloadedModule{}, "", fmt.Errorf("decode go mod download output: %w", err)
	}

	raw, err := os.ReadFile(upstream.GoMod)
	if err != nil {
		return downloadedModule{}, "", fmt.Errorf("read upstream go.mod: %w", err)
	}

	upstreamGo, staging, err := parseUpstreamGoMod(raw)
	if err != nil {
		return downloadedModule{}, "", err
	}

	stagingVersion := fmt.Sprintf("v0.%d.%d", v.Minor, v.Patch)

	goMod := renderWorkGoMod(upstreamGo, v.String(), staging, stagingVersion)
	if err = os.WriteFile(filepath.Join(work, "go.mod"), goMod, 0o600); err != nil {
		return downloadedModule{}, "", fmt.Errorf("write work go.mod: %w", err)
	}

	var main strings.Builder

	main.WriteString("package main\n\nimport (\n")

	for _, r := range roots {
		fmt.Fprintf(&main, "\t_ %q\n", r)
	}

	main.WriteString(")\n\nfunc main() {}\n")

	if err = os.WriteFile(filepath.Join(work, "main.go"), []byte(main.String()), 0o600); err != nil {
		return downloadedModule{}, "", fmt.Errorf("write work main.go: %w", err)
	}

	if _, err = goCmd(ctx, work, []string{modModEnv}, "mod", "tidy"); err != nil {
		return downloadedModule{}, "", err
	}

	return upstream, upstreamGo, nil
}

// writeTree writes the mirrored packages, the upstream LICENSE and MIRROR.json
// into dst, replacing anything already there.
func writeTree(dst, upstreamDir string, pkgs map[string]*pkgFiles, m manifest) error {
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("clear %s: %w", dst, err)
	}

	for _, p := range pkgs {
		rel := strings.TrimPrefix(strings.TrimPrefix(p.ImportPath, upstreamModule), "/")

		for f := range p.Files {
			data, err := os.ReadFile(filepath.Join(p.Dir, filepath.FromSlash(f)))
			if err != nil {
				return fmt.Errorf("read %s/%s: %w", p.ImportPath, f, err)
			}

			if strings.HasSuffix(f, ".go") {
				data, err = rewriteImports(f, data, upstreamModule, mirrorModule)
				if err != nil {
					return err
				}
			}

			if err = writeFile(filepath.Join(dst, filepath.FromSlash(rel), filepath.FromSlash(f)), data); err != nil {
				return err
			}
		}
	}

	license, err := os.ReadFile(filepath.Join(upstreamDir, "LICENSE"))
	if err != nil {
		return fmt.Errorf("read upstream LICENSE: %w", err)
	}

	if err = writeFile(filepath.Join(dst, "LICENSE"), license); err != nil {
		return err
	}

	return m.write(filepath.Join(dst, manifestName))
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}

	if err := os.WriteFile(
		path,
		data,
		0o644,
	); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

// swapAndPin moves the staged tree into place and pins go.mod. On failure the
// previous tree, go.mod and go.sum are restored.
func swapAndPin(ctx context.Context, repo, work, upstreamGo string) error {
	live := filepath.Join(repo, mirrorDir)
	retired := filepath.Join(repo, retiredDir)

	if err := os.RemoveAll(retired); err != nil {
		return fmt.Errorf("clear %s: %w", retired, err)
	}

	backup, err := backupFiles(repo, "go.mod", "go.sum")
	if err != nil {
		return err
	}

	if err = os.Rename(live, retired); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("retire old mirror: %w", err)
	}

	if err = os.Rename(filepath.Join(repo, stagedDir), live); err != nil {
		return errors.Join(fmt.Errorf("install new mirror: %w", err), os.Rename(retired, live))
	}

	if err = pinGoMod(ctx, repo, work, upstreamGo); err != nil {
		return errors.Join(
			fmt.Errorf("pin go.mod: %w", err),
			os.RemoveAll(live),
			os.Rename(retired, live),
			restoreFiles(repo, backup),
		)
	}

	if err = os.RemoveAll(retired); err != nil {
		return fmt.Errorf("remove old mirror: %w", err)
	}

	return nil
}

// pinGoMod requires, in the repository, every module version the scratch
// module selected, raises the go directive to upstream's if it is newer, and
// tidies. MVS keeps any higher version petri's own dependencies need.
func pinGoMod(ctx context.Context, repo, work, upstreamGo string) error {
	out, err := goCmd(ctx, work, []string{modModEnv}, "list", "-m", "-json", "all")
	if err != nil {
		return err
	}

	args := []string{"mod", "edit"}

	current, err := os.ReadFile(filepath.Join(repo, "go.mod"))
	if err != nil {
		return fmt.Errorf("read go.mod: %w", err)
	}

	if m := repoGoDirective.FindSubmatch(current); m != nil && goVersionLess(string(m[1]), upstreamGo) {
		args = append(args, "-go="+upstreamGo)
	}

	dec := json.NewDecoder(bytes.NewReader(out))

	for {
		var mod listedModule

		decErr := dec.Decode(&mod)
		if errors.Is(decErr, io.EOF) {
			break
		}

		if decErr != nil {
			return fmt.Errorf("decode go list -m output: %w", decErr)
		}

		if mod.Main || mod.Path == upstreamModule {
			continue
		}

		ver := mod.Version
		if mod.Replace != nil {
			ver = mod.Replace.Version
		}

		args = append(args, "-require="+mod.Path+"@"+ver)
	}

	if _, err = goCmd(ctx, repo, nil, args...); err != nil {
		return err
	}

	if _, err = goCmd(ctx, repo, nil, "mod", "tidy"); err != nil {
		return err
	}

	return nil
}

func setKubernetesVersion(path string, minor int) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	if !kubeVersionLine.Match(src) {
		return fmt.Errorf("%s has no `const KubernetesVersion = \"1.N\"` line", path)
	}

	out := kubeVersionLine.ReplaceAll(src, fmt.Appendf(nil, `const KubernetesVersion = "1.%d"`, minor))

	if err = os.WriteFile(
		path,
		out,
		0o644,
	); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

func backupFiles(dir string, names ...string) (map[string][]byte, error) {
	saved := map[string][]byte{}

	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("back up %s: %w", name, err)
		}

		saved[name] = data
	}

	return saved, nil
}

func restoreFiles(dir string, saved map[string][]byte) error {
	var errs []error

	for name, data := range saved {
		if err := os.WriteFile(
			filepath.Join(dir, name),
			data,
			0o644,
		); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", name, err))
		}
	}

	return errors.Join(errs...)
}

// goCmd runs the go command in dir with GOWORK=off plus env, returning stdout.
func goCmd(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GOWORK=off"), env...)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s (in %s): %w\n%s", strings.Join(args, " "), dir, err, stderr.String())
	}

	return out, nil
}
