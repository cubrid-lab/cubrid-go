# AGENTS.md

## Shared issue and pull request policy

See [cubrid-lab issue, PR, and commit standard](https://github.com/cubrid-lab/.github/blob/main/docs/ISSUE_PR_COMMIT_STANDARD.md). Issues use a simple type-only title (`feat: ...`, `bug: ...`, `docs: ...`) without required module names. Describe the problem or goal, relevant evidence, and a verifiable outcome; keep implementation details provisional. Triage owns validity, duplicates, scope, priority and size. Pull requests and commits use the existing Conventional Commits/CI requirements and document actual changes and checks. Do not include internal agent instructions or personal comments in public issue prose.


## Purpose
`cubrid-go` provides a pure Go CUBRID driver and related ecosystem integrations.

## Read First
- `README.md`
- `PRD.md`
- `CONTRIBUTING.md`
- `docs/agent-playbook.md`

## Working Rules
- Preserve `database/sql` semantics and DSN behavior unless the change explicitly updates those contracts.
- Keep documentation examples synchronized with the real driver behavior.
- Prefer compatibility-safe changes to exported types and behavior.
- Add tests for protocol, scanning, or transaction changes.




## Development setup before design and TDD implementation

Use the repository-specific [development setup guide](docs/DEVELOPMENT_SETUP.md) and [setup ADR 0005](docs/adr/0005-development-setup.md) (**Proposed**) before changing this repository's public behavior or tooling. Verify the **actual** manifest, supported developer toolchain, dependency resolution, read-only offline checks, and availability/absence of real CUBRID integration separately. Do not invent a nonexistent integration harness, lint command, published package, or dependency path. A successful unit build is not a successful real-database test or a published package. Record observed setup blockers as issues; do not silently change the project's public compatibility contract, weaken CI, or modify release credentials to make onboarding appear green.

## Test-first implementation after architecture review

Before implementing a CUBRID behavior change, check the approved API/SQL/protocol contract and [TDD ADR 0004](docs/adr/0004-test-first-development.md) (**currently Proposed**). Follow **Red → Green → Refactor** in focused slices: execute a deterministic failure for the intended reason first, implement the smallest fix, rerun the case and relevant regressions, then refactor. Explain the initial failure and actual checks in the PR without inventing command output. A final PR must pass its required CI; never keep a deliberately failing regression as proof of Red. Confirm database-dependent behavior through real CUBRID tests and the release candidate matrix; mocks alone cannot certify it. Upstream feasibility prototypes and docs-only changes are exempt from ritual Red steps but must show their own accurate evidence. Release policy and architecture decisions must be approved before classifying design as completed.

## Architecture decisions and release order

Read the [repository ADR index](docs/adr/README.md) before proposing changes to public behavior, release contracts, this repository's architecture, transaction ownership or public API. ADR 0001 records release governance; ADR 0002 records this repository's correctness-evidence requirements; architecture ADR 0003 remains **Proposed** in its own design PR. Do not treat a Proposed ADR as approved. Adopt the release policy before accepting the architecture ADR; require actual tested evidence before claiming implementation or release readiness. Use the [ADR template](docs/adr/TEMPLATE.md) for material new decisions, not for every trivial change.

## CUBRID correctness lessons and native regression review

Before changing CAS protocol framing, database/sql connection/transaction lifetime, Go result conversion, errors, cancellations or release claims, review [the repository-specific engineering invariants](docs/ENGINEERING_INVARIANTS.md). These rules are grounded in the Go `database/sql` driver API and its own CAS responsibilities, and verifiable CUBRID behavior. Distinguish mock tests, live CUBRID execution and independent differential evidence; never claim a check ran without its actual CI result. For an unrelated docs-only change, the checklist is advisory and does not mandate unnecessary database jobs. Existing repository review and merge requirements remain authoritative.

## Development Workflow (cubrid-lab org standard)

Use **one review policy** for human-authored contributions and all coding
tools (OpenCode, Codex, Claude Code, ChatGPT, Copilot, and others).
No Oracle pre/post review or particular AI reviewer is required by tool.

1. **Implement:** scope a focused change, add relevant tests, and update
   affected documentation in the same PR (or explain why docs are unaffected).
2. **GitHub CI:** required GitHub Actions checks on the final PR head are
   authoritative. Run applicable language-specific lint, tests, documentation,
   integration and security checks. Local runs are encouraged, not a separate
   prerequisite for merging.
3. **Review:** check correctness, risk, tests, security and maintainability.
   Oracle, Copilot and `agent-review` are optional **advisory** evidence.
4. **Merge:** address or disposition findings, verify required checks, and
   have the responsible maintainer decide on merge. Security-sensitive
   CI permissions, authentication and publishing changes require an
   independent maintainer's assessment.

Preserve existing branch protections, CI gates, integration tests, releases
and protected security paths. No automatic approval follows from green CI or
AI summaries.

## Validation
- `go test ./...`
- `make vet`
- `make test-cov`

## Human-readable communication (all agents)

Use this rule for text authored or edited by Codex, Claude Code, OpenCode,
ChatGPT, Copilot, or any other agent. It applies to issues, PRs, reviews,
comments, checklists, status updates, commit summaries, release notes,
documentation, presentations, and other maintainer-facing writing.

- Write for a reader unfamiliar with the underlying script, storyboard,
  automation run, or agent plan. Name the actual project, component,
  action, evidence, and location in ordinary, clear language.
- Never use unexplained internal identifiers as reader-facing labels or
  checklist instructions (for example, `H1, P1, C1, K1, F2` or `A1/B1/R1/R2`).
  Do not substitute `P1`/`P2` for repository priority labels.
- When an internal identifier is necessary for accurate cross-reference,
  **describe the subject first**, then add the identifier in context:
  `package introduction (0:20; storyboard shot 1)`. Use descriptive
  names and timestamps, section titles, or paths wherever practical.
- Use natural, actionable wording. Prefer `Listen to the full narration and
  verify the pronunciation of each project name` to `Maintainer full listen`.
  Avoid compressed task fragments, unexplained acronyms, and generated
  process/status jargon.
- Keep legitimate technical identifiers unchanged in code, tests, structured
  data, logs, specifications, and exact evidence references. Issue numbers,
  filenames, command names, APIs, error codes, and hashes are not prohibited.
  Do not break machine-readable references to improve prose.
- Before publishing, reread the text as a first-time maintainer. It should
  state what happened or needs to happen, why, and where without requiring a
  separate lookup table. Edit an existing editable comment to correct unclear
  wording rather than adding redundant follow-up comments.

This is a shared writing convention, not a new CI check, reviewer approval,
or merge gate. Keep existing repository-specific technical and safety rules.

### Explanations in code, tests and reviews

Source comments, test names and test comments, examples, PR descriptions and
review replies follow
[section 6 of the cubrid-lab issue, PR, and commit standard](https://github.com/cubrid-lab/.github/blob/main/docs/ISSUE_PR_COMMIT_STANDARD.md#6-explanations-in-code-tests-reviews-and-agent-written-text):
say what happens, why it matters and the constraint involved, in plain
English, before any issue number or scenario code. Identifiers are fine for
traceability once explained; keep machine-readable names (test names used for
discovery, fixtures, error codes, paths) unchanged, and revise only text a
reader would find unclear.

In this driver, a comment on CAS protocol framing, reconnects or session and
handle ownership should state the behavior and the invariant it protects (for
example, that a handle from an earlier session must never be sent) before
citing an issue.

This repository's own safety, build, test and release rules take precedence
where they differ from the shared standard.

## Repository ownership in issues, PRs, and design documents

This repository owns **the Go database/sql CAS driver**. Author issue and PR titles, summaries, ADR context, acceptance criteria, and release decisions from **cubrid-go**'s actual code, public contract and evidence. For changes affecting Go database/sql/driver interfaces, physical CAS sessions, Go module imports, context cancellation, and real CUBRID tests, cite local files/tests, expected behavior and actual verification. Do not use another repository's failures, ADRs, issue tracker or language-specific tooling as a governing authority or as the motivation in a title. Do not copy issue/PR templates from an unrelated package. GORM integrations are external consumers; coordinate through a documented Go module/API contract without adopting another repository's design or release plan.

Cross-repository links are appropriate only for a **verified dependency/API compatibility boundary**, independently executed semantic comparison (such as official JDBC), or an explicitly identified coordination requirement. Label such references as context, not as this repository's acceptance proof, and keep tracking and release decisions local. During PR review, check issue title/body, PR title/body, ADRs, AGENTS/CONTRIBUTING, and user-facing release notes for misplaced external project references before merge.
