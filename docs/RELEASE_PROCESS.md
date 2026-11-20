# Release process

This Go module uses [Release Please](https://github.com/googleapis/release-please)
with the **go** release type to *propose* releases. Release Please never tags
or publishes: a Go module version exists once its `vX.Y.Z` tag is pushed and
the Go module proxy fetches it, so tagging stays a maintainer step. The
versioning rules and the release gate are defined in
[RELEASE_POLICY.md](RELEASE_POLICY.md); this page describes the mechanics.

## Overview

1. Contributors merge PRs whose titles follow Conventional Commits.
2. On every push to `main`, [`release-please.yml`](../.github/workflows/release-please.yml)
   creates or updates one **Release PR** (`chore(main): release X.Y.Z`) that
   changes only `CHANGELOG.md` and `.release-please-manifest.json`.
3. A maintainer merges the curated release note, finishes and reviews the
   Release PR, runs the release preflight and merges the Release PR.
4. The maintainer tags `vX.Y.Z` on that merge commit and creates the GitHub
   Release. The Go proxy picks up the tag.

## Commit titles drive the version and the changelog

With the [required repository settings](#repository-settings) (squash merge
only, squash commit title = PR title, blank squash body), the PR title becomes
the whole commit message on `main`. If those settings are changed, a merge
commit, a rebase merge or a single-commit squash merge puts the branch's own
commit messages on `main`, and those bypass the title check. Release Please
skips subjects it cannot parse, so such changes would be missing from the
CHANGELOG.

[`pr-title.yml`](../.github/workflows/pr-title.yml) requires
`type: description` or `type(scope): description`, with `type` one of
`feat fix docs style refactor perf test build ci chore revert`.

| Title | Version bump while 0.x | From 1.0 onward | CHANGELOG section |
| --- | --- | --- | --- |
| `fix:` | patch | patch | Bug Fixes |
| `feat:` | minor | minor | Features |
| `type!:` or a `BREAKING CHANGE:` footer | **minor** | major | BREAKING CHANGES |
| `perf:`, `revert:` | patch | patch | Performance Improvements, Reverts |
| `docs:`, `refactor:`, `test:`, `build:`, `ci:`, `style:`, `chore:` | none | none | hidden |

`bump-minor-pre-major` is enabled in
[`release-please-config.json`](../release-please-config.json): before 1.0 a
breaking change bumps the minor version, as RELEASE_POLICY.md requires
("MINOR with explicit migration notes"). Without it Release Please would
propose 1.0.0, which needs the separate 1.0 release gate. A 1.0 release is
proposed only on purpose, with a `Release-As: 1.0.0` commit footer.

Hidden types do not trigger a Release PR on their own, matching the policy
that internal-only changes need no release. A hidden type marked breaking
(`chore!:`) is listed twice: under BREAKING CHANGES and under its own hidden
section (for example Miscellaneous Chores).

**Do not edit `CHANGELOG.md` in feature or fix PRs.** Release Please writes it
from the merged titles. Describe user-visible behavior, migration steps and
evidence in the PR description; the maintainer carries them into the release
notes.

## Baseline

`.release-please-manifest.json` starts at `0.2.1`, the latest published tag.
Release Please finds the GitHub Release whose tag is `v0.2.1`
(`include-component-in-tag: false`, default `v` prefix) and collects commits
after it, so no `bootstrap-sha` or `last-release-sha` is configured.

## One release at a time

Two mechanisms keep Release Please from proposing the next version before the
current one is tagged:

1. **Tag guard (primary).** The workflow reads the version from
   `.release-please-manifest.json` and runs Release Please only if the tag
   `v<version>` exists. After a Release PR is merged and before it is tagged,
   every run ends with a notice and proposes nothing.
2. **Release Please label (secondary).** Release Please does not open a new
   Release PR while a merged one still carries `autorelease: pending`. It
   recognizes that PR only while its body still parses as a Release Please
   body, so an edited body silently disables this block. **Do not edit the
   Release PR's title or description; change only its files.** The workflow
   also relabels the PR by its exact title once the tag exists (step 6).

## Releasing a version

### 1. Add the curated release note first

While the Release PR is open, add `docs/releases/vX.Y.Z.md` in a normal
`docs:` PR and merge it before the Release PR, so the tagged commit contains
the note. See [RELEASE_NOTES_QUALITY.md](RELEASE_NOTES_QUALITY.md).

- The note is not a copy of the CHANGELOG section.
  `scripts/release_notes_quality.py` (run on the PR) rejects generated `* `
  bullets and section names such as `BREAKING CHANGES` (use `Upgrade notes`),
  and each entry needs a `by @username` credit.
- `sync-release-notes.yml` runs only **after** the merge, on `main`, and fails
  if any note contains `github.com/cubrid-labs/`. The PR check does not catch
  this. In the note, name the old module path without the `github.com/`
  prefix, or link to the CHANGELOG upgrade notes.
- Date the note's heading (`## vX.Y.Z (YYYY-MM-DD)`) with the planned tag
  date. If the tag slips to another day, correct the date in a follow-up
  `docs:` PR; after it merges, `sync-release-notes.yml` updates the existing
  GitHub Release description.
- Merging the note before the tag exists is safe: the sync workflow skips
  versions that have no GitHub Release.
- For the first release under the new module path, repoint the README's
  "Import path change" link from `CHANGELOG.md#upgrade-notes` to
  `docs/releases/vX.Y.Z.md#upgrade-notes` in the same PR. The CHANGELOG anchor
  moves once a later version adds its own `### Upgrade notes`.

The merge pushes to `main` and regenerates the Release PR, so do the next step
after it.

### 2. Finish the Release PR

Every push to `main` rebuilds the Release PR branch and discards edits made
on it. Edit the files last, after the final change for this release is merged,
and check them again before merging.

- Check the proposed version against RELEASE_POLICY.md and the merged changes.
- Reword generated entries that are not good user-facing descriptions, and
  remove entries that are not user-visible (for example `fix(ci):`).
- Remove the duplicate entry of a breaking hidden-type commit (`chore!:`): keep
  it under BREAKING CHANGES, delete it from Miscellaneous Chores.
- With the default `GITHUB_TOKEN`, PRs created by Actions do not trigger
  `pull_request` workflows. Close and reopen the Release PR to start CI, or
  configure a `RELEASE_PLEASE_TOKEN` secret (see
  [Repository settings](#repository-settings)).

#### First Release PR: fold the curated `[Unreleased]` section

Before Release Please was adopted, `CHANGELOG.md` was edited by hand, and its
`## [Unreleased]` section holds curated entries, including the upgrade notes
for the module path move to `github.com/cubrid-lab/cubrid-go`. Release Please
inserts its new version section above the first version heading, which is
`## [Unreleased]`, and leaves the rest of the file unchanged.

In the first Release PR the maintainer must:

1. Move the `[Unreleased]` content into the new version section: keep
   `### Upgrade notes` first, then merge `### Fixed` and `### Added` with the
   generated `### Bug Fixes` and `### Features`, so each change appears once.
2. Remove the duplicate `chore!:` module-move entry (see above).
3. Delete the `## [Unreleased]` heading. From then on the file has no
   Unreleased section; Release Please inserts each new version above the
   previous one.
4. Decide whether to add the missing `## [0.2.0]` and `## [0.2.1]` sections:
   those releases are documented only in `docs/releases/v0.2.0.md` and
   `docs/releases/v0.2.1.md`, so the CHANGELOG jumps from 0.1.0 to the new
   version.

Do not merge a Release PR that shows the same change twice or leaves the old
`[Unreleased]` section below a released version.

### 3. Preflight and merge

Run the release preflight from [RELEASE_POLICY.md](RELEASE_POLICY.md) on the
Release PR head, including the real CUBRID gate, then squash-merge it. Before
tagging, confirm that the merge commit has the reviewed content:
`git diff <release-pr-head> <merge-commit>` must be empty.

### 4. Tag and create the GitHub Release

```sh
git fetch origin && git checkout <merge-commit>
git tag -a vX.Y.Z -m "cubrid-go vX.Y.Z"
git push origin vX.Y.Z
gh release create vX.Y.Z --verify-tag --title "cubrid-go vX.Y.Z" \
  --notes-file docs/releases/vX.Y.Z.md
```

Never move or delete a pushed tag: the Go checksum database records it.

### 5. Verify the published module

`GOPROXY=https://proxy.golang.org go list -m github.com/cubrid-lab/cubrid-go@vX.Y.Z`
must resolve. For the first tag under `github.com/cubrid-lab/cubrid-go`, run
the full proxy and consumer preflight in
[#83](https://github.com/cubrid-lab/cubrid-go/issues/83): a fresh external
module must `go get` the tag and build with the new import path and the
`dialector` package, and requesting the tag through the old path must fail
with "module declares its path as".

### 6. Mark the Release PR as tagged

The next run of `release-please.yml` (the next push to `main`, or
**Actions → Prepare reviewed Release PR → Run workflow**) does this
automatically: once the tag guard passes, it finds merged PRs labelled
`autorelease: pending` whose title is exactly `chore(main): release X.Y.Z` for
the tagged version, removes `autorelease: pending` and adds
`autorelease: tagged`. It then proposes the next Release PR when there are
releasable changes. Run the workflow by hand after tagging to clear the label
right away.

If the step fails or warns, relabel manually:

```sh
gh pr edit <release-pr> --remove-label "autorelease: pending" --add-label "autorelease: tagged"
```

Both labels are defined in [`.github/labels.yml`](../.github/labels.yml).

## Repository settings

**Required merge settings** (Settings → General → Pull Requests). These are
the current values; keep them, because every commit on `main` must be a
checked PR title:

| Setting | Value | Why |
| --- | --- | --- |
| `allow_squash_merge` | `true` | One commit per PR |
| `allow_merge_commit` | `false` | Merge commits bring the branch's unchecked commits to `main` |
| `allow_rebase_merge` | `false` | Same for rebase merges |
| `squash_merge_commit_title` | `PR_TITLE` | `COMMIT_OR_PR_TITLE` would use the commit subject of a single-commit PR and skip the title check |
| `squash_merge_commit_message` | `BLANK` | Release Please also parses Conventional Commit lines in a squash body, so a list of branch commits could add unintended CHANGELOG entries and version bumps |

**Optional:** make **Check PR title / title** a required status check on
`main`.

**Actions:**

- Settings → Actions → General → Workflow permissions: **Allow GitHub Actions
  to create and approve pull requests** must stay enabled.
- Optional `RELEASE_PLEASE_TOKEN` secret: a fine-grained PAT limited to this
  repository with Contents and Pull requests read/write. With it, the Release
  PR triggers CI automatically. Without it the workflow uses `GITHUB_TOKEN`
  and CI must be started by closing and reopening the Release PR.
