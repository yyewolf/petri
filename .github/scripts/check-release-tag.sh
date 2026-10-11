#!/usr/bin/env bash
# Checks that a release tag belongs to the release line declared in version.go:
# petri v0.<minor>.<patch> must target Kubernetes 1.<minor>.
# Usage: check-release-tag.sh <tag> [version-file]
set -euo pipefail

tag="${1-}"
version_file="${2:-version.go}"

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
