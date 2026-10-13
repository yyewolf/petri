---
slug: /
sidebar_position: 1
description: What petri is, and how it compares with envtest.
---

# Introduction

petri runs a real Kubernetes control plane inside `go test`: upstream
kube-apiserver code, compiled into your test binary and serving from memory.
There is nothing to download, no container to start and nothing written to
disk. A test calls `petri.Start(t)` for the control plane its test binary
shares, `cp.Namespace(t)` for a namespace of its own, and talks to the API
server with the clients it already uses.

## What you get

- Validation, defaulting, admission, RBAC, strategic merge patch, CRD schemas
  and ClusterIP allocation run upstream's own code.
- Deleted namespaces go away and deleting an owner deletes its dependents,
  without any setup. Deployments, StatefulSets, Jobs and the other workload
  controllers run when a test enables them.
- Simulated Nodes run Pods the way [kwok](https://kwok.sigs.k8s.io/) does,
  placed by upstream kube-scheduler or by the test, gomock-style.
- One plane starts in about two seconds and serves every test in the binary,
  each in its own namespace, so suites run in parallel.
- A controller-runtime module provides a Manager helper and a drop-in for
  envtest.

## Compared with envtest

controller-runtime's envtest starts etcd and kube-apiserver as binaries that
you download first. petri differs in a few ways:

| | envtest | petri |
|---|---|---|
| Setup | download binaries (`setup-envtest`) | `go get` |
| Processes | etcd and kube-apiserver | none: in your test process |
| Namespace deletion, garbage collection | no | yes, by default |
| Workload controllers, scheduler, Nodes | no | opt-in |
| Kubernetes version | any, per binary | your `k8s.io/client-go` minor |
| Memory, plain build | about 350 MiB | about 300 MiB |
| Memory, `-race` build | about 400 MiB | about 1 to 1.5 GiB |

Choose envtest if you need to test against several Kubernetes versions from one
module, or if `-race` memory is a constraint you cannot work around. Otherwise,
petri tests more of what a cluster does with less setup, and an existing
envtest suite moves over by changing one import; see
[migrating from envtest](guides/migrating-from-envtest.mdx).

## Next

Start with [getting started](getting-started.mdx), then the guides for what
your code touches.
