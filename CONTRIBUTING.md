# Contributing to petri

## Prerequisites

- Go 1.26 or newer
- [golangci-lint](https://golangci-lint.run/) v2 on your `PATH`
- GNU make

## Common tasks

```sh
make test   # go test -race ./...
make lint   # golangci-lint run
make fmt    # format with gofumpt and goimports
make tidy   # go mod tidy
```

`make` with no target lists them.

## Branches and versions

- `main` tracks the newest supported Kubernetes minor.
- `release-1.N` branches carry fixes for older minors.
- Tags are `v0.<k8s minor>.<patch>`, for example `v0.37.2` for Kubernetes 1.37.
  The patch number increments for any petri change.
- `KubernetesVersion` in `version.go` must match the release line. The release
  workflow refuses tags that do not match it.

## Pull requests

- The CI `test` and `lint` jobs must pass.
- Add or update tests for behaviour changes.
- Update the README when user-facing behaviour changes.
- For fixes that apply to an older Kubernetes minor, say so in the PR so it can
  be backported to the matching `release-1.N` branch.
