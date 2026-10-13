# petri

Real Kubernetes control plane for `go test`. No binaries, no Docker.

> [!WARNING]
> petri is pre-alpha. The API server, the built-in controllers, kube-scheduler
> and simulated Nodes work; the API will change.

## Why

Testing a controller today usually means controller-runtime's envtest. envtest
needs etcd and kube-apiserver binaries downloaded with `setup-envtest` and a
`KUBEBUILDER_ASSETS` variable pointing at them. Without that setup, `go test`
fails.

envtest also runs only an API server. No controller-manager runs, so
Deployments never create ReplicaSets and ownerReference garbage collection
never fires.

petri runs the real kube-apiserver inside the test process as a Go library,
over an in-memory store, so `go test ./...` works on a fresh checkout and
nothing is written to disk unless a test asks for a webhook serving
certificate, which is the only disk use. It runs upstream built-in controllers
too: the namespace controller and the garbage collector by default, twelve
more on demand, and kube-scheduler over simulated Nodes, so Pods actually
run.

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
The `ServiceAccount` admission plugin is disabled, as in envtest, so Pod
creation in a fresh namespace does not race against ServiceAccount creation;
the ServiceAccount controller fills that gap once enabled, keeping a `default`
ServiceAccount in every namespace.

Built-in controllers run by default. Every control plane runs the namespace
controller and the garbage collector, and `Start` and `New` return only once
their informers are synced, so an object created right after startup is seen
by both. A deleted namespace really goes away, and deleting an owner deletes
its dependents, with foreground, background and orphan propagation as on a
real cluster. Resources served by a CRD installed after startup are collected
too.

The shared control plane always runs the defaults. A dedicated one can start
without them:

```go
cp, err := petri.New(ctx, petri.WithoutDefaultControllers())
```

Deleted namespaces then stay `Terminating` with their contents, and deleting
an owner leaves its dependents behind: the semantics of envtest.

The remaining controllers are opt-in per plane:

```go
env.EnableControllers(t, petri.DeploymentController, petri.ReplicaSetController)
```

`EnableControllers` is idempotent, safe to call from parallel tests, and
returns once the enabled controllers' informers are synced. Controllers stay
enabled once enabled, and enabling one never enables another: the Deployment
controller alone creates ReplicaSets but no Pods, because the ReplicaSet
controller is off. A plane started with `WithoutDefaultControllers` can enable
controllers too.

The workload controllers — `petri.DeploymentController`,
`petri.ReplicaSetController`, `petri.StatefulSetController`,
`petri.DaemonSetController`, `petri.JobController`, `petri.CronJobController`
and `petri.TTLAfterFinishedController` — are available as one slice from
`petri.WorkloadControllers()`. The others are `petri.EndpointSliceController`,
`petri.ServiceAccountController`, `petri.ServiceAccountTokenController`,
`petri.ResourceQuotaController` and `petri.DisruptionController`.
`petri.ServiceAccountTokenController` signs legacy service-account token
Secrets with the plane's in-memory key, so a TokenReview authenticates them.
`petri.ResourceQuotaController` keeps a quota's `status.used` current; quota
admission is on, so hard limits are enforced even with the controller off.
`petri.DisruptionController` fills in PodDisruptionBudget statuses; with no
ready Pods, every budget settles on zero `disruptionsAllowed`.

The first `go test` compiles the API server and its controller code and takes
a few minutes; the controllers add about 33,000 lines and a couple of seconds
to that first compile. Later runs use the build cache and are unchanged.

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

### Nodes and scheduling

A dedicated control plane can have Nodes, simulated as kwok does: no
containers run, but Pods placed on a Node go `Running` and `Ready` at once,
with an IP from the Node's PodCIDR, and deleted Pods go away at once.

```go
cp, _ := petri.New(ctx)
gpu := cp.AddNode(t, petri.NodeOptions{
	Name:   "gpu-a",
	Labels: map[string]string{"pool": "gpu"},
	Taints: []corev1.Taint{{Key: "gpu", Effect: corev1.TaintEffectNoSchedule}},
})
```

`NodeOptions` also sets capacity (32 CPUs, 256Gi and 110 Pods by default) and
can create the Node cordoned. The Node is deleted when the test finishes, and
its handle changes it the way an operator or a failing kubelet would:
`Taint`, `Untaint`, `Cordon`, `Uncordon`, `Drain` (evictions through the
Eviction API, retried while a PodDisruptionBudget refuses them), `Delete`
and `SetReady`. A Node set not Ready gets the not-ready taint and its
kubelet freezes: bound Pods stay Pending and deleted Pods stay `Terminating`
until it is Ready again. Nothing is evicted automatically: petri runs no
node lifecycle or taint-eviction controller.

Pods get onto Nodes one of two ways, chosen per control plane.

**kube-scheduler.** Enable `petri.SchedulerController` and upstream
kube-scheduler places Pods by requests, taints and tolerations, node
selectors, affinities and topology spread, as on a cluster:

```go
cp.EnableControllers(t, petri.SchedulerController, petri.DeploymentController, petri.ReplicaSetController)
// a Deployment created now becomes Available
```

