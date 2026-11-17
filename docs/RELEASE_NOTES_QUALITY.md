# Release notes quality

Use this repository's own versioned `docs/releases/vX.Y.Z.md` descriptions as the editorial source. Run `python3 scripts/release_notes_quality.py` and `python3 -m unittest discover -s scripts -p test_release_notes_quality.py -v` before reviewing a candidate.

## Write for users

- Lead with one or two sentences naming a concrete outcome and any required user action. For documentation-only changes, explicitly say runtime behavior is unchanged only when verified.
- Call out upgrade, security, support and migration implications first, especially **Go module import path, minimum Go version, database/sql and GORM behavior, verified CUBRID broker compatibility**. Never infer compatibility from a green unit test alone.
- Use only applicable, nonempty `###` categories such as Compatibility, Upgrade notes, Features, Bug Fixes, Documentation, CI and Tests. Describe outcomes rather than copying raw commit subjects.
- Combine duplicate descriptions of one change. Include direct implementing PR/commit links and verified human credits inline. An issue reporter is not necessarily the author; verify first-contribution claims separately. Do not label bot-authored work as human.
- Finish with exactly one version-correct Full Changelog link. The first tag may link its commit list. Preserve exact release-version provenance rather than inventing missing versions or silently changing tags.

## Review and publication safeguards

The read-only CI gate checks Markdown structure, summaries, obvious duplicates, raw generated bullets and links. It cannot prove factual correctness, semantics, author identity or GitHub body equality; review these manually against the tagged code and release history.

The current `sync-release-notes.yml` has **separate write privileges**. Do not assume passing this quality check grants permission to edit older GitHub Release bodies. Before any historical public-body sync, separately review the exact canonical note, back up the current remote body, check remote concurrency, update **body only** with explicit approval, and verify it by a fresh GitHub API read. Retain tag, published date, title, draft/prerelease status, assets and registry artifacts. This PR does not dispatch or change the existing publishing workflow.

Tracked in #80.
