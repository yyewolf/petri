# petri

Real Kubernetes control plane for `go test`. No binaries, no Docker.

> [!WARNING]
> petri is pre-alpha. The control plane is not implemented yet, and the API
> will change.

## Why

Testing a controller today usually means controller-runtime's envtest. envtest
needs etcd and kube-apiserver binaries downloaded with `setup-envtest` and a
`KUBEBUILDER_ASSETS` variable pointing at them. Without that setup, `go test`
fails.

envtest also runs only an API server. No controller-manager or scheduler runs,
so Deployments never create ReplicaSets, Pods never run, and ownerReference
garbage collection never fires.

petri aims to run the API server inside the test process as a Go library, so
`go test ./...` works on a fresh checkout. Opt-in built-in controllers and fake
kubelets are planned after that.

## Planned API

Planned, not implemented:

```go
func TestReconcile(t *testing.T) {
	env := petri.Start(t)  // one control plane per package
	cfg := env.Config()    // *rest.Config, same as envtest
	ns := env.Namespace(t) // fresh namespace, deleted on cleanup

	// run your reconciler against cfg in ns
}
```

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

The API server is linked into your test binary, so its version always equals
your `client-go` minor. Testing a client against an older or newer API server
(version-skew testing) is out of scope. envtest can do that because its
binaries are separate from your code.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
