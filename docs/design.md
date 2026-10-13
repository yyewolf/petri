# petri design

This records the decisions petri is built on and why, so they are not
re-argued with every change. The README describes what petri does; this
describes the choices behind it. Change a decision here in the same PR that
changes the code.

## Goal

`go test ./...` on a fresh checkout runs a real Kubernetes control plane inside
the test process: no downloaded binaries, no Docker, nothing written to disk.
"Real" means upstream code doing upstream things: validation, defaulting,
strategic merge patch, admission, RBAC, ClusterIP allocation, controllers and
the scheduler. It is not a lookalike built from OpenAPI schemas.

petri started as a drop-in replacement for controller-runtime's envtest and
goes further where envtest stops: namespaces really go away, garbage
collection cascades, workload controllers run, and Pods can run on simulated
Nodes.

## Kubernetes version

- **One version per build, chosen through go.mod.** One test binary can link
  only one `k8s.io/apiserver`, and the API server relies on process globals,
  so there is no runtime version option. The server minor always equals the
  user's `k8s.io/client-go` minor. Version-skew testing, which envtest can do
  with separate binaries, is out of scope.
- **Versions follow the staging modules.** petri `v0.37.x` embeds Kubernetes
  1.37. `main` tracks the newest minor and older minors live on
  `release-1.N` branches. See [releasing.md](releasing.md).
- **The Go floor follows Kubernetes.** petri's `go` directive is never newer
  than the embedded Kubernetes release's. `make mirror` raises it only when
  Kubernetes does.

## Mirroring instead of importing

- **Users never import `k8s.io/kubernetes`.** Its go.mod pins the staging
  modules at `v0.0.0` and resolves them with `replace` directives, which Go
  ignores in dependencies, so every user would have to copy them. petri's own
  go.mod never needs `replace` either.
- **Upstream code is mirrored.** `hack/mirror` copies the packages petri needs
  from `k8s.io/kubernetes` into `internal/kubernetes`, and kine into
  `internal/kine`, with import paths rewritten so they build against the
  published staging modules. `MIRROR.json` lists the root packages and the
  mirrored version; the tool computes the closure for every supported
  platform and copies only non-test files.
- **Nobody edits the mirrors by hand.** CI regenerates them and fails on any
  difference. All adaptation lives in petri's own code, except small patches
  in `hack/mirror/patches/`, applied after the rewrite, when a mirrored import
  would drag a large module into every user's graph (today: etcd's server
  module, pulled in by kine for one constant). `TestBuildGraphStaysLean`
  guards the modules petri keeps out.
- **kine is mirrored too**, not imported, because its go.mod asks for a newer
  Go patch than Kubernetes does, which would raise every user's Go floor. Only
  its memory driver and etcd gRPC server are mirrored, so no SQL, NATS or S3
  backend reaches the user's module graph.
- **Upkeep is automated.** The Mirror workflow opens a PR for each new
  Kubernetes patch on every release line, and for the next minor on `main`.
  Lines upstream stops patching stop on their own.

## The control plane

- **Storage is kine's in-memory driver**, served on `127.0.0.1:0` through its
  etcd gRPC API. No etcd, no SQLite, nothing on disk. etcd-specific edge
  cases such as compaction timing may differ; storage sits behind the etcd API
  so it could be swapped without touching anything else.
- **kube-apiserver runs from the mirrored `cmd/kube-apiserver/app`,** set up
  the way upstream's test server does it, minus its test-only dependencies.
  Certificates and service-account keys are generated in memory.
- **Two kinds of plane.** `Start(t)` returns one control plane shared by the
  whole test binary, started on first use and torn down by process exit.
  `New(ctx)` starts a dedicated one that the test stops. Several dedicated
  planes can run concurrently and stay isolated, which is tested.
- **Isolation on the shared plane is by namespace.** Each test gets a unique
  namespace from `Namespace(t)` or `NewNamespace(ctx)`. Anything cluster-wide
  (CRDs, controllers) is additive on the shared plane: installing or enabling
  twice is safe, and nothing is ever removed, because other tests may rely on
  it.
- **Logs are captured.** klog and logrus are redirected process-wide into a
  buffer that is printed when startup fails; `PETRI_VERBOSE=1` also sends them
  to stderr. Feature gates stay at upstream defaults.

## Test helpers

- **`InstallCRDs` returns only when every CRD is Established and served by
  discovery,** so a RESTMapper built right after it resolves the new kinds. A
  CRD that already exists with an equivalent spec (compared after server-side
  defaulting) is left alone; a different spec fails the test with a diff.
- **Waits are watch-based,** list-then-watch from the object's
  resourceVersion, so a change made just before the wait is not missed.
- **Every helper has a `t`-taking form and a ctx/error form**, the second for
  frameworks like Ginkgo that do not hand out a `testing.TB`.
- **controller-runtime support is a separate module,**
  `github.com/yyewolf/petri/controllerruntime`, so petri itself does not
  depend on controller-runtime. It provides a Manager helper and an envtest
  drop-in. Webhooks are in scope, for both admission and CRD conversion.

## Controllers

- **petri runs its own small manager,** not kube-controller-manager. Only the
  controller packages are mirrored; the upstream binary would bring flags,
  cloud providers and every controller. One manager per plane owns shared
  informer factories and a discovery-backed RESTMapper. Controllers use the
  admin config with a distinct user agent, without leader election.
- **Namespace and garbage-collector controllers run by default,** on every
  plane. This differs from envtest on purpose: deleted namespaces go away and
  owners cascade. `WithoutDefaultControllers` restores envtest semantics on a
  dedicated plane.
- **Everything else is opt-in per test,** with `EnableControllers`. Enabling
  is idempotent, safe from parallel tests, and returns once the controller's
  informers have synced. Enabling one controller never enables another:
  Deployment without ReplicaSet creates ReplicaSets and no Pods, as upstream.
- **Out of scope:** node lifecycle and IPAM, volumes, HPA, cloud controllers,
  certificate signing, bootstrap tokens.

## Nodes and scheduling

- **Nodes exist only on dedicated planes.** The shared plane refuses every
  Node operation, so one test's Nodes never schedule another test's Pods.
- **Simulated Nodes behave like kwok's.** A kubelet simulator manages only the
  Nodes petri created: a bound Pod goes Running and Ready once, with an IP
  from the Node's PodCIDR, and a deleted Pod is finalized at once. It never
  overrides a phase or readiness the test set. No containers run.
- **The Node lifecycle is the test's.** There is no node lifecycle
  controller: a Node that is not Ready freezes its Pods and never gets tainted
  or evicted on its own. Tests drive it with `SetReady`, `Taint`, `Cordon`,
  `Drain` and `Delete`; `Drain` uses the Eviction API, so budgets apply.
- **The scheduler is upstream kube-scheduler and opt-in**
  (`SchedulerController`), with its default profile.
- **Without it, tests place Pods themselves,** gomock-style: imperative
  `ExpectPod` and `Bind`, and declarative `ExpectPods(...).Times(n).BindTo(node)`
  and `StrictPods`, built only on the imperative calls. Expectations claim
  matching Pods in registration order, unmet ones fail at cleanup. A plane
  uses either the scheduler or manual placement, never both.
