# petri

Real Kubernetes control plane for `go test`. No binaries, no Docker.

> [!WARNING]
> petri is pre-alpha. The API server works; built-in controllers and fake
> kubelets are not implemented yet, and the API will change.

## Why

Testing a controller today usually means controller-runtime's envtest. envtest
needs etcd and kube-apiserver binaries downloaded with `setup-envtest` and a
`KUBEBUILDER_ASSETS` variable pointing at them. Without that setup, `go test`
fails.

envtest also runs only an API server. No controller-manager or scheduler runs,
so Deployments never create ReplicaSets, Pods never run, and ownerReference
garbage collection never fires.

petri runs the real kube-apiserver inside the test process as a Go library,
over an in-memory store, so `go test ./...` works on a fresh checkout and
nothing is written to disk. Opt-in built-in controllers and fake kubelets are
planned next.

## Usage

```go
func TestReconcile(t *testing.T) {
	env := petri.Start(t)  // one control plane shared by the test binary
	cfg := env.Config()    // *rest.Config with admin rights, same as envtest
	cs := env.Client()     // shared admin *kubernetes.Clientset
	ns := env.Namespace(t) // fresh namespace, deleted on cleanup

	// run your reconciler against cfg, check results with cs, all in ns
}
```

`petri.New(ctx)` starts a dedicated control plane when a test needs one to
itself. Call `Stop` when done. Starting one takes about two seconds, and
stopping it releases all of its goroutines.

The API server is upstream kube-apiserver code, so you get upstream
defaulting, validation, strategic merge patch, RBAC, and ClusterIP allocation.
Service accounts are not provisioned and the `ServiceAccount` admission plugin
is disabled, as in envtest.

The first `go test` compiles the API server and takes a few minutes. Later
runs use the build cache.

## Versioning

petri versions follow the `k8s.io` staging modules. Use the petri minor that
matches your `k8s.io/client-go` minor:

| petri     | Kubernetes | k8s.io/client-go |
|-----------|------------|------------------|
| `v0.37.x` | 1.37       | `v0.37.x`        |

`main` tracks the newest supported Kubernetes minor. Older minors are
maintained on `release-1.N` branches. `petri.KubernetesVersion` reports the
minor a build targets.

## Limitations

- The API server is linked into your test binary, so its version always
  equals your `client-go` minor. Testing a client against an older or newer
  API server (version-skew testing) is out of scope. envtest can do that
  because its binaries are separate from your code.
- No controllers run yet. Deleted namespaces stay `Terminating` with their
  contents, Deployments do not create ReplicaSets, and garbage collection does
  not run.
- kube-apiserver and kine log through klog and logrus, which petri redirects
  process-wide into a buffer that it prints when startup fails. Set
  `PETRI_VERBOSE=1` to also see them on stderr. If your code logs through klog
  or logrus, its output is redirected too.
- Storage is kine's in-memory driver, not etcd. It behaves like etcd for
  everything kube-apiserver does, but etcd-specific edge cases such as
  compaction timing may differ.

## Design constraints

petri will never require you to import `k8s.io/kubernetes`. That module pins
its staging modules (`k8s.io/api`, `k8s.io/apiserver`, and others) at `v0.0.0`
and resolves them with `replace` directives, which Go ignores in dependencies.
Importing it would force every user to copy those directives into their own
go.mod, so petri only depends on modules that resolve without them.

The API server code is mirrored from `k8s.io/kubernetes` into
`internal/kubernetes` with its import paths rewritten, so it resolves against
the published staging modules.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
