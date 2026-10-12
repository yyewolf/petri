package main

import (
	"bytes"
	"cmp"
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
	"slices"
	"strings"
)

const (
	petriModule  = "github.com/yyewolf/petri"
	manifestName = "MIRROR.json"
	// modModEnv lets the scratch module resolve and update its go.mod freely.
	modModEnv = "GOFLAGS=-mod=mod"
	// autoToolchainEnv lets go commands on the upstreams switch to a newer
	// toolchain when one of them needs it: kine often declares a newer Go
	// than petri's floor, and CI's setup-go pins GOTOOLCHAIN=local.
	autoToolchainEnv = "GOTOOLCHAIN=auto"
)

// source is one upstream module mirrored into the repository: its packages
// needed by the roots in dir/MIRROR.json, with imports rewritten under dir.
type source struct {
	name   string // names the staging directories
	module string // upstream module path
	dir    string // mirror directory, relative to the repository root
}

//nolint:gochecknoglobals // fixed configuration.
var (
	kubeSource = source{name: "kubernetes", module: "k8s.io/kubernetes", dir: "internal/kubernetes"}
	kineSource = source{name: "kine", module: "github.com/k3s-io/kine", dir: "internal/kine"}
	sources    = []source{kubeSource, kineSource}
)

func (s source) mirrorModule() string { return petriModule + "/" + s.dir }

func (s source) manifestPath(repo string) string {
	return filepath.Join(repo, filepath.FromSlash(s.dir), manifestName)
}

func (s source) stagedDir(repo string) string {
	return filepath.Join(repo, "internal", "."+s.name+"-new")
}

func (s source) retiredDir(repo string) string {
	return filepath.Join(repo, "internal", "."+s.name+"-old")
}

// patchDir holds the reviewed changes to s's mirror: unified diffs with paths
// relative to the mirror directory, applied in name order after imports are
// rewritten. They are the only way mirrored code may differ from upstream.
func (s source) patchDir(repo string) string {
	return filepath.Join(repo, "hack", "mirror", "patches", s.name)
}

// mirrored reports whether module is one of the mirrored upstreams.
func mirrored(module string) bool {
	return slices.ContainsFunc(sources, func(s source) bool { return s.module == module })
}

var (
	kubeVersionLine = regexp.MustCompile(`(?m)^const KubernetesVersion = "1\.\d+"$`)
	repoGoDirective = regexp.MustCompile(`(?m)^go\s+(\S+)\s*$`)
)

type downloadedModule struct {
	Dir   string `json:"Dir"`
	GoMod string `json:"GoMod"`
	Error string `json:"Error"`
}

type listedModule struct {
	Path    string        `json:"Path"`
	Version string        `json:"Version"`
	Main    bool          `json:"Main"`
	Replace *listedModule `json:"Replace"`
}

// runSync regenerates internal/kubernetes and internal/kine for Kubernetes
// version kubeVer and kine version kineVer (each defaulting to the version its
// MIRROR.json records) and pins go.mod to match.
func runSync(ctx context.Context, repo, kubeVer, kineVer string) error {
	kubeManifest, err := readManifest(kubeSource.manifestPath(repo))
	if err != nil {
		return err
	}

	kineManifest, err := readManifest(kineSource.manifestPath(repo))
	if err != nil {
		return err
	}

	kube, err := pickVersion(kubeVer, kubeManifest.Kubernetes, 1)
	if err != nil {
		return fmt.Errorf("invalid Kubernetes version: %w", err)
	}

	kine, err := pickVersion(kineVer, kineManifest.Kine, 0)
	if err != nil {
		return fmt.Errorf("invalid kine version: %w", err)
	}

	work, err := os.MkdirTemp("", "petri-mirror-")
	if err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	defer os.RemoveAll(work)

	roots := slices.Concat(kubeManifest.Roots, kineManifest.Roots)

	upstream, kubeGo, err := resolve(ctx, work, kube, kine, roots)
	if err != nil {
		return err
	}

	pkgs, err := closure(ctx, work, roots)
	if err != nil {
		return err
	}

	staged := map[source]manifest{
		kubeSource: {Kubernetes: kube.String(), Roots: kubeManifest.Roots},
		kineSource: {Kine: kine.String(), Roots: kineManifest.Roots},
	}

	for _, src := range sources {
		if err = writeTree(src.stagedDir(repo), src, upstream[src.module], pkgs[src.module], staged[src]); err != nil {
			return err
		}

		if err = applyPatches(ctx, repo, src); err != nil {
			return err
		}
	}

	if err = swapAndPin(ctx, repo, work, kubeGo); err != nil {
		return err
	}

	if err = setKubernetesVersion(filepath.Join(repo, "version.go"), kube.Minor); err != nil {
		return err
	}

	if _, err = goCmd(ctx, repo, nil, "build", "./..."); err != nil {
		return fmt.Errorf("verify build: %w", err)
	}

	return nil
}

