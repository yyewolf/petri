# Changelog

## 0.37.0 (2026-10-13)


### Features

* add ControlPlane.Client for a shared admin clientset ([f0ba51f](https://github.com/yyewolf/petri/commit/f0ba51f856152266de14f0b8889c9a16c7ad0f0d))
* add EnableControllers and the seven workload controllers ([051552c](https://github.com/yyewolf/petri/commit/051552caa2d6026cf23f331bba7b957fe59629b8))
* add InstallCRDs with sources, defaulting-aware conflicts and discovery readiness ([9ea97af](https://github.com/yyewolf/petri/commit/9ea97af29efd8f7df5b817b1fe2fc5c33e9d48f8))
* add kube-scheduler and a kubelet simulator to the controllers ([104ac73](https://github.com/yyewolf/petri/commit/104ac7344018741fb79cd771e12a490698980fde))
* add NewNamespace without a test-name prefix ([e705f0c](https://github.com/yyewolf/petri/commit/e705f0ce44a17e0650250b4aea5ba9794417d117))
* add petri module with KubernetesVersion ([e330b12](https://github.com/yyewolf/petri/commit/e330b1242633ec54b7fc410868556a333425dac4))
* add simulated Nodes, manual Pod placement and Pod expectations ([b4ef961](https://github.com/yyewolf/petri/commit/b4ef96150b9a9ef7ddf06ce9a59418955ebfc9d1))
* add Start and Namespace with upstream fidelity tests ([90a313c](https://github.com/yyewolf/petri/commit/90a313c83b88f40bb872e5b90b34ea143f2948dd))
* add the controller manager skeleton ([cad45e3](https://github.com/yyewolf/petri/commit/cad45e39f0dc52580764e03c4fba269ae654cf4a))
* add the controller-runtime Manager helper and typed waits ([43b8bc1](https://github.com/yyewolf/petri/commit/43b8bc141795d400b864205dedd7b80c81b8f48c))
* add the envtest drop-in and a ported kubebuilder suite ([ea6c7f9](https://github.com/yyewolf/petri/commit/ea6c7f960ead7db1779c2357434452d2c3ded321))
* add watch-based wait and retry helpers ([8808b6d](https://github.com/yyewolf/petri/commit/8808b6de1f8722bd72e2d15a8a0f207040058a2d))
* add webhook helpers for admission and CRD conversion ([cb9baf0](https://github.com/yyewolf/petri/commit/cb9baf05b0cc2cd04f70c28469278f43eeb5bd57))
* capture control plane logs and derive namespace names ([cd38ac2](https://github.com/yyewolf/petri/commit/cd38ac2c14d9f4120a699e051e74851dcd2d40e9))
* expose EnableControllers on the envtest drop-in ([a43755b](https://github.com/yyewolf/petri/commit/a43755b5cef33a06ff85ec5f5a7c4004aa143651))
* give the envtest drop-in an opt-out from petri's default controllers ([2860a37](https://github.com/yyewolf/petri/commit/2860a37a720c77678356c7b2a2d2422069e92e59))
* let controllers wait on and release informers of their own ([3952161](https://github.com/yyewolf/petri/commit/395216191924b7d84c05a08818f2c29eb09e4095))
* mirror the built-in controller packages ([22b3287](https://github.com/yyewolf/petri/commit/22b3287915d7ff9df71d548db16934a86a5c23c9))
* **mirror:** add sync pipeline and make mirror target ([d299dd5](https://github.com/yyewolf/petri/commit/d299dd556c153b2150d4e52686f7e9404d3e1757))
* **mirror:** plan patch and minor mirror updates ([ed29594](https://github.com/yyewolf/petri/commit/ed295945bbe5cfe3cae336a08f9031c3ada96d52))
* **mirror:** rewrite k8s.io/kubernetes import paths ([ffd38d1](https://github.com/yyewolf/petri/commit/ffd38d1a44f7f3d238f2c9e90d7723232fdddc33))
* register the EndpointSlice, ServiceAccount, token, ResourceQuota and disruption controllers ([384314c](https://github.com/yyewolf/petri/commit/384314cf05c165f2915c15559def903b30a845d0))
* run kube-apiserver in-process over in-memory kine ([7b0d954](https://github.com/yyewolf/petri/commit/7b0d954e3e1c94c03f44579457e147e20986442c))
* run the namespace controller and garbage collector by default ([8d4be7b](https://github.com/yyewolf/petri/commit/8d4be7ba5eff28f52228ea7f92d4d36483bd1c53))


### Bug Fixes

* address review findings on submodule tooling and webhook installs ([4aef7e1](https://github.com/yyewolf/petri/commit/4aef7e196a13a55b9454428cf6a94e510a848ed0))
* copy registered specs so callers cannot mutate the registry ([ad94ab4](https://github.com/yyewolf/petri/commit/ad94ab409233cb08cf9d7b8dd1be339c7f5547a9))
* default Config to JSON so typed CRD clients work ([0a6ff61](https://github.com/yyewolf/petri/commit/0a6ff618bca7a55673689cb54d7f4288260d62a0))
* keep draining kine watch channels after cancellation ([5ccb980](https://github.com/yyewolf/petri/commit/5ccb980037427930e71642e8f0f053c889889ed3))
* make late controller construction safe against running informers ([f1cd141](https://github.com/yyewolf/petri/commit/f1cd1415c8d397c9239500fb4db5f68d72a05601))
* retry namespace creation when the random name collides ([5f304c0](https://github.com/yyewolf/petri/commit/5f304c0900b86ae52c119c57f19ac74603f927e2))
* serialize kine memory-driver reads against writes ([0f12718](https://github.com/yyewolf/petri/commit/0f12718629c546ea4b7f4cdbf211fccff6581438))
* stop repeating startup logs and errors ([e6f9eab](https://github.com/yyewolf/petri/commit/e6f9eabd019316d71c721a33abaa5db6400687aa))


### Kubernetes, kine and dependencies

* add golangci-lint config and Makefile ([7c30117](https://github.com/yyewolf/petri/commit/7c30117aaa418caeea9aff531d3b48f2ed97a5f6))
* add the controllerruntime module, workspace and release tooling ([575f467](https://github.com/yyewolf/petri/commit/575f467b2cd41fa51ef4260671c0e3ed7dba3fe1))
* adopt stricter golangci-lint config (v2.14) ([15ffbc9](https://github.com/yyewolf/petri/commit/15ffbc92069d03d6b5936087d0ea04d2643f286f))
* let hack/mirror apply reviewed patches to the mirrors ([5c1ff06](https://github.com/yyewolf/petri/commit/5c1ff0630001771e845b43dcae93d423295b7069))
* let hack/mirror switch toolchains for the upstreams ([19c971d](https://github.com/yyewolf/petri/commit/19c971da909ec0437c72e6b4fcf954624405dc05))
* mirror kine and lower the Go floor to Kubernetes' ([e91a035](https://github.com/yyewolf/petri/commit/e91a03548e37fd59295e212a6542a34b4ebb7fd6))
* mirror kube-scheduler ([8b8ea0f](https://github.com/yyewolf/petri/commit/8b8ea0ff65d092f0fe47cd6924eacf726702adfa))
* mirror Kubernetes v1.37.1 kube-apiserver ([2eca1cc](https://github.com/yyewolf/petri/commit/2eca1cc88087bba1ebe17bb25aa0361f8d6f50ff))
* patch etcd's server module out of the kine mirror ([2dcd0bd](https://github.com/yyewolf/petri/commit/2dcd0bde2fedf1b314c93e6e292f8b74447aaa12))


### Documentation

* add AGENTS.md, the design decisions and the release process ([5309c83](https://github.com/yyewolf/petri/commit/5309c834b9857fea637037e2f59f136594322134))
* add README, CONTRIBUTING, and GitHub templates ([bf3e988](https://github.com/yyewolf/petri/commit/bf3e9882a349c5c5c81283d461cb57824bb673b4))
* document Nodes, scheduling and Pod expectations ([b86e421](https://github.com/yyewolf/petri/commit/b86e42126ccc6a928acc145834001867f5f13981))
* document petri's memory use against envtest ([76f2c74](https://github.com/yyewolf/petri/commit/76f2c74b52c59ce24839df61af0bc9f22bac2e25))
* document the build gate's handle contract ([e46cb7a](https://github.com/yyewolf/petri/commit/e46cb7ac0c64884db03b4836573febab6c2a3721))
* document the built-in controllers ([2484ab5](https://github.com/yyewolf/petri/commit/2484ab539c05ebacc9a06a1795ef4bd964dca7fb))
* document the drop-in's defaults and the Manager composition ([46fae67](https://github.com/yyewolf/petri/commit/46fae679dd054e16c355daf08888abe8470ec753))
* document the helpers, shared-plane rules and the envtest drop-in ([b7f39b5](https://github.com/yyewolf/petri/commit/b7f39b53678a15fb9abd3bee1278a731d6539dc9))
* document the in-process control plane and the mirror ([f34d966](https://github.com/yyewolf/petri/commit/f34d96623ba1dc20b2cfa74ce08bed37ea12f505))
* document the kine mirror and petri's Go floor ([b6e3203](https://github.com/yyewolf/petri/commit/b6e3203cb4fdc87e07e8f5a01ae48a9d36ec3ee9))
* document the mirror patches and the lean build guard ([98a6d94](https://github.com/yyewolf/petri/commit/98a6d9404e0b5df2c65821ad25ea6b794e9bb422))
* note the opt-out plane's controllers and the Enable restart rule ([106fbff](https://github.com/yyewolf/petri/commit/106fbff0bbc6dd996594ec166b630e31f84135b0))
* record the k8s.io/kubernetes import constraint ([ce0ab20](https://github.com/yyewolf/petri/commit/ce0ab201bd0a57d1e53d7c0d817ca00f35b90acf))
