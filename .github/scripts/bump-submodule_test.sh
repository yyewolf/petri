#!/usr/bin/env bash
# Scenario test for bump-submodule.sh with a stubbed go command. Run from the
# repository root.
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/bump-submodule.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

failures=0
fail() {
	echo "FAIL: $case: $*"
	failures=$((failures + 1))
}

# stub_go writes a go stub whose `list -m -versions` answer and controller-runtime
# .mod fixtures are parameterized: <newest-mod-minor>.
stub_go() {
	cat >"$tmp/go" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$tmp/go.log"
case "\$1 \$2" in
"list -m")
	printf 'sigs.k8s.io/controller-runtime v0.24.0 v0.25.2 v0.26.0\n'
	;;
"mod download")
	version=\${4##*@}
	case "\$version" in
	v0.26.0) echo '{"GoMod": "$tmp/cr026.mod"}' ;;
	v0.25.2) echo '{"GoMod": "$tmp/cr025.mod"}' ;;
	*) echo '{"GoMod": "$tmp/cr026.mod"}' ;;
	esac
	;;
esac
EOF
	printf 'module sigs.k8s.io/controller-runtime\n\nrequire k8s.io/client-go %s\n' "$1" >"$tmp/cr026.mod"
	printf 'module sigs.k8s.io/controller-runtime\n\nrequire k8s.io/client-go %s\n' "$2" >"$tmp/cr025.mod"
	chmod +x "$tmp/go"
}

# seed writes a repo root with a mirrored root go.mod and a sub-module go.mod
# still on the previous minor.
seed() {
	printf 'module github.com/yyewolf/petri\n\ngo 1.26.5\n\nrequire (\n\tk8s.io/api %s\n\tk8s.io/apimachinery %s\n\tk8s.io/client-go %s\n)\n' \
		"$1" "$1" "$1" >"$tmp/go.mod"
	mkdir -p "$tmp/controllerruntime"
	printf 'module github.com/yyewolf/petri/controllerruntime\n\ngo 1.26.5\n\nrequire (\n\tgithub.com/yyewolf/petri v0.37.2\n\tk8s.io/api %s\n\tk8s.io/apimachinery %s\n\tk8s.io/client-go %s\n)\n' \
		"$2" "$2" "$2" >"$tmp/controllerruntime/go.mod"
}

run() {
	: >"$tmp/go.log"
	status=0
	(cd "$tmp" && GO="$tmp/go" "$script" "$1") >"$tmp/out.log" 2>&1 || status=$?
}

case="newest controller-runtime matching the minor is picked"
stub_go v0.38.0 v0.37.0
seed v0.38.1 v0.37.1
run v1.38.1
[[ $status == 0 ]] || fail "exit $status: $(cat "$tmp/out.log")"
grep -qF "get sigs.k8s.io/controller-runtime@v0.26.0" "$tmp/go.log" || fail "wrong controller-runtime: $(grep get "$tmp/go.log" | head -2)"
grep -qF "get k8s.io/api@v0.38.1" "$tmp/go.log" || fail "staging modules not bumped"
grep -qF "github.com/yyewolf/petri v0.38.0" "$tmp/controllerruntime/go.mod" || fail "petri require not bumped: $(cat "$tmp/controllerruntime/go.mod")"

case="a pseudo-version petri require is replaced whole"
# A pre-release go.mod can pin petri at a pseudo-version; the replacement must
# cover the entire version token, not just its tag-shaped prefix.
stub_go v0.38.0 v0.37.0
seed v0.38.1 v0.37.1
sed -i 's|github.com/yyewolf/petri v0.37.2|github.com/yyewolf/petri v0.0.0-20261011200139-b7f39b53678a|' "$tmp/controllerruntime/go.mod"
run v1.38.1
[[ $status == 0 ]] || fail "exit $status: $(cat "$tmp/out.log")"
grep -qF "github.com/yyewolf/petri v0.38.0" "$tmp/controllerruntime/go.mod" || fail "pseudo-version not replaced: $(grep yyewolf "$tmp/controllerruntime/go.mod")"
grep -qF "v0.38.0-20261011200139" "$tmp/controllerruntime/go.mod" && fail "replacement corrupted a pseudo-version: $(grep yyewolf "$tmp/controllerruntime/go.mod")"

case="an older controller-runtime release stops the search"
# v0.26.0 requires 0.37 (older than the target minor): nothing matches.
stub_go v0.37.0 v0.37.0
seed v0.38.1 v0.37.1
run v1.38.1
[[ $status == 2 ]] || fail "exit $status, want 2: $(cat "$tmp/out.log")"
grep -qF "github.com/yyewolf/petri v0.37.2" "$tmp/controllerruntime/go.mod" || fail "go.mod was touched on failure"

case="a newer-than-target controller-runtime is skipped"
# v0.26.0 requires 0.39 (newer than the target minor); v0.25.2 requires 0.38.
stub_go v0.39.0 v0.38.0
seed v0.38.1 v0.37.1
run v1.38.1
[[ $status == 0 ]] || fail "exit $status: $(cat "$tmp/out.log")"
grep -qF "get sigs.k8s.io/controller-runtime@v0.25.2" "$tmp/go.log" || fail "wrong controller-runtime: $(grep get "$tmp/go.log" | head -2)"

case="bad arguments fail"
stub_go v0.38.0 v0.37.0
seed v0.38.1 v0.37.1
run v1
[[ $status != 0 ]] || fail "exited 0"

case="a root go.mod that does not match the minor fails"
stub_go v0.38.0 v0.37.0
seed v0.37.1 v0.37.1
run v1.38.1
[[ $status != 0 ]] || fail "exited 0"
grep -qF "make mirror" "$tmp/out.log" || fail "error does not point at make mirror"

if ((failures > 0)); then
	echo "$failures case(s) failed"
	exit 1
fi
echo "ok: all bump-submodule cases passed"
