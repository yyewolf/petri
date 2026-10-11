#!/usr/bin/env bash
# Checks that a release tag belongs to the release line it declares:
# - petri v0.<minor>.<patch> must target the Kubernetes minor in version.go.
# - controllerruntime/v0.<minor>.<patch> must require petri v0.<minor>.<patch>
#   in controllerruntime/go.mod.
# Usage: check-release-tag.sh <tag> [version-file]
set -euo pipefail

tag="${1-}"
version_file="${2:-version.go}"

if [[ "$tag" == controllerruntime/* ]]; then
	sub="${tag#controllerruntime/}"

	if [[ ! "$sub" =~ ^v0\.([0-9]+)\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
		echo "error: tag '$tag' is not of the form controllerruntime/v0.<minor>.<patch>[-prerelease]" >&2
		exit 1
	fi
	minor="${BASH_REMATCH[1]}"

	gomod="controllerruntime/go.mod"
	required=$(awk '$1 == "github.com/yyewolf/petri" { print $2; exit }' "$gomod" 2>/dev/null || true)
	if [[ -z "$required" ]]; then
		echo "error: $gomod does not require github.com/yyewolf/petri" >&2
		exit 1
	fi
	if [[ "$required" != "v0.$minor."* ]]; then
		echo "error: tag $tag requires petri v0.$minor.x but $gomod requires $required" >&2
		exit 1
	fi

	echo "ok: $tag requires petri $required"

	exit 0
fi

if [[ ! "$tag" =~ ^v0\.([0-9]+)\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
	echo "error: tag '$tag' is not of the form v0.<minor>.<patch>[-prerelease]" >&2
	exit 1
fi
minor="${BASH_REMATCH[1]}"

kube=""
if [[ -f "$version_file" ]]; then
	kube=$(sed -n 's/^const KubernetesVersion = "\(.*\)"$/\1/p' "$version_file")
fi
if [[ -z "$kube" ]]; then
	echo "error: could not read 'const KubernetesVersion = \"…\"' from $version_file" >&2
	exit 1
fi

if [[ "$kube" != "1.$minor" ]]; then
	echo "error: tag $tag targets Kubernetes 1.$minor but $version_file declares $kube" >&2
	exit 1
fi

echo "ok: $tag matches Kubernetes $kube"
