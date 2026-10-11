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
nothing is written to disk unless a test asks for a webhook serving
certificate, which is the only disk use. Opt-in built-in controllers and fake
kubelets are planned next.

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
stopping it releases all of its goroutines. `cp.NewNamespace(ctx)` creates a
namespace without a test-name prefix for callers that manage their own
lifecycle; the caller owns deletion.

The API server is upstream kube-apiserver code, so you get upstream
defaulting, validation, strategic merge patch, RBAC, and ClusterIP allocation.
Service accounts are not provisioned and the `ServiceAccount` admission plugin
is disabled, as in envtest.

The first `go test` compiles the API server and takes a few minutes. Later
runs use the build cache.

## Helpers

### Waiting

Waits follow one object with a list-then-watch, so a change made just before
the wait is never missed, and they reconnect after watch expiry. On timeout the
failure shows the last observed state.

```go
petri.WaitFor(t, cs.AppsV1().Deployments(ns), "web",
	petri.StatusCondition[*appsv1.Deployment]("Available", metav1.ConditionTrue))

_, err := petri.WaitUntil(ctx, cs.CoreV1().Pods(ns), "sidecar",
	petri.Deleted[*corev1.Pod](), petri.WithTimeout(time.Minute))
```

Every typed client-go resource client works directly; for resources without a
typed client, wrap a dynamic `ResourceInterface` with `petri.DynamicResource`.
`WaitUntil` returns the last observed object (nil when it is gone) and an
error, for Ginkgo and Gomega suites. `petri.RetryUntil` / `petri.Retry` is the
escape hatch for waiting on anything that is not one object.

Ready-made conditions are `Exists`, `Deleted` and `StatusCondition`, which
reads `status.conditions[type]` for any condition shape with string type and
status fields, including objects fetched through the dynamic client. Anything
else is a condition function away.

### CRDs

```go
env.InstallCRDs(t, "testdata/crds", myEmbeddedFS, crdObject)
```

Sources are file and directory paths, an `fs.FS` (so `//go:embed` works), and
CRD objects. YAML and JSON manifests are read, including multi-document files;
documents that are not CRDs are skipped, as envtest does. `InstallCRDs`
returns only when every CRD is `Established` and its resources appear in
discovery, so a RESTMapper built right afterwards resolves them.

CRDs are installed per test and installing again is safe, so parallel tests on
the shared control plane can install the same manifests. A CRD that already
exists with an equivalent spec, compared after the server's defaulting, is
left alone; a different spec fails with a readable diff. CRDs are never
deleted: other tests on the shared control plane may still be using them.

### Webhooks

Every control plane generates a webhook CA in memory at startup.

```go
certDir := env.ServingCertDir(t) // tls.crt/tls.key for 127.0.0.1 and localhost

env.InstallAdmissionWebhooks(t, petri.AdmissionWebhookOptions{
	Host: "127.0.0.1", Port: port, Namespace: ns,
}, "testdata/webhooks", mutatingConfig)
```

`ServingCertDir` writes a pair a controller-runtime webhook server loads
straight from `CertDir`; it is petri's only disk use and the directory dies
with the test. `WebhookCABundle` returns the CA for webhook manifests that
carry their own URL.

`InstallAdmissionWebhooks` takes the same kinds of sources as `InstallCRDs`,
rewrites each service-based webhook to the local server keeping its path, and
sets the CA as the caBundle. Configurations are installed under a unique
suffix, so parallel tests can install the same manifests, and are deleted when
the test finishes. Webhooks are scoped to `Namespace` by default, merged into
any selector the manifest already had; leave `Namespace` empty only for
webhooks that must see the whole cluster — on the shared control plane an
unscoped webhook fires for every test's objects.

`InstallConversionWebhooks` patches CRD conversion webhooks the same way. A
CRD applies to the whole control plane, so conversion webhooks are only
allowed on a dedicated control plane from `New`; the shared one refuses.

### controller-runtime

The `github.com/yyewolf/petri/controllerruntime` module wires petri into
controller-runtime:

```go
mgr := petricr.NewManager(t, cfg) // metrics off, scheme overridable
// attach controllers and webhooks to mgr ...
petricr.StartManager(t, mgr)      // returns once the caches synced

petricr.WaitFor(t, c, deployment, func(d *appsv1.Deployment) (bool, error) {
	return d.Status.ReadyReplicas > 0, nil
})
```

`StartManager` stops the manager on cleanup and fails the test when it exits
early; a control plane running a Manager and a webhook server releases all of
its goroutines.

### envtest drop-in

For a kubebuilder suite, changing only the import path from
`sigs.k8s.io/controller-runtime/pkg/envtest` to
`github.com/yyewolf/petri/controllerruntime/envtest` works:

```go
testEnv := &envtest.Environment{
	CRDDirectoryPaths:     []string{"testdata/crds"},
	WebhookInstallOptions: envtest.WebhookInstallOptions{Paths: []string{"testdata/webhooks"}},
}
cfg, err := testEnv.Start()
```

`Start` runs a dedicated control plane, installs the CRDs and webhook
configurations, and fills in `LocalServingHost`, `LocalServingPort` and
`LocalServingCertDir` for the suite's webhook server. `Stop` removes the
installed configurations and the certificate directory. The drop-in's
supported fields are `CRDDirectoryPaths`, `CRDs`, `ErrorIfCRDPathMissing`,
`CRDInstallOptions.Paths`, `CRDInstallOptions.CRDs`,
`WebhookInstallOptions.Paths`, `WebhookInstallOptions.MutatingWebhooks`,
`WebhookInstallOptions.ValidatingWebhooks`, `WebhookInstallOptions.LocalServingHost`,
`WebhookInstallOptions.LocalServingPort`, `WebhookInstallOptions.LocalServingCertDir`,
`Scheme` and `AttachControlPlaneOutput`. Fields that would change what a test
tests fail `Start`: `UseExistingCluster` and `BinaryAssetsDirectory` (petri
runs no binaries, so setup-envtest can be dropped), and
`CRDInstallOptions.CleanUpAfterUse` (CRDs are never deleted). Unlike envtest,
a missing or manifest-less CRD path always fails; `ErrorIfCRDPathMissing` is
accepted for compatibility.

## Versioning

petri versions follow the `k8s.io` staging modules. Use the petri minor that
matches your `k8s.io/client-go` minor:

| petri     | Kubernetes | k8s.io/client-go |
|-----------|------------|------------------|
| `v0.37.x` | 1.37       | `v0.37.x`        |

The `controllerruntime` sub-module is tagged `controllerruntime/v0.N.P`
together with each `v0.N.P` petri tag and requires it, carrying the matching
controller-runtime release.

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
