// Package petri runs a real Kubernetes control plane inside go test, with no
// downloaded binaries, no Docker, and nothing written to disk.
//
// petri is pre-alpha: the API server, the namespace controller, the garbage
// collector and the workload controllers work, but the scheduler and fake
// kubelets do not — Pods stay Pending — and the API will change without
// notice.
package petri
