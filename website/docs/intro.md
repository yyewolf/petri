---
slug: /
sidebar_position: 1
description: What petri is, and how it compares with envtest.
---

# Introduction

petri runs a real Kubernetes control plane inside `go test`. The upstream
kube-apiserver code is compiled into your test binary and keeps its data in
memory, so there is nothing to download, no container to start and nothing
written to disk.

A test calls `petri.Start(t)` to get the control plane its test binary shares,
and `cp.Namespace(t)` to get a namespace of its own. It then talks to the API
server with the clients it already uses.

## What you get

- Upstream code handles validation, defaulting, admission, RBAC, strategic
  merge patch, CRD schemas and ClusterIP allocation.
- Deleted namespaces go away and deleting an owner deletes its dependents,
  with no setup. Workload controllers, such as the Deployment and Job
  controllers, run when a test enables them.
- Simulated Nodes run Pods the way [kwok](https://kwok.sigs.k8s.io/) does.
  Either upstream kube-scheduler places the Pods, or the test does.
- One control plane serves every test in the binary, so tests can run in
  parallel.
- A separate module adds a controller-runtime Manager helper and a drop-in
  replacement for envtest.

## Compared with envtest

controller-runtime's envtest starts etcd and kube-apiserver as binaries that
you download first. The main differences:

| | envtest | petri |
|---|---|---|
| Setup | download binaries (`setup-envtest`) | `go get` |
| Processes | etcd and kube-apiserver | none: in your test process |
| Namespace deletion, garbage collection | no | yes, by default |
| Workload controllers, scheduler, Nodes | no | opt-in |
| Kubernetes version | any, per binary | your `k8s.io/client-go` minor |
| Memory, plain build | about 350 MiB | about 300 MiB |
| Memory, `-race` build | about 400 MiB | about 1 to 1.5 GiB |

Choose envtest if you test against several Kubernetes versions from one
module, or if you cannot afford the memory petri uses under `-race`. Otherwise
petri covers more of a cluster's behaviour with less setup. An existing envtest
suite moves over with a one-line import change; see
[migrating from envtest](guides/migrating-from-envtest.mdx).

## Next

Read [getting started](getting-started.mdx), then the guides for the parts of
Kubernetes your code uses.
