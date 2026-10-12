#!/usr/bin/env bash
# Commits a regenerated mirror and opens or updates the pull request for one
# target branch. Run from the repository root after `make mirror`.
#
# Environment:
#   BRANCH           target branch (main or release-1.N)
#   VERSION          Kubernetes version mirrored, such as v1.37.2
#   KIND             patch, minor, or kine (only kine moved)
#   KINE             kine version mirrored, when kine moved (optional)
#   MIRROR_OUTCOME   outcome of `make mirror`
#   TEST_OUTCOME     outcome of the tests; empty when they were skipped
#   SUBMODULE_OUTCOME outcome of the controllerruntime bump on a minor move:
#                    success, none (no controller-runtime release yet), skipped
#   REMOTE           URL to push to
#   RUN_URL          link to the workflow run (optional)
#   GH               gh binary to use (optional; tests stub it)
set -euo pipefail

: "${BRANCH:?}" "${VERSION:?}" "${KIND:?}" "${MIRROR_OUTCOME:?}" "${REMOTE:?}"
gh=${GH:-gh}

kine=${KINE:-}
command="make mirror K8S_VERSION=$VERSION${kine:+ KINE_VERSION=$kine}"

if [[ -z "$(git status --porcelain)" ]]; then
	echo "::error::$command changed nothing; see the 'Regenerate the mirror' step." >&2
	exit 1
fi

# One PR per target branch: a newer version updates the pending PR instead of
# opening a second one next to it.
head="mirror/$BRANCH"
if [[ "$KIND" == kine ]]; then
	title="build: mirror kine $kine"
elif [[ -n "$kine" ]]; then
	title="build: mirror Kubernetes $VERSION and kine $kine"
else
	title="build: mirror Kubernetes $VERSION"
fi

git switch -q -C "$head"
git add -A
git -c user.name=petri-mirror -c user.email=petri-mirror@users.noreply.github.com commit -q -m "$title"

# Skip the push when the branch already holds this exact content, so the daily
# run does not re-trigger CI on an unchanged PR.
changed=true
if git fetch -q "$REMOTE" "refs/heads/$head" 2>/dev/null &&
	[[ "$(git rev-parse 'FETCH_HEAD^{tree}')" == "$(git rev-parse 'HEAD^{tree}')" ]]; then
	changed=false
	echo "$head already holds this content; not pushing."
else
	git push -q --force "$REMOTE" "HEAD:refs/heads/$head"
fi

body="Regenerated with \`$command\` by the Mirror workflow."
if [[ "$KIND" == minor ]]; then
	body="$body

This moves \`$BRANCH\` to a new Kubernetes minor. The previous minor continues on its \`release-1.N\` branch. Check whether the Go version or kine also need a bump."

	case "${SUBMODULE_OUTCOME:-}" in
	success)
		body="$body

The controllerruntime sub-module was bumped to the new minor."
		;;
	none)
		body="$body

controller-runtime for this minor is not released yet, so the controllerruntime sub-module still needs its k8s.io and controller-runtime requirements bumped once it ships."
		;;
	skipped|failed)
		body="$body

The controllerruntime sub-module could not be bumped; its k8s.io and controller-runtime requirements still match the previous minor."
		;;
	esac
fi

broken=false
if [[ "$MIRROR_OUTCOME" != success || "${TEST_OUTCOME:-}" != success ]]; then
	broken=true
	body="$body

**Needs a human:** \`make mirror\` finished with \`$MIRROR_OUTCOME\` and \`go test -race ./...\` with \`${TEST_OUTCOME:-skipped}\`${RUN_URL:+ in [this run]($RUN_URL)}."
elif [[ "$KIND" == minor && "${SUBMODULE_OUTCOME:-}" != success && "${SUBMODULE_OUTCOME:-}" != "" ]]; then
	broken=true
	body="$body

**Needs a human:** the controllerruntime sub-module bump did not complete (outcome: ${SUBMODULE_OUTCOME:-skipped})${RUN_URL:+, see [this run]($RUN_URL)}."
fi

read -r number is_draft < <("$gh" pr list --head "$head" --base "$BRANCH" --state open \
	--json number,isDraft --jq '.[0] | select(.) | "\(.number) \(.isDraft)"') || true

if [[ -z "${number:-}" ]]; then
	if [[ "$changed" == false ]]; then
		# Same content as a PR someone closed: leave it closed.
		echo "No open PR for $head and nothing new to propose."
		exit 0
	fi

	draft=()
	if [[ "$broken" == true ]]; then draft=(--draft); fi
	"$gh" pr create --base "$BRANCH" --head "$head" --title "$title" --body "$body" "${draft[@]}"
	exit 0
fi

"$gh" pr edit "$number" --title "$title" --body "$body"
if [[ "$broken" == true && "$is_draft" == false ]]; then
	"$gh" pr ready "$number" --undo
elif [[ "$broken" == false && "$is_draft" == true ]]; then
	"$gh" pr ready "$number"
fi
