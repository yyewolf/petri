# Contributing to petri

## Prerequisites

- Go 1.26 or newer
- [golangci-lint](https://golangci-lint.run/) v2.14 or newer on your `PATH`
- GNU make

## Common tasks

```sh
make test   # go test -race ./...
make lint   # golangci-lint config verify + run
make fmt    # format with goimports and golines
make tidy   # go mod tidy
make mirror # regenerate internal/kubernetes
```

`make` with no target lists them.

## Branches and versions

- `main` tracks the newest supported Kubernetes minor.
- `release-1.N` branches carry fixes for older minors.
- Tags are `v0.<k8s minor>.<patch>`, for example `v0.37.2` for Kubernetes 1.37.
  The patch number increments for any petri change.
- `KubernetesVersion` in `version.go` must match the release line. The release
  workflow refuses tags that do not match it.

## The Kubernetes mirror

`internal/kubernetes` is upstream kube-apiserver code copied from
`k8s.io/kubernetes` by `hack/mirror`, with import paths rewritten. Never edit
it by hand; CI regenerates it and fails on any difference.

- `make mirror` regenerates it at the version in `internal/kubernetes/MIRROR.json`.
- `make mirror K8S_VERSION=v1.37.2` moves to another release and pins go.mod
  to the module versions that release uses.

The Mirror workflow runs daily. It opens a PR for each new patch release on
`main` and every `release-1.N` branch. When a new Kubernetes minor ships, it
cuts `release-1.N` from `main` and opens a PR moving `main` to the new minor.
It needs a `MIRROR_TOKEN` secret: a fine-grained personal access token for
this repository with Contents and Pull requests read/write, because PRs opened
with the default token do not trigger CI.

## Pull requests

- The CI `test` and `lint` jobs must pass.
- Add or update tests for behaviour changes.
- Update the README when user-facing behaviour changes.
- For fixes that apply to an older Kubernetes minor, say so in the PR so it can
  be backported to the matching `release-1.N` branch.
