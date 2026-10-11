#!/usr/bin/env bash
# Table test for check-release-tag.sh. Run from the repository root.
set -euo pipefail

script="$(dirname "$0")/check-release-tag.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

printf 'package petri\n\nconst KubernetesVersion = "1.37"\n' >"$tmp/v137.go"
printf 'package petri\n\nconst (\n\tKubernetesVersion = "1.37"\n)\n' >"$tmp/block.go"
printf 'package petri\n\nconst KubernetesVersion = "1.37" // comment\n' >"$tmp/trailing.go"
printf 'package petri\n' >"$tmp/missing.go"

failures=0
expect() {
	local want="$1" tag="$2" file="$3" got
	if "$script" "$tag" "$file" >/dev/null 2>&1; then got=pass; else got=fail; fi
	if [[ "$got" != "$want" ]]; then
		echo "FAIL: tag=$tag file=$(basename "$file"): want $want, got $got"
		failures=$((failures + 1))
	fi
}

# Matching tags.
expect pass v0.37.0 "$tmp/v137.go"
expect pass v0.37.12 "$tmp/v137.go"
expect pass v0.37.1-rc.1 "$tmp/v137.go"

# Wrong release line or look-alike minors.
expect fail v0.36.0 "$tmp/v137.go"
expect fail v0.370.0 "$tmp/v137.go"
expect fail v0.3.0 "$tmp/v137.go"

# Malformed tags.
expect fail v1.0.0 "$tmp/v137.go"
expect fail v0.37 "$tmp/v137.go"
expect fail 0.37.0 "$tmp/v137.go"
expect fail v0.37.0+meta "$tmp/v137.go"
expect fail "" "$tmp/v137.go"

# version.go shapes the guard cannot read must fail, never pass.
expect fail v0.37.0 "$tmp/block.go"
expect fail v0.37.0 "$tmp/trailing.go"
expect fail v0.37.0 "$tmp/missing.go"
expect fail v0.37.0 "$tmp/does-not-exist.go"

# The real version.go must stay readable by the guard.
expect pass "v0.$(cut -d. -f2 <<<"$(sed -n 's/^const KubernetesVersion = "\(.*\)"$/\1/p' version.go)").0" version.go

if ((failures > 0)); then
	echo "$failures case(s) failed"
	exit 1
fi
echo "ok: all release tag cases passed"