// pickVersion parses requested, or recorded when requested is empty, and
// requires a stable release with the given major version.
func pickVersion(requested, recorded string, major int) (version, error) {
	s := cmp.Or(requested, recorded)

	v, ok := parseVersion(s)
	if !ok || !v.stable() || v.Major != major {
		return version{}, fmt.Errorf("%q: want a stable v%d.N.P", s, major)
	}

	return v, nil
}

// resolve creates the scratch module in work, requiring Kubernetes and kine
// together so one version selection covers both, and returns each downloaded
// upstream module by path plus Kubernetes's go directive.
func resolve(
	ctx context.Context, work string, kube, kine version, roots []string,
) (map[string]downloadedModule, string, error) {
	kubeMod, err := download(ctx, work, kubeSource.module, kube)
	if err != nil {
		return nil, "", err
	}

	kineMod, err := download(ctx, work, kineSource.module, kine)
	if err != nil {
		return nil, "", err
	}

	raw, err := os.ReadFile(kubeMod.GoMod)
	if err != nil {
		return nil, "", fmt.Errorf("read upstream go.mod: %w", err)
	}

	kubeGo, staging, err := parseUpstreamGoMod(raw)
	if err != nil {
		return nil, "", err
	}

	raw, err = os.ReadFile(kineMod.GoMod)
	if err != nil {
		return nil, "", fmt.Errorf("read kine go.mod: %w", err)
	}

	kineGo, _, err := parseUpstreamGoMod(raw)
	if err != nil {
		return nil, "", err
	}

	// The scratch module must satisfy both upstreams; only Kubernetes's
	// directive is carried over to petri's go.mod.
	workGo := kubeGo
	if goVersionLess(workGo, kineGo) {
		workGo = kineGo
	}

	stagingVersion := fmt.Sprintf("v0.%d.%d", kube.Minor, kube.Patch)
	require := []string{kubeSource.module + " " + kube.String(), kineSource.module + " " + kine.String()}

	goMod := renderWorkGoMod(workGo, require, staging, stagingVersion)
	if err = os.WriteFile(filepath.Join(work, "go.mod"), goMod, 0o600); err != nil {
		return nil, "", fmt.Errorf("write work go.mod: %w", err)
	}

	var main strings.Builder

	main.WriteString("package main\n\nimport (\n")

	for _, r := range roots {
		fmt.Fprintf(&main, "\t_ %q\n", r)
	}

	main.WriteString(")\n\nfunc main() {}\n")

	if err = os.WriteFile(filepath.Join(work, "main.go"), []byte(main.String()), 0o600); err != nil {
		return nil, "", fmt.Errorf("write work main.go: %w", err)
	}

	if _, err = scratchCmd(ctx, work, []string{modModEnv}, "mod", "tidy"); err != nil {
		return nil, "", err
	}

	return map[string]downloadedModule{kubeSource.module: kubeMod, kineSource.module: kineMod}, kubeGo, nil
}

func download(ctx context.Context, work, module string, v version) (downloadedModule, error) {
	out, err := scratchCmd(ctx, work, nil, "mod", "download", "-json", module+"@"+v.String())

	// go mod download -json reports its failures in the output, not on stderr.
	var mod downloadedModule
	if jsonErr := json.Unmarshal(out, &mod); jsonErr != nil {
		return downloadedModule{}, errors.Join(err, fmt.Errorf("decode go mod download output: %w", jsonErr))
	}

	if mod.Error != "" {
		return downloadedModule{}, fmt.Errorf("download %s@%s: %s", module, v, mod.Error)
	}

	if err != nil {
		return downloadedModule{}, err
	}

	return mod, nil
}

// writeTree writes src's mirrored packages, the upstream LICENSE and
// MIRROR.json into dst, replacing anything already there. Imports of every
// mirrored upstream are rewritten, so mirrors may import each other.
func writeTree(dst string, src source, upstream downloadedModule, pkgs map[string]*pkgFiles, m manifest) error {
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("clear %s: %w", dst, err)
	}

	for _, p := range pkgs {
		rel := strings.TrimPrefix(strings.TrimPrefix(p.ImportPath, src.module), "/")

		for f := range p.Files {
			if err := mirrorFile(p, f, filepath.Join(dst, filepath.FromSlash(rel), filepath.FromSlash(f))); err != nil {
				return err
			}
		}
	}

	license, err := os.ReadFile(filepath.Join(upstream.Dir, "LICENSE"))
	if err != nil {
		return fmt.Errorf("read upstream LICENSE: %w", err)
	}

	if err = writeFile(filepath.Join(dst, "LICENSE"), license); err != nil {
		return err
	}

	return m.write(filepath.Join(dst, manifestName))
}

