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

# Sub-module fixtures: a go.mod requiring the matching petri version, one
# requiring an older one, and one with no petri require at all.
subgomod() {
	mkdir -p "$tmp/$1/controllerruntime"
	cat >"$tmp/$1/controllerruntime/go.mod" <<EOF
module github.com/yyewolf/petri/controllerruntime

go 1.26.5

require (
	github.com/yyewolf/petri $2
	sigs.k8s.io/controller-runtime v0.25.2
)
EOF
}
subgomod sub-matching v0.37.2
subgomod sub-older v0.36.0
subgomod sub-missing 'v0.0.0-00010101000000-000000000000'
sed -i '/github.com\/yyewolf\/petri/d' "$tmp/sub-missing/controllerruntime/go.mod"

failures=0
expect() {
	local want="$1" tag="$2" file="$3" got
	if "$script" "$tag" "$file" >/dev/null 2>&1; then got=pass; else got=fail; fi
	if [[ "$got" != "$want" ]]; then
		echo "FAIL: tag=$tag file=$(basename "$file"): want $want, got $got"
		failures=$((failures + 1))
	fi
}

# expectsub runs the check from a fixture directory, where the sub-module's
# go.mod is what the guard reads.
expectsub() {
	local want="$1" tag="$2" dir="$3" got
	if (cd "$tmp/$dir" && "$OLDPWD/$script" "$tag" >/dev/null 2>&1); then got=pass; else got=fail; fi
	if [[ "$got" != "$want" ]]; then
		echo "FAIL: tag=$tag dir=$dir: want $want, got $got"
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

# Sub-module tags must carry a matching petri require in their go.mod.
expectsub pass controllerruntime/v0.37.2 sub-matching
expectsub pass controllerruntime/v0.37.12 sub-matching
expectsub fail controllerruntime/v0.37.2 sub-older
expectsub fail controllerruntime/v0.37.2 sub-missing
expectsub fail controllerruntime/v0.36.0 sub-matching
expectsub fail controllerruntime/v0.37 sub-matching
expectsub fail v0.37.2 sub-matching

# The real version.go must stay readable by the guard.
expect pass "v0.$(cut -d. -f2 <<<"$(sed -n 's/^const KubernetesVersion = "\(.*\)"$/\1/p' version.go)").0" version.go

if ((failures > 0)); then
	echo "$failures case(s) failed"
	exit 1
fi
echo "ok: all release tag cases passed"
