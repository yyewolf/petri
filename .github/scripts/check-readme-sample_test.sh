#!/usr/bin/env bash
# Scenario test for check-readme-sample.sh on scratch files. Run from the
# repository root.
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/check-readme-sample.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

failures=0
fail() {
	echo "FAIL: $case: $*"
	failures=$((failures + 1))
}

cat >"$tmp/sample_test.go" <<'GO'
package quickstart_test

import "testing"

func TestQuickstart(t *testing.T) {
	t.Parallel()
	if true {
		t.Log("inner braces do not end the function")
	}
}

func helper() {}
GO

readme() {
	printf '# petri\n\nIntro.\n\n```bash\ngo get example\n```\n\n```go\n%s\n```\n\nMore text.\n' "$1" >"$tmp/README.md"
}

body='func TestQuickstart(t *testing.T) {
	t.Parallel()
	if true {
		t.Log("inner braces do not end the function")
	}
}'

run() {
	status=0
	"$script" "$tmp/README.md" "$tmp/sample_test.go" >"$tmp/out.log" 2>&1 || status=$?
}

case="a README showing the sample's test function passes"
readme "$body"
run
[[ $status == 0 ]] || fail "exit $status: $(cat "$tmp/out.log")"

case="an edited README fails with a diff"
readme "${body/t.Parallel()/t.Parallel() \/\/ edited}"
run
[[ $status != 0 ]] || fail "accepted an edited sample"
grep -qF 'edited' "$tmp/out.log" || fail "no diff in the output: $(cat "$tmp/out.log")"

case="a README without a Go block fails"
printf '# petri\n\n```bash\ngo get example\n```\n' >"$tmp/README.md"
run
[[ $status != 0 ]] || fail "accepted a README with no Go block"

if ((failures > 0)); then
	echo "$failures check(s) failed"
	exit 1
fi
echo "ok: all README sample cases passed"
