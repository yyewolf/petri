# Contributing to petri

## Prerequisites

- Go 1.26 or newer
- [golangci-lint](https://golangci-lint.run/) v2.14 or newer on your `PATH`
- GNU make

## Common tasks

```sh
make test   # go test -race ./..., for both modules
make lint   # golangci-lint config verify + run, for both modules
make fmt    # format with goimports and golines
make tidy   # go mod tidy, for both modules
make mirror # regenerate internal/kubernetes and internal/kine
```

`make` with no target lists them.

## The Go workspace

The repository root carries a committed `go.work` listing both modules, so the
`controllerruntime` sub-module builds against the working tree instead of a
published petri version. It also replaces the final pre-split
`google.golang.org/genproto` with an empty stand-in: nothing imports its
packages any more, but cockroachdb/errors still requires it, and the workspace
build list does not prune it away like a plain module build does. Builds
without the workspace are unaffected.

## Branches and versions

- `main` tracks the newest supported Kubernetes minor.
- `release-1.N` branches carry fixes for older minors.
- Tags are `v0.<k8s minor>.<patch>`, for example `v0.37.2` for Kubernetes 1.37.
  The patch number increments for any petri change.
- The `controllerruntime` sub-module is tagged `controllerruntime/v0.<k8s
  minor>.<patch>` together with each petri tag, and its go.mod requires the
  matching petri tag.
- `KubernetesVersion` in `version.go` must match the release line. The release
  workflow refuses tags that do not match it; for `controllerruntime/` tags it
  checks the go.mod requirement instead.

## Releasing

1. Make sure `controllerruntime/go.mod` requires the petri version being
   released, and that `make test` and `make lint` pass for both modules.
2. Push both tags together:

   ```sh
   git tag v0.37.2 && git tag controllerruntime/v0.37.2
   git push origin v0.37.2 controllerruntime/v0.37.2
   ```

The release workflow drafts a GitHub release for each tag.

## The mirrors

`hack/mirror` copies upstream code into the repository with import paths
rewritten:

- `internal/kubernetes`: kube-apiserver and controller code from
  `k8s.io/kubernetes`.
- `internal/kine`: kine's memory driver and etcd gRPC server from
  `github.com/k3s-io/kine`. Depending on kine directly would raise every
  user's Go floor to whatever kine's go.mod declares, which is usually a newer
  patch than Kubernetes requires.

Never edit either by hand; CI regenerates them and fails on any difference.
Each tree's `MIRROR.json` lists the upstream packages petri needs (`roots`)
and records the mirrored version. Add a root there, not a file.

The only allowed differences from upstream are the patches in
`hack/mirror/patches/<kubernetes|kine>/`: unified diffs with paths relative
to the mirror directory, applied in name order after the import rewrite. Keep
them small and say in the patched code why it differs. Today one patch
replaces kine's import of etcd's server module, used for a single constant,
which keeps etcd's server, raft and bbolt out of every user's graph. When an
update makes a patch fail, `make mirror` stops and names it: rebase it onto
the new upstream, or drop it if upstream made it unnecessary.
`TestBuildGraphStaysLean` fails if a module petri keeps out, such as etcd's
server or one of kine's storage backends, is built again.

- `make mirror` regenerates both at the versions their `MIRROR.json` records.
- `make mirror K8S_VERSION=v1.37.2` moves to another Kubernetes release and
  pins go.mod to the module versions that release uses.
- `make mirror KINE_VERSION=v0.18.0` moves kine. Both upstreams are resolved
  together, so go.mod gets one consistent version selection.

petri's `go` directive follows Kubernetes': `make mirror` raises it when a
Kubernetes release needs a newer Go, and never for kine.

The Mirror workflow runs daily. It opens a PR for each new patch release on
`main` and every `release-1.N` branch, for Kubernetes and for kine within its
current minor; when both have one, they share the PR. Moving kine to a new
minor is left to a human, since it usually follows a Kubernetes minor. When a new Kubernetes minor ships, it
cuts `release-1.N` from `main` and opens a PR moving `main` to the new minor;
the PR also bumps the `controllerruntime` sub-module's k8s.io and
controller-runtime requirements when a matching controller-runtime is out, and
opens as a draft with a note when it is not.
Each target branch has at most one mirror PR, from `mirror/<branch>`: a newer
release updates it in place, and runs that would push identical content do
not push. A PR closed without merging comes back only once the release or its
target branch changes. Updates that fail `make mirror` or the tests open
as drafts.
It needs a `MIRROR_TOKEN` secret: a fine-grained personal access token for
this repository with Contents and Pull requests read/write, because PRs opened
with the default token do not trigger CI.

## Pull requests

- The CI `test` and `lint` jobs must pass.
- Add or update tests for behaviour changes.
- Update the README when user-facing behaviour changes.
- For fixes that apply to an older Kubernetes minor, say so in the PR so it can
  be backported to the matching `release-1.N` branch.
