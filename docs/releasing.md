# Releasing petri

Releases are automated. A human merges a release PR; everything else is done
by the [Release workflow](../.github/workflows/release.yml), which runs
[release-please](https://github.com/googleapis/release-please) and then tags
the `controllerruntime` sub-module.

## Versions

petri versions follow the `k8s.io` staging modules (`k8s.io/client-go` and
the rest):

- `v0.<k8s minor>.<patch>`: petri `v0.37.x` embeds Kubernetes 1.37 and is used
  with `k8s.io/client-go v0.37.x`. The first release is `v0.37.0`.
- The patch number goes up for every petri release, whatever the change. A
  breaking change is a patch too, as in the staging modules.
- There is no `v1`, and never a `v0.N` that does not match a Kubernetes minor.
  `KubernetesVersion` in [version.go](../version.go) names the minor of the
  release line; the workflows refuse any tag that disagrees with it.
- The `controllerruntime` sub-module is tagged `controllerruntime/v0.N.P`
  with every `v0.N.P`, and that tag's go.mod requires petri `v0.N.P`.

## Branches

- `main` tracks the newest supported Kubernetes minor.
- `release-1.N` branches carry older minors. The Mirror workflow cuts
  `release-1.N` from `main` when it moves `main` to the next minor, and keeps
  every line on its newest Kubernetes patch.
- Each of these branches has its own release PR and its own tags.

## How a release happens

1. **Commits land.** Every commit on `main` and `release-1.N` follows
   [Conventional Commits](https://www.conventionalcommits.org/). The type
   decides where it goes in the changelog:

   | Type | Changelog section | Triggers a release |
   |---|---|---|
   | `feat` | Features | yes |
   | `fix` | Bug Fixes | yes |
   | `perf` | Performance | yes |
   | `build` | Kubernetes, kine and dependencies | yes |
   | `refactor` | Refactoring | yes |
   | `docs` | Documentation | yes |
   | `test`, `ci`, `chore` | hidden | no |

   Mirror updates are `build:` commits, so a Kubernetes or kine patch release
   shows up in the next petri release on its own.

2. **release-please keeps a release PR open.** After each push, it opens or
   updates the PR `chore(<branch>): release 0.37.N` from `release-please--branches--<branch>`,
   with the `CHANGELOG.md` section for the commits since the last release.
   Edit the PR to reword the changelog if needed. CI checks that the PR's
   version belongs to the release line in `version.go`.

3. **Merge the release PR.** The Release workflow then:
   1. tags `v0.37.N` on the merge commit and publishes its GitHub release with
      the changelog section as notes;
   2. points `controllerruntime/go.mod` at the new tag (`go mod edit` and
      `go mod tidy` without the workspace), builds and vets the sub-module
      as users will get it;
   3. commits that as `chore: release controllerruntime/v0.37.N`, tags it
      `controllerruntime/v0.37.N`, pushes both, and publishes its release.

   Releases from a `release-1.N` branch are never marked as the latest
   GitHub release.

The sub-module needs its own commit because a go.mod cannot require a tag that
does not exist yet: even inside the workspace, the go command reads the
required version's go.mod. So `controllerruntime/go.mod` keeps requiring the
previous petri release between releases, and the workspace (`go.work`) builds
it against the working tree.

## A new Kubernetes minor

When the Mirror workflow moves `main` to Kubernetes 1.38, its commit carries a
`Release-As: 0.38.0` footer, so the next release PR proposes `v0.38.0` instead
of the next 1.37 patch. If that footer gets lost, for example because the
merge rewrote the message, the release PR's CI check fails. Fix it with an
empty commit:

```sh
git commit --allow-empty -m "chore: release 0.38.0" -m "Release-As: 0.38.0"
```

## Docs for each release line

The documentation site (`website/`) is versioned by release line, like the
code. `main`'s docs are the current version, labelled from `version.go`. When
the Mirror workflow moves `main` to a new minor, the same PR runs
`website/scripts/version-docs.sh 0.N` for the outgoing line: Docusaurus freezes
the pages under `website/versioned_docs/version-0.N/`, and the samples they show
are copied to `website/_versioned_examples/version-0.N/` so those pages keep
the code of their own line. If the freeze fails, the PR opens as a draft that
says so; run the script by hand from `website/` and push the result to the PR.

Only `main` deploys the site. Fix an older line's docs in its
`versioned_docs/version-0.N/` folder on `main`.

## Setup

There is no secret to manage: the workflows use their own `GITHUB_TOKEN`. The
repository needs one setting, under *Settings → Actions → General → Workflow
permissions*: **Allow GitHub Actions to create and approve pull requests**, so
release-please and the Mirror workflow can open their PRs. Each workflow
declares the permissions it needs, so the default token permission can stay
read-only.

GitHub starts no workflow for pushes and PRs made with that token, so the bot
PRs would get no CI. The Release and Mirror workflows therefore dispatch CI
(`gh workflow run ci.yml --ref <branch>`) on the branch they pushed; its
checks attach to the head commit and show on the PR. To re-run CI on a bot PR
by hand, do the same from the Actions tab or with `gh`.

While the repository is private, the Release workflow fetches petri from git
rather than the module proxy (`GOPRIVATE`). Nothing needs to change when it
goes public.

## When something goes wrong

- **The controllerruntime job failed.** petri `v0.37.N` is out, but the
  sub-module is not. Fix the cause, then either re-run the job, or do its
  steps by hand on the release branch:

  ```sh
  cd controllerruntime
  GOWORK=off go mod edit -require=github.com/yyewolf/petri@v0.37.N
  GOWORK=off go mod tidy
  git commit -am "chore: release controllerruntime/v0.37.N"
  git tag controllerruntime/v0.37.N
  git push origin HEAD controllerruntime/v0.37.N
  gh release create controllerruntime/v0.37.N --verify-tag --latest=false --notes "…"
  ```

  The push fails if the branch moved after the release merged; re-running the
  job picks up the new head.
- **A wrong version was tagged.** Go modules are immutable once the proxy has
  seen them: do not move or delete a published tag. Release the next patch
  with the fix, and retract the bad version in go.mod (`retract v0.37.N`) if
  it must not be used.
- **Never push release tags by hand** outside the recovery above.
  release-please finds the previous release through its own tags and
  changelog, and a stray tag confuses it.
