# AGENTS.md

Guidance for coding agents working in this repository. Humans: see
[CONTRIBUTING.md](CONTRIBUTING.md); the rules are the same.

## What this is

petri runs a real Kubernetes control plane (kube-apiserver on kine's in-memory
store, upstream controllers, kube-scheduler, simulated Nodes) inside `go test`.
Read [docs/design.md](docs/design.md) before changing behaviour: it records
the decisions and why they were made.

## Layout

| Path | What |
|---|---|
| `*.go` (root) | Package `petri`: the public API. |
| `controllerruntime/` | Separate Go module: Manager helper and envtest drop-in. Depends on petri, which the root `go.work` resolves to the working tree. |
| `internal/controllers/` | petri's controller manager, registry, kubelet simulator, scheduler wiring. |
| `internal/kubernetes/`, `internal/kine/` | Mirrored upstream code. **Generated. Never edit.** |
| `hack/mirror/` | The mirror tool (`make mirror`), its patches in `hack/mirror/patches/`. |
| `internal/leakcheck/` | Goroutine-leak test for start/stop cycles. |
| `.github/scripts/` | Workflow scripts, each with a `_test.sh` scenario test. |
| `docs/` | Design decisions and the release process. |

## Commands

```sh
make test    # go test -race, both modules
make lint    # golangci-lint, both modules (strict config: fix, don't silence)
make fmt     # formatters
make mirror  # regenerate the mirrors at the recorded versions
```

- `-race` builds are heavy (about 1.5 GiB per test process, 3 GB to link). On
  a small machine, use `go test -race -p 1 -parallel 4 ./...`, or run one
  test with `-run`.
- The first build compiles about 350k lines of mirrored code; it is cached
  afterwards.
- Run the scripts' tests after touching `.github/scripts/`:
  `.github/scripts/<name>_test.sh`.

## Rules

- **Never edit `internal/kubernetes` or `internal/kine`.** Add a root to
  `MIRROR.json` and run `make mirror`; for an unavoidable change, add a small
  patch under `hack/mirror/patches/`. CI fails on any drift.
- **The public API never imports `k8s.io/kubernetes`**, and go.mod never gets
  `replace` directives.
- **Do not raise the `go` directive** beyond what the embedded Kubernetes
  release requires; `make mirror` owns it.
- **Versions are `v0.<Kubernetes minor>.<patch>`** (`v0.37.x` for Kubernetes
  1.37, matching `k8s.io/client-go`). Never propose `v0.1.0`, `v1.0.0` or any
  minor that does not match `KubernetesVersion` in `version.go`.
- **Never create or push tags, and never edit `CHANGELOG.md` or
  `.release-please-manifest.json`.** release-please owns them; releases happen
  by merging its release PR. See [docs/releasing.md](docs/releasing.md).
- **Commits follow Conventional Commits** (`feat:`, `fix:`, `perf:`,
  `build:`, `refactor:`, `docs:`, `test:`, `ci:`, `chore:`). The changelog and
  the release trigger are built from them: `test`, `ci` and `chore` never
  release, everything else does.
- Never commit planning documents or scratch specs.
- Update the README when user-facing behaviour changes, and
  `docs/design.md` when a decision changes.

## Writing tests

- Prefer the shared plane (`petri.Start(t)` and a namespace per test). Use a
  dedicated plane (`petri.New`) only for what the shared one cannot do:
  Nodes, the scheduler, `WithoutDefaultControllers`, or cluster-wide state a
  test must own.
- Every test and subtest calls `t.Parallel()` unless it cannot.
- **Give each parallel subtest its own deadline.** A parallel subtest only
  starts after its parent returns and a `-parallel` slot frees up, so a
  context with a timeout created in the parent may already be expired. Derive
  timeouts from the subtest's own `t.Context()`.
- Wait with `petri.WaitFor` and friends, never with `time.Sleep`.
- Tests must leave no goroutines behind once a plane stops;
  `internal/leakcheck` checks start/stop cycles.
