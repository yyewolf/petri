// Package petri runs a real Kubernetes control plane inside go test, with no
// downloaded binaries, no Docker, and nothing written to disk.
//
// petri is pre-alpha: the API server, the namespace controller and the
// garbage collector work, but the workload controllers, the scheduler and
// fake kubelets are not implemented yet, and the API will change without
// notice.
package petri
