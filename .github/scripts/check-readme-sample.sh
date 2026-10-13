#!/usr/bin/env bash
# Checks that the README's Go sample is the quickstart test the documentation
# site shows and CI runs: the first ```go block of the README must equal the
# sample file's first test function, from `func Test` to its closing brace.
# Usage: check-readme-sample.sh [readme] [sample]
set -euo pipefail

readme="${1:-README.md}"
sample="${2:-website/examples/quickstart/quickstart_test.go}"

shown=$(awk '/^```go$/ { inside = 1; next } inside && /^```$/ { exit } inside { print }' "$readme")
if [[ -z "$shown" ]]; then
	echo "error: $readme has no \`\`\`go block" >&2
	exit 1
fi

# gofmt puts a top-level function's closing brace alone at column 0.
tested=$(awk '/^func Test/ { inside = 1 } inside { print } inside && /^}$/ { exit }' "$sample")

if ! diff -u --label "$sample" --label "$readme" <(echo "$tested") <(echo "$shown"); then
	echo "error: the Go sample in $readme differs from the test in $sample; copy the test function into the README" >&2
	exit 1
fi

echo "ok: $readme shows the test in $sample"
