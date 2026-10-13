#!/usr/bin/env bash
# Scenario test for check-doc-versions.sh on a scratch tree.
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/check-doc-versions.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

failures=0
fail() {
	echo "FAIL: $case: $*"
	failures=$((failures + 1))
}

seed() {
	rm -rf "$tmp/repo"
	mkdir -p "$tmp/repo/website/docs" "$tmp/repo/website/versioned_docs/version-0.37"
	printf 'package petri\n\nconst KubernetesVersion = "1.%s"\n' "$1" >"$tmp/repo/version.go"
	printf 'go get github.com/yyewolf/petri@v0.37\n' >"$tmp/repo/README.md"
	printf 'go get github.com/yyewolf/petri/controllerruntime@v0.37\n' >"$tmp/repo/website/docs/start.mdx"
	printf 'go get github.com/yyewolf/petri@v0.37\n' >"$tmp/repo/website/versioned_docs/version-0.37/start.mdx"
}

run() {
	status=0
	(cd "$tmp/repo" && "$script") >"$tmp/out.log" 2>&1 || status=$?
}

case="references on the current line pass"
seed 37
run
[[ $status == 0 ]] || fail "exit $status: $(cat "$tmp/out.log")"

case="references left on the previous line fail and name the file"
seed 38
run
[[ $status != 0 ]] || fail "accepted @v0.37 on the 0.38 line"
grep -qF 'README.md' "$tmp/out.log" || fail "README not named: $(cat "$tmp/out.log")"
grep -qF 'website/docs/start.mdx' "$tmp/out.log" || fail "page not named: $(cat "$tmp/out.log")"
grep -qF 'versioned_docs' "$tmp/out.log" && fail "frozen docs were checked: $(cat "$tmp/out.log")"

if ((failures > 0)); then
	echo "$failures check(s) failed"
	exit 1
fi
echo "ok: all doc version cases passed"