**By hand, like a mock.** Without the scheduler, Pods stay Pending until the
test places them. `ExpectPod` waits for a Pending Pod and hands it over once;
`Bind` places it through the same `pods/binding` call the scheduler makes:

```go
pod := cp.ExpectPod(t, petri.InNamespace(ns), petri.Labeled("app", "web"))
cp.Bind(t, pod, gpu)
```

Declarative expectations claim Pods as they arrive, gomock-style, and fail
the test when it finishes if they claimed too few; `StrictPods` also fails
it for every Pod nobody expected:

```go
cp.ExpectPods(t, petri.OwnedBy(rs)).Times(3).BindTo(gpu).Wait(t)
cp.StrictPods(t)
```

Matchers are `InNamespace`, `Labeled`, `OwnedBy` and `PodMatching` for
anything else. Expectations claim Pods before `ExpectPod` does, in the order
they were registered. Mixing the two ways on one plane fails the test.

Whoever placed it, a running Pod is the test's to drive: `CompletePod` and
`FailPod` finish it (that is how Jobs complete), `SetPodReady` flips its
readiness, and `Evict` goes through the Eviction API, so PodDisruptionBudgets
apply. The simulator never overrides what the test set.

Nodes belong to the whole cluster, so the shared control plane refuses all
of this: use `petri.New`.

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
`Scheme`, `AttachControlPlaneOutput` and `DisableDefaultControllers`. Fields
that would change what a test tests fail `Start`: `UseExistingCluster` and
`BinaryAssetsDirectory` (petri runs no binaries, so setup-envtest can be
dropped), and `CRDInstallOptions.CleanUpAfterUse` (CRDs are never deleted).
Unlike envtest, a missing or manifest-less CRD path always fails;
`ErrorIfCRDPathMissing` is accepted for compatibility.

Unlike envtest, the environment runs petri's default controllers: deleted
namespaces really go away and deleting an owner cascades to its dependents.
Expect one consequence: a namespace whose custom resource carries a finalizer
nobody removes stays `Terminating`, because the namespace waits for the stuck
object. That is correct namespace and garbage-collection behaviour, not a
petri bug; the `controllerruntime/envtest` package documentation covers it.
Remove the finalizer in the test's cleanup, or set `DisableDefaultControllers`
for strict envtest parity, which keeps deleted namespaces `Terminating`. The
environment's other controllers are opt-in through
`testEnv.EnableControllers(t, petri.DeploymentController, ...)`, which
forwards to the control plane.

## Resource usage

One control plane takes about 300 MiB, growing with what tests store: less
than envtest's three processes, which take about 350 MiB together. Measured
on Kubernetes 1.37, idle and then after 5,000 writes of 1 KiB ConfigMaps:

| Build      | petri               | envtest             |
|------------|---------------------|---------------------|
| plain      | 300 → 370 MiB       | 350 → 420 MiB       |
| `-race`    | 1.0 → 1.5 GiB       | 400 → 480 MiB       |

The race detector instruments everything compiled into the test binary. With
envtest that is only your code, since kube-apiserver and etcd are separate
binaries; with petri it is the whole API server too, and no setting changes
that. Linking a test binary that imports petri also takes about 3 GiB of RAM,
against about 1 GiB with envtest.

`go test ./...` builds and runs packages in parallel, so these costs add up
per package that starts a control plane. On small CI runners, run `-race`
suites with `-p 1` or `-p 2`, or keep `-race` for the packages that do not
start one.

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
[docs/releasing.md](docs/releasing.md) describes how releases are made.

## Limitations

- The API server is linked into your test binary, so its version always
  equals your `client-go` minor. Testing a client against an older or newer
  API server (version-skew testing) is out of scope. envtest can do that
  because its binaries are separate from your code.
- Pods only run on the simulated Nodes of a dedicated control plane. On the
  shared one, or on a plane without Nodes, Pods stay `Pending` and the
  workload controllers stop where node state would be needed: Jobs never
  finish, StatefulSets with the default `OrderedReady` policy stop at Pod 0,
  DaemonSets create no Pods, EndpointSlices have no endpoints, and
  PodDisruptionBudgets settle on zero `disruptionsAllowed`. A namespace that
  contained Pods can linger `Terminating` because no kubelet confirms Pod
  deletion, as upstream. These are expectations on a nodeless plane, not
  bugs.
- Simulated Nodes run no containers: probes, images, volumes, resource usage
  and restarts are not simulated, readiness gates are ignored, and Pods stay
  `Running` until the test finishes them. The Node lifecycle is the test's to
  drive: no heartbeats or Leases, no taint-based eviction.
- CronJob schedules by wall clock, so tests that need a Job now create one
  from the CronJob's `spec.jobTemplate` themselves.
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

The API server and the upstream controller code are mirrored from
`k8s.io/kubernetes` into `internal/kubernetes` with their import paths
rewritten, so they resolve against the published staging modules.

petri asks for no newer Go than the Kubernetes release it embeds. kine is
mirrored into `internal/kine` for that reason, and only its in-memory driver
and etcd gRPC server are included, so none of kine's other storage backends
(SQL drivers, NATS, S3) end up in your module graph.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
