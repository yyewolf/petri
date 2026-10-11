package petri

// KubernetesVersion is the Kubernetes minor version this release line of petri
// targets. petri tags mirror the k8s.io staging modules: petri v0.37.x builds
// against k8s.io/* v0.37.x, which is Kubernetes 1.37.
const KubernetesVersion = "1.37"
