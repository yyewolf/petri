#!/usr/bin/env bash
# Bumps the controllerruntime sub-module to the Kubernetes minor being
# mirrored: the staging module versions from the root go.mod, which the mirror
# has already pinned, and the newest controller-runtime release that requires
# them. The petri requirement is written as the upcoming minor tag, which
# exists by the time the sub-module is released; the go.work keeps local and CI
# builds on the working tree until then.
#
# Exits 2 when no controller-runtime for the minor is released yet, so the
# mirror workflow can still open its PR as a draft.
#
# Usage: bump-submodule.sh <kube-version>   e.g. v1.38.2
# Environment: GO overrides the go command (tests stub it).
set -euo pipefail

kube="${1:?usage: bump-submodule.sh <kube-version>}"
go_cmd=${GO:-go}

if [[ ! "$kube" =~ ^v1\.([0-9]+)\.[0-9]+$ ]]; then
	echo "error: '$kube' is not a Kubernetes version like v1.38.2" >&2
	exit 1
fi
minor="${BASH_REMATCH[1]}"

# mod_require MODULE FILE prints the version FILE requires of MODULE, whether
# the require is a single line or part of a block.
mod_require() {
	awk '$1 == "require" && $2 == "'"$1"'" { print $3; exit } $1 == "'"$1"'" { print $2; exit }' "$2"
}

staging=$(mod_require k8s.io/client-go go.mod)
if [[ -z "$staging" ]]; then
	echo "error: the root go.mod does not require k8s.io/client-go; run make mirror first" >&2
	exit 1
fi
if [[ "$(sed -n 's/^v0\.\([0-9]*\)\..*/\1/p' <<<"$staging")" != "$minor" ]]; then
	echo "error: the root go.mod requires k8s.io/client-go $staging, which is not Kubernetes 1.$minor; run make mirror first" >&2
	exit 1
fi

# The newest controller-runtime release that requires this minor. Releases
# requiring a newer minor are skipped, and the walk stops at the first one
# below it: controller-runtime tracks Kubernetes minors one to one.
controller_runtime=""
while read -r version; do
	gomod=$("$go_cmd" mod download -json "sigs.k8s.io/controller-runtime@$version" |
		sed -n 's/.*"GoMod": *"\([^"]*\)".*/\1/p')
	client=$(mod_require k8s.io/client-go "$gomod")
	cr_minor=$(sed -n 's/^v0\.\([0-9]*\)\..*/\1/p' <<<"$client")

	if ((cr_minor == minor)); then
		controller_runtime=$version

		break
	fi
	if ((cr_minor < minor)); then
		break
	fi
done < <("$go_cmd" list -m -versions sigs.k8s.io/controller-runtime |
	awk '{for (i = 2; i <= NF; i++) print $i}' | sort -rV)

if [[ -z "$controller_runtime" ]]; then
	echo "error: no controller-runtime release for Kubernetes 1.$minor yet" >&2
	exit 2
fi

cd controllerruntime

# controller-runtime first, so its own staging requirements enter the graph;
# the explicit get then pins the exact versions petri mirrors.
"$go_cmd" get "sigs.k8s.io/controller-runtime@$controller_runtime"
"$go_cmd" get "k8s.io/api@$staging" "k8s.io/apimachinery@$staging" "k8s.io/client-go@$staging"

sed -i "s|github.com/yyewolf/petri v[^ ]*|github.com/yyewolf/petri v0.$minor.0|" go.mod
if ! grep -q "github.com/yyewolf/petri v0.$minor.0" go.mod; then
	echo "error: controllerruntime/go.mod does not require github.com/yyewolf/petri" >&2
	exit 1
fi

"$go_cmd" mod tidy
echo "bumped controllerruntime to Kubernetes 1.$minor with controller-runtime $controller_runtime"
