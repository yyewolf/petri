// Package controllerruntime wires petri control planes into
// controller-runtime: a Manager helper and an envtest drop-in, so a
// kubebuilder test suite switches to petri by changing its import path.
//
// The petri control plane runs the namespace controller and the garbage
// collector by default; see the envtest package for what that means for a
// suite, how to opt out of it, and how to enable petri's other controllers.
//
// The Manager helper is a different concern: it builds a controller-runtime
// Manager for the suite's own reconcilers, and it never runs petri's
// controllers, which belong to the control plane. The two compose: petri's
// controllers run on the plane's own manager, the suite's reconcilers on the
// Manager NewManager returns, both against the same API server.
package controllerruntime
