#!/usr/bin/env bash
# Checks that the install commands in the README and the current docs name the
# release line version.go declares: `@v0.N` must be the current minor. Frozen
# docs (website/versioned_docs) keep their own line and are not checked.
# Run from the repository root.
set -euo pipefail

minor=$(sed -n 's/^const KubernetesVersion = "1\.\([0-9]*\)"$/\1/p' version.go)
if [[ -z "$minor" ]]; then
	echo "error: could not read 'const KubernetesVersion = \"1.N\"' from version.go" >&2
	exit 1
fi

if stale=$(grep -rnoE '@v0\.[0-9]+' README.md website/docs | grep -v "@v0\.$minor\$"); then
	echo "error: these install commands name another release line than v0.$minor (version.go):" >&2
	echo "$stale" >&2
	exit 1
fi

echo "ok: README and current docs install petri v0.$minor"
