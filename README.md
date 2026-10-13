<h1 align="center">petri</h1>

<p align="center">A real Kubernetes control plane inside <code>go test</code>: upstream kube-apiserver and controllers, compiled into your test binary. No binaries to download, no Docker.</p>

<p align="center">
  <a href="https://yyewolf.github.io/petri/"><img src="https://img.shields.io/badge/docs-yyewolf.github.io%2Fpetri-b45309" alt="Documentation"></a>
  <a href="https://pkg.go.dev/github.com/yyewolf/petri"><img src="https://pkg.go.dev/badge/github.com/yyewolf/petri.svg" alt="Go Reference"></a>
  <a href="https://github.com/yyewolf/petri/actions/workflows/ci.yml"><img src="https://github.com/yyewolf/petri/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/yyewolf/petri/releases"><img src="https://img.shields.io/github/v/release/yyewolf/petri?sort=semver&filter=v*" alt="Release"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/yyewolf/petri" alt="Go version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="License"></a>
</p>

---

> [!WARNING]
> petri is pre-alpha: the API will change between releases.

## Install

```bash
go get github.com/yyewolf/petri@v0.37
# for controller-runtime and the envtest drop-in:
go get github.com/yyewolf/petri/controllerruntime@v0.37
```

> [!IMPORTANT]
> Use the petri minor that matches your `k8s.io/client-go` minor: petri
> `v0.37.x` embeds Kubernetes 1.37. The API server is compiled into your test
> binary, so it always runs the version your client libraries target.

## Usage

```go
func TestDeploymentIsReconciled(t *testing.T) {
	t.Parallel()

	cp := petri.Start(t)  // the control plane shared by this test binary
	ns := cp.Namespace(t) // a fresh namespace, deleted when the test ends
	cs := cp.Client()     // an admin clientset; cp.Config() gives a *rest.Config
	cp.EnableControllers(t, petri.DeploymentController)

	labels := map[string]string{"app": "web"}
	deployments := cs.AppsV1().Deployments(ns)
	_, err := deployments.Create(t.Context(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "web", Image: "nginx"}},
				},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// The Deployment controller has seen the Deployment once it reports the
	// generation it acted on.
	petri.WaitFor(t, deployments, "web", func(d *appsv1.Deployment) (bool, error) {
		return d != nil && d.Status.ObservedGeneration == d.Generation, nil
	})
}
```

`petri.Start(t)` starts one control plane for the whole test binary, in about
two seconds; each test works in its own namespace. From there:

- [CRDs](https://yyewolf.github.io/petri/docs/guides/crds) and [webhooks](https://yyewolf.github.io/petri/docs/guides/webhooks), installed per test.
- [Built-in controllers](https://yyewolf.github.io/petri/docs/guides/controllers): namespace deletion and garbage collection by default, workload controllers on demand.
- [Simulated Nodes and kube-scheduler](https://yyewolf.github.io/petri/docs/guides/nodes), or [Pods placed by the test](https://yyewolf.github.io/petri/docs/guides/pod-expectations), gomock-style.
- [controller-runtime](https://yyewolf.github.io/petri/docs/guides/controller-runtime) and an [envtest drop-in](https://yyewolf.github.io/petri/docs/guides/migrating-from-envtest): change one import.

## Documentation

- [yyewolf.github.io/petri](https://yyewolf.github.io/petri/): guides, practices and reference.
- [pkg.go.dev](https://pkg.go.dev/github.com/yyewolf/petri): the API.
- [CONTRIBUTING.md](CONTRIBUTING.md): working on petri.

## License

Apache-2.0. See [LICENSE](LICENSE).