// applyPatches applies src's patches to its staged tree. A patch that no longer
// applies fails the sync: it must be rebased onto the new upstream, or dropped
// when upstream made it unnecessary.
func applyPatches(ctx context.Context, repo string, src source) error {
	patches, err := filepath.Glob(filepath.Join(src.patchDir(repo), "*.patch"))
	if err != nil {
		return fmt.Errorf("list %s patches: %w", src.name, err)
	}

	staged, err := filepath.Rel(repo, src.stagedDir(repo))
	if err != nil {
		return fmt.Errorf("locate staged %s mirror: %w", src.name, err)
	}

	for _, p := range patches {
		if _, err = gitCmd(ctx, repo, "apply", "--directory="+filepath.ToSlash(staged), p); err != nil {
			return fmt.Errorf("patch %s no longer applies to the %s mirror; rebase or drop it: %w",
				filepath.Base(p), src.name, err)
		}
	}

	return nil
}

// mirrorFile copies file f of package p to path, rewriting the imports of every
// mirrored upstream in Go files.
func mirrorFile(p *pkgFiles, f, path string) error {
	data, err := os.ReadFile(filepath.Join(p.Dir, filepath.FromSlash(f)))
	if err != nil {
		return fmt.Errorf("read %s/%s: %w", p.ImportPath, f, err)
	}

	if strings.HasSuffix(f, ".go") {
		for _, s := range sources {
			data, err = rewriteImports(f, data, s.module, s.mirrorModule())
			if err != nil {
				return err
			}
		}
	}

	return writeFile(path, data)
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

// swapAndPin moves every staged tree into place and pins go.mod. On failure the
// previous trees, go.mod and go.sum are restored.
func swapAndPin(ctx context.Context, repo, work, upstreamGo string) error {
	for _, src := range sources {
		if err := os.RemoveAll(src.retiredDir(repo)); err != nil {
			return fmt.Errorf("clear %s: %w", src.retiredDir(repo), err)
		}
	}

	backup, err := backupFiles(repo, "go.mod", "go.sum")
	if err != nil {
		return err
	}

	var installed []source

	// rollback puts back the previous tree of every source swapped so far.
	rollback := func() error {
		var errs []error

		for _, src := range installed {
			live := filepath.Join(repo, filepath.FromSlash(src.dir))
			errs = append(errs, os.RemoveAll(live), restoreRetired(src.retiredDir(repo), live))
		}

		return errors.Join(errs...)
	}

	for _, src := range sources {
		live := filepath.Join(repo, filepath.FromSlash(src.dir))

		if err = os.Rename(live, src.retiredDir(repo)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return errors.Join(fmt.Errorf("retire old %s mirror: %w", src.name, err), rollback())
		}

		if err = os.Rename(src.stagedDir(repo), live); err != nil {
			return errors.Join(
				fmt.Errorf("install new %s mirror: %w", src.name, err),
				restoreRetired(src.retiredDir(repo), live),
				rollback(),
			)
		}

		installed = append(installed, src)
	}

	if err = pinGoMod(ctx, repo, work, upstreamGo); err != nil {
		return errors.Join(fmt.Errorf("pin go.mod: %w", err), rollback(), restoreFiles(repo, backup))
	}

	for _, src := range sources {
		if err = os.RemoveAll(src.retiredDir(repo)); err != nil {
			return fmt.Errorf("remove old %s mirror: %w", src.name, err)
		}
	}

	return nil
}

// restoreRetired moves a retired tree back to live, if there was one.
func restoreRetired(retired, live string) error {
	if err := os.Rename(retired, live); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("restore %s: %w", live, err)
	}

	return nil
}

// pinGoMod requires, in the repository, every module version the scratch
// module selected, raises the go directive to Kubernetes's if it is newer, and
// tidies. MVS keeps any higher version petri's own dependencies need. kine's
// go directive is deliberately not carried over: petri should demand no newer
// Go than Kubernetes does.
func pinGoMod(ctx context.Context, repo, work, upstreamGo string) error {
	out, err := scratchCmd(ctx, work, []string{modModEnv}, "list", "-m", "-json", "all")
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

		if mod.Main || mirrored(mod.Path) {
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

// scratchCmd runs the go command in the scratch module, which may need a newer
// toolchain than the one running: see autoToolchainEnv.
func scratchCmd(ctx context.Context, work string, env []string, args ...string) ([]byte, error) {
	return goCmd(ctx, work, append(slices.Clone(env), autoToolchainEnv), args...)
}

// goCmd runs the go command in dir with GOWORK=off plus env, returning stdout,
// even on failure.
func goCmd(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GOWORK=off"), env...)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("go %s (in %s): %w\n%s", strings.Join(args, " "), dir, err, stderr.String())
	}

	return out, nil
}
