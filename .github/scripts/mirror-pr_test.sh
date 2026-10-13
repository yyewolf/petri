#!/usr/bin/env bash
# Scenario test for mirror-pr.sh against a local remote and a stubbed gh. Run
# from the repository root.
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/mirror-pr.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

git init -q --bare -b main "$tmp/remote.git"
git clone -q "$tmp/remote.git" "$tmp/seed" 2>/dev/null
echo v1.37.1 >"$tmp/seed/mirror.txt"
git -C "$tmp/seed" add mirror.txt
git -C "$tmp/seed" -c user.name=t -c user.email=t@t commit -q -m seed
git -C "$tmp/seed" push -q origin main

# The stub logs every call; `pr list` answers with $tmp/open_pr ("<number>
# <isDraft>"), or nothing when no PR is open.
cat >"$tmp/gh" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$tmp/gh.log"
if [[ "\$1 \$2" == "pr list" && -s "$tmp/open_pr" ]]; then cat "$tmp/open_pr"; fi
EOF
chmod +x "$tmp/gh"

failures=0
runs=0
fail() {
	echo "FAIL: $case: $*"
	failures=$((failures + 1))
}

# run <version> <content> <mirror outcome> <test outcome> <open pr> [kind]
# [submodule outcome] [kine version]: regenerate in a fresh clone of main and run the script;
# sets $status.
run() {
	local version="$1" content="$2" mirror="$3" tests="$4"
	printf '%s' "$5" >"$tmp/open_pr"
	: >"$tmp/gh.log"
	rm -rf "$tmp/work"
	git clone -q "$tmp/remote.git" "$tmp/work" 2>/dev/null
	if [[ -n "$content" ]]; then echo "$content" >"$tmp/work/mirror.txt"; fi
	# A distinct commit date per run, so a repeated push changes the remote SHA.
	runs=$((runs + 1))
	export GIT_AUTHOR_DATE="@$((1700000000 + runs)) +0000" GIT_COMMITTER_DATE="@$((1700000000 + runs)) +0000"
	status=0
	(cd "$tmp/work" && BRANCH=main VERSION="$version" KIND="${6:-patch}" MIRROR_OUTCOME="$mirror" \
		TEST_OUTCOME="$tests" REMOTE="$tmp/remote.git" GH="$tmp/gh" SUBMODULE_OUTCOME="${7:-}" KINE="${8:-}" "$script") \
		>"$tmp/out.log" 2>&1 || status=$?
}
remote_head() { git -C "$tmp/remote.git" rev-parse --verify -q refs/heads/mirror/main || true; }
logged() { grep -qF -- "$1" "$tmp/gh.log"; }

case="first update opens a PR from mirror/main"
run v1.37.2 v1.37.2 success success ""
[[ $status == 0 ]] || fail "exit $status: $(cat "$tmp/out.log")"
[[ -n "$(remote_head)" ]] || fail "mirror/main not pushed"
logged "pr create --base main --head mirror/main --title build: mirror Kubernetes v1.37.2" || fail "no pr create: $(cat "$tmp/gh.log")"
logged "--draft" && fail "opened as draft"
pushed=$(remote_head)

case="unchanged rerun does not push"
run v1.37.2 v1.37.2 success success "7 false"
[[ $status == 0 ]] || fail "exit $status: $(cat "$tmp/out.log")"
[[ "$(remote_head)" == "$pushed" ]] || fail "mirror/main was pushed again"
logged "pr create" && fail "opened a second PR"
logged "pr ready" && fail "changed draft state"

case="unchanged content with no open PR leaves a closed PR closed"
run v1.37.2 v1.37.2 success success ""
[[ $status == 0 ]] || fail "exit $status: $(cat "$tmp/out.log")"
logged "pr create" && fail "reopened a PR: $(cat "$tmp/gh.log")"

