# petri design

This file records the decisions petri is built on and why, so they are not
argued again with every change. The
[documentation site](https://yyewolf.github.io/petri/) describes what petri
does; this file explains the choices behind it. When a decision changes,
update it here in the same PR as the code.

## Goal

`go test ./...` on a fresh checkout runs a real Kubernetes control plane inside
the test process, with no downloaded binaries, no Docker and nothing written to
disk. "Real" means upstream code doing what it does upstream: validation,
defaulting, strategic merge patch, admission, RBAC, ClusterIP allocation,
controllers and the scheduler. petri is not a lookalike built from OpenAPI
schemas.

petri started as a drop-in replacement for controller-runtime's envtest. It
goes further than envtest: deleted namespaces go away, garbage collection
cascades, workload controllers run, and Pods can run on simulated Nodes.

## Kubernetes version

- **One version per build, chosen through go.mod.** A test binary can link
  only one `k8s.io/apiserver`, and the API server relies on process globals,
  so there is no option to pick a version at runtime. The server minor always
  equals the user's `k8s.io/client-go` minor. Version-skew testing, which
  envtest can do with separate binaries, is out of scope.
- **Versions follow the staging modules.** petri `v0.37.x` embeds Kubernetes
  1.37. `main` tracks the newest minor, and older minors live on
  `release-1.N` branches. See [releasing.md](releasing.md).
- **The Go floor follows Kubernetes.** petri's `go` directive is never newer
  than the embedded Kubernetes release's. `make mirror` raises it only when
  Kubernetes does.

## Mirroring instead of importing

- **Users never import `k8s.io/kubernetes`.** Its go.mod pins the staging
  modules at `v0.0.0` and resolves them with `replace` directives. Go ignores
  `replace` in dependencies, so every user would have to copy them. petri's
  own go.mod never needs `replace` either.
- **Upstream code is mirrored.** `hack/mirror` copies the packages petri needs
  from `k8s.io/kubernetes` into `internal/kubernetes`, and kine into
  `internal/kine`. It rewrites import paths so the code builds against the
  published staging modules. `MIRROR.json` lists the root packages and the
  mirrored version. The tool computes the closure for every supported
  platform and copies only non-test files.
- **Nobody edits the mirrors by hand.** CI regenerates them and fails on any
  difference. All adaptation lives in petri's own code. The one exception is
  small patches in `hack/mirror/patches/`, applied after the rewrite, for when
  a mirrored import would pull a large module into every user's graph. Today
  there is one: kine imports etcd's server module for a single constant.
  `TestBuildGraphStaysLean` checks that the modules petri keeps out stay out.
- **kine is mirrored too**, not imported. Its go.mod asks for a newer Go patch
  than Kubernetes does, which would raise every user's Go floor. Only its
  memory driver and etcd gRPC server are mirrored, so no SQL, NATS or S3
  backend reaches the user's module graph.
- **Upkeep is automated.** The Mirror workflow opens a PR for each new
  Kubernetes patch on every release line, and for the next minor on `main`.
  A line stops getting updates when upstream stops patching it.

## The control plane

- **Storage is kine's in-memory driver**, served on `127.0.0.1:0` through its
  etcd gRPC API. There is no etcd or SQLite, and nothing on disk.
  etcd-specific edge cases such as compaction timing may differ. Because
  storage sits behind the etcd API, it could be swapped without touching
  anything else.
- **kube-apiserver runs from the mirrored `cmd/kube-apiserver/app`,** set up
  the way upstream's test server sets it up, without its test-only
  dependencies. Certificates and service-account keys are generated in memory.
- **There are two kinds of plane.** `Start(t)` returns one control plane shared
  by the whole test binary. It starts on first use and goes away when the
  process exits. `New(ctx)` starts a dedicated plane that the test stops.
  Several dedicated planes can run at once and stay isolated; tests check
  this.
- **Tests on the shared plane are isolated by namespace.** Each test gets a
  unique namespace from `Namespace(t)` or `NewNamespace(ctx)`. Cluster-wide
  changes (CRDs, controllers) only add to the shared plane. Installing or
  enabling twice is safe, and nothing is ever removed, because other tests may
  rely on it.
- **Logs are captured.** klog and logrus are redirected, process-wide, into a
  buffer that is printed when startup fails. `PETRI_VERBOSE=1` also sends them
  to stderr. Feature gates stay at upstream defaults.

## Test helpers

- **`InstallCRDs` returns only when every CRD is Established and served by
  discovery,** so a RESTMapper built right after it resolves the new kinds. A
  CRD that already exists with the same spec (compared after server-side
  defaulting) is left alone. A different spec fails the test with a diff.
- **Waits use watches.** They list, then watch from the object's
  resourceVersion, so a change made just before the wait is not missed.
- **Every helper has a form that takes `t` and a form that takes a context
  and returns an error.** The second is for frameworks like Ginkgo that do not
  provide a `testing.TB`.
- **controller-runtime support is a separate module,**
  `github.com/yyewolf/petri/controllerruntime`, so petri itself does not
  depend on controller-runtime. It provides a Manager helper and an envtest
  drop-in. Both admission and CRD conversion webhooks are supported.

## Controllers

- **petri runs its own small manager,** not kube-controller-manager. Only the
  controller packages are mirrored; the upstream binary would bring its flags,
  cloud providers and every controller. Each plane has one manager, which owns
  the shared informer factories and a RESTMapper backed by discovery.
  Controllers use the admin config with their own user agent, and no leader
  election.
- **The namespace and garbage-collector controllers run by default** on every
  plane. This differs from envtest on purpose: deleted namespaces go away and
  owners cascade. `WithoutDefaultControllers` restores envtest's behaviour on
  a dedicated plane.
- **Every other controller is opt-in per test,** with `EnableControllers`.
  Enabling is idempotent, safe from parallel tests, and returns once the
  controller's informers have synced. Enabling one controller never enables
  another: the Deployment controller without the ReplicaSet controller creates
  ReplicaSets and no Pods, as upstream.
- **Out of scope:** node lifecycle and IPAM, volumes, HPA, cloud controllers,
  certificate signing, bootstrap tokens.

## Nodes and scheduling

- **Only dedicated planes have Nodes.** The shared plane refuses every Node
  operation, so one test's Nodes never run another test's Pods.
- **Simulated Nodes behave like kwok's.** A kubelet simulator manages only the
  Nodes petri created. A bound Pod goes Running and Ready once, with an IP
  from the Node's PodCIDR, and a deleted Pod is finalized at once. The
  simulator never overrides a phase or readiness the test set. No containers
  run.
- **The test drives the Node lifecycle.** There is no node lifecycle
  controller. A Node that is not Ready freezes its Pods and is never tainted
  or evicted by itself. Tests use `SetReady`, `Taint`, `Cordon`, `Drain` and
  `Delete`. `Drain` uses the Eviction API, so budgets apply.
- **The scheduler is upstream kube-scheduler and opt-in**
  (`SchedulerController`), with its default profile.
- **Without it, tests place Pods themselves,** in the style of gomock.
  `ExpectPod` and `Bind` are the imperative calls. `ExpectPods(...).Times(n).BindTo(node)`
  and `StrictPods` declare expectations, and are built only on the imperative
  calls. Expectations claim matching Pods in the order they were registered,
  and unmet ones fail at cleanup. A plane uses either the scheduler or manual
  placement, never both.
