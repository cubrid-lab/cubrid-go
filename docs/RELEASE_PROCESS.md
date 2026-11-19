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
3. A maintainer reviews the Release PR, runs the release preflight and merges it.
4. The maintainer tags `vX.Y.Z` on the merge commit and creates the GitHub
   Release. The Go proxy picks up the tag.

## Commit titles drive the version and the changelog

PRs are squash-merged, so the PR title becomes the commit subject on `main`.
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
(`chore!:`) still appears under BREAKING CHANGES.

**Do not edit `CHANGELOG.md` in feature or fix PRs.** Release Please writes it
from the merged titles. Describe user-visible behavior, migration steps and
evidence in the PR description; the maintainer carries them into the release
notes.

## Baseline

`.release-please-manifest.json` starts at `0.2.1`, the latest published tag.
Release Please finds the GitHub Release whose tag is `v0.2.1`
(`include-component-in-tag: false`, default `v` prefix) and collects commits
after it, so no `bootstrap-sha` or `last-release-sha` is configured.

## Reviewing the Release PR

- Check the proposed version against RELEASE_POLICY.md and the merged changes.
- Edit the generated section where a commit subject is not a good user-facing
  description; remove entries that are not user-visible (for example `fix(ci):`).
- Every push to `main` regenerates the Release PR branch from scratch and
  discards edits made on it. Make the CHANGELOG edits last, after the final
  change for this release is merged, and check them again before merging.
- With the default `GITHUB_TOKEN`, PRs created by Actions do not trigger
  `pull_request` workflows. Run CI on the Release PR manually (for example
  close and reopen it, or push an empty commit to its branch), or configure a
  `RELEASE_PLEASE_TOKEN` secret (see [Repository settings](#repository-settings)).

### First Release PR: fold the curated `[Unreleased]` section

Before Release Please was adopted, `CHANGELOG.md` was edited by hand, and its
`## [Unreleased]` section holds curated entries, including the upgrade notes
for the module path move to `github.com/cubrid-lab/cubrid-go`. Release Please
inserts its new version section above the first version heading, which is
`## [Unreleased]`. The section is kept unchanged until then.

In the first Release PR the maintainer must:

1. Move the `[Unreleased]` content into the new version section: keep
   `### Upgrade notes` first (the README links to `CHANGELOG.md#upgrade-notes`),
   then merge `### Fixed` and `### Added` with the generated
   `### Bug Fixes` and `### Features`, so each change appears once.
2. Delete the `## [Unreleased]` heading. From then on the file has no
   Unreleased section; Release Please inserts each new version above the
   previous one.

Do not merge a Release PR that shows the same change twice or leaves the old
`[Unreleased]` section below a released version.

## Release preflight, tag and GitHub Release

After merging the Release PR:

1. Run the release preflight from [RELEASE_POLICY.md](RELEASE_POLICY.md) on
   the merge commit, including the real CUBRID gate.
2. Add the curated release note `docs/releases/vX.Y.Z.md` in a normal PR
   (see [RELEASE_NOTES_QUALITY.md](RELEASE_NOTES_QUALITY.md)). It is not a
   copy of the CHANGELOG section: `scripts/release_notes_quality.py` rejects
   generated `* ` bullets and section names such as `BREAKING CHANGES` (use
   `Upgrade notes`), and each entry needs a `by @username` credit. The
   `sync-release-notes.yml` check also rejects any `github.com/cubrid-labs/`
   string, so in this note name the old module path without the
   `github.com/` prefix or link to the CHANGELOG upgrade notes.
3. Tag the commit and create the GitHub Release:

   ```sh
   git fetch origin && git checkout <merge-commit>
   git tag -a vX.Y.Z -m "cubrid-go vX.Y.Z"
   git push origin vX.Y.Z
   gh release create vX.Y.Z --verify-tag --title "cubrid-go vX.Y.Z" \
     --notes-file docs/releases/vX.Y.Z.md
   ```

4. Verify the published module:
   `GOPROXY=https://proxy.golang.org go list -m github.com/cubrid-lab/cubrid-go@vX.Y.Z`.
   For the first tag under `github.com/cubrid-lab/cubrid-go`, run the full
   proxy and consumer preflight in
   [#83](https://github.com/cubrid-lab/cubrid-go/issues/83): a fresh external
   module must `go get` the tag and build with the new import path and the
   `dialector` package, and requesting the tag through the old path must fail
   with "module declares its path as".
5. Mark the merged Release PR as released:

   ```sh
   gh pr edit <release-pr> --remove-label "autorelease: pending" --add-label "autorelease: tagged"
   ```

   While a merged Release PR still has `autorelease: pending`, Release Please
   does not open the next Release PR. This keeps the next version from being
   proposed before the current one is tagged.

Never move or delete a pushed tag: the Go checksum database records it.

## Repository settings

- **Settings → Actions → General → Workflow permissions:** "Allow GitHub
  Actions to create and approve pull requests" must stay enabled.
- **Optional `RELEASE_PLEASE_TOKEN` secret:** a fine-grained PAT limited to
  this repository with Contents and Pull requests read/write. With it, the
  Release PR triggers CI automatically. Without it the workflow falls back to
  `GITHUB_TOKEN` and CI must be started by hand.
- **Merge method:** squash merging keeps one Conventional Commit per PR. Merge
  commits and rebase merges bypass the title check, so each commit message
  must then follow Conventional Commits.
