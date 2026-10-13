#!/usr/bin/env bash
# Scenario test for version-docs.sh on a scratch site, with npx stubbed so
# docusaurus is not needed: the stub does what `docusaurus docs:version`
# does to the files.
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/version-docs.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/npx" <<'STUB'
#!/usr/bin/env bash
# npx docusaurus docs:version <v>
v="$3"
mkdir -p "versioned_docs/version-$v"
cp -R docs/. "versioned_docs/version-$v/"
echo "[\"$v\"]" >versions.json
STUB
chmod +x "$tmp/npx"

failures=0
fail() {
	echo "FAIL: $case: $*"
	failures=$((failures + 1))
}

# seed writes a site with one page importing a sample and its manifest.
seed() {
	rm -rf "$tmp/site"
	mkdir -p "$tmp/site/docs/guides" "$tmp/site/examples/nodes/testdata"
	cat >"$tmp/site/docs/guides/nodes.mdx" <<'PAGE'
import Drain from '!!raw-loader!@site/examples/nodes/drain_test.go';
import Manifest from '!!raw-loader!@site/examples/nodes/testdata/node.yaml';
PAGE
	echo 'package nodes_test' >"$tmp/site/examples/nodes/drain_test.go"
	echo 'kind: Node' >"$tmp/site/examples/nodes/testdata/node.yaml"
	printf 'module example\n' >"$tmp/site/examples/go.mod"
	printf 'example v0\n' >"$tmp/site/examples/go.sum"
}

run() {
	status=0
	(cd "$tmp/site" && NPX="$tmp/npx" "$script" "$@") >"$tmp/out.log" 2>&1 || status=$?
}

case="freezes the docs with their own copy of the samples"
seed
run 0.37
[[ $status == 0 ]] || fail "exit $status: $(cat "$tmp/out.log")"
frozen="$tmp/site/versioned_docs/version-0.37/guides/nodes.mdx"
grep -qF "@site/_versioned_examples/version-0.37/nodes/drain_test.go" "$frozen" || fail "import not rewritten: $(cat "$frozen")"
grep -qF "@site/_versioned_examples/version-0.37/nodes/testdata/node.yaml" "$frozen" || fail "manifest import not rewritten"
grep -qF "@site/examples/" "$frozen" && fail "frozen page still imports live samples"
[[ -f "$tmp/site/_versioned_examples/version-0.37/nodes/drain_test.go" ]] || fail "sample not copied"
[[ -f "$tmp/site/_versioned_examples/version-0.37/nodes/testdata/node.yaml" ]] || fail "testdata not copied"
[[ -e "$tmp/site/_versioned_examples/version-0.37/go.mod" ]] && fail "go.mod copied: the frozen copy must not be a module"
grep -qF "@site/examples/nodes/drain_test.go" "$tmp/site/docs/guides/nodes.mdx" || fail "current docs were rewritten"

case="freezes docs whose pages import no samples"
seed
printf '# Plain page\n' >"$tmp/site/docs/guides/nodes.mdx"
run 0.37
[[ $status == 0 ]] || fail "exit $status: $(cat "$tmp/out.log")"
grep -qF 'froze docs 0.37' "$tmp/out.log" || fail "stopped before finishing: $(cat "$tmp/out.log")"

case="refuses a version that is not 0.N"
seed
run 1.37
[[ $status != 0 ]] || fail "accepted 1.37"
run 0.37.1
[[ $status != 0 ]] || fail "accepted 0.37.1"

case="refuses to freeze the same version twice"
seed
run 0.37
run 0.37
[[ $status != 0 ]] || fail "froze 0.37 twice"

case="fails when a frozen page reaches live samples another way"
seed
echo "import X from '!!raw-loader!../../examples/nodes/drain_test.go';" >>"$tmp/site/docs/guides/nodes.mdx"
run 0.37
[[ $status != 0 ]] || fail "accepted a relative import of a live sample"
grep -q 'nodes.mdx' "$tmp/out.log" || fail "error does not name the page: $(cat "$tmp/out.log")"

if ((failures > 0)); then
	echo "$failures check(s) failed"
	exit 1
fi
echo "ok: all version-docs cases passed"
