// Package petri runs a real Kubernetes control plane inside go test, with no
// downloaded binaries, no Docker, and nothing written to disk.
//
// petri is pre-alpha: the API server, the built-in controllers, kube-scheduler
// and simulated Nodes work, and the API will change without notice.
//
// Pods run on simulated Nodes, kwok-style: a dedicated control plane gets
// Nodes from [ControlPlane.AddNode], and Pods placed on them by
// [SchedulerController] or by the test ([ControlPlane.Bind],
// [ControlPlane.ExpectPod], [ControlPlane.ExpectPods]) go Running and Ready
// at once, with no containers behind them.
package petri