case="newer version updates the open PR and marks it draft when tests fail"
run v1.37.3 v1.37.3 success failure "7 false"
[[ $status == 0 ]] || fail "exit $status: $(cat "$tmp/out.log")"
[[ "$(remote_head)" != "$pushed" ]] || fail "mirror/main not updated"
logged "pr edit 7 --title build: mirror Kubernetes v1.37.3" || fail "PR not retitled: $(cat "$tmp/gh.log")"
logged "pr ready 7 --undo" || fail "PR not moved to draft"
logged "pr create" && fail "opened a second PR"

case="fixed update marks the draft PR ready"
run v1.37.3 v1.37.3 success success "7 true"
logged "pr ready 7" || fail "PR not marked ready: $(cat "$tmp/gh.log")"
logged "--undo" && fail "PR moved to draft"

case="broken first update opens a draft PR"
git -C "$tmp/remote.git" update-ref -d refs/heads/mirror/main
run v1.37.4 v1.37.4 failure "" ""
logged "--draft" || fail "not a draft: $(cat "$tmp/gh.log")"
grep -qF 'with `skipped`' "$tmp/gh.log" || fail "body does not report skipped tests"

case="no regenerated changes fails"
run v1.37.5 "" success success ""
[[ $status != 0 ]] || fail "exited 0"
logged "pr " && fail "called gh"

case="minor update with a bumped sub-module says so and stays ready"
run v1.38.0 v1.38.0 success success "" minor success
[[ $status == 0 ]] || fail "exit $status: $(cat "$tmp/out.log")"
logged "pr create --base main --head mirror/main --title build: mirror Kubernetes v1.38.0" || fail "no pr create"
logged "--draft" && fail "opened as draft"
grep -qF "controllerruntime sub-module was bumped" "$tmp/gh.log" || fail "body does not report the sub-module bump"
[[ "$(git -C "$tmp/remote.git" log -1 --format=%B mirror/main)" == *"Release-As: 0.38.0"* ]] ||
	fail "commit lacks the Release-As footer: $(git -C "$tmp/remote.git" log -1 --format=%B mirror/main)"

case="minor update without a controller-runtime release opens a draft and asks for a human"
run v1.38.1 v1.38.1 success success "8 false" minor none
[[ $status == 0 ]] || fail "exit $status: $(cat "$tmp/out.log")"
logged "pr ready 8 --undo" || fail "PR not moved to draft"
grep -qF "controller-runtime for this minor is not released" "$tmp/gh.log" || fail "body does not report the missing release"

case="minor update with a skipped or failed bump notes it"
run v1.38.2 v1.38.2 success success "" minor skipped
grep -qF "could not be bumped" "$tmp/gh.log" || fail "body does not report the skipped bump"
logged "pr ready" && fail "changed draft state for a broken PR"
run v1.38.2 v1.38.2 success success "9 false" minor failed
logged "pr ready 9 --undo" || fail "failed bump did not open as draft"

case="patch update does not mention the sub-module"
run v1.37.6 v1.37.6 success success "" patch success
grep -qF "controllerruntime" "$tmp/gh.log" && fail "body mentions the sub-module on a patch"
[[ "$(git -C "$tmp/remote.git" log -1 --format=%B mirror/main)" == *Release-As* ]] && fail "patch commit carries Release-As"

case="kine-only update is titled after kine and names both versions in the command"
git -C "$tmp/remote.git" update-ref -d refs/heads/mirror/main
run v1.37.6 kine-v0.17.3 success success "" kine "" v0.17.3
[[ $status == 0 ]] || fail "exit $status: $(cat "$tmp/out.log")"
logged "--title build: mirror kine v0.17.3 " || fail "wrong title: $(cat "$tmp/gh.log")"
grep -qF 'make mirror K8S_VERSION=v1.37.6 KINE_VERSION=v0.17.3' "$tmp/gh.log" || fail "body lacks the kine command"

case="Kubernetes and kine together name both in the title"
run v1.37.7 both success success "10 false" patch "" v0.17.4
logged "pr edit 10 --title build: mirror Kubernetes v1.37.7 and kine v0.17.4" || fail "wrong title: $(cat "$tmp/gh.log")"

if ((failures > 0)); then
	echo "$failures check(s) failed"
	exit 1
fi
echo "ok: all mirror PR cases passed"
