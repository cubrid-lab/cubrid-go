# AGENTS.md

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

## Issue triage lifecycle

New issues in this repository begin with `status: needs triage`. Issue
templates attach the label and the `issues: opened` workflow covers external
submissions. GitHub Actions creating issues with `GITHUB_TOKEN` must attach
the label at creation because their own events may not retrigger the workflow.

1. Review the problem, evidence/unknowns, duplicate issues and open PRs,
   owning repository, type, scope/non-goals, acceptance and prerequisites.
2. Compare **every open issue in this repository**, including paginated
   results. Judge impact, urgency, known risks and blocked versus executable
   work; do not simply sort by issue age or PR creation time.
3. Set one `priority: critical/high/medium/low` and one existing
   `size: XS/S/M/L/XL`. Split overlarge independent work, preserving
   contributor ownership and existing issue acceptance criteria.
4. Re-fetch GitHub labels and verify type, priority, size, scope and duplicate
   disposition. Remove `status: needs triage` only after this manual review;
   retain it while evidence or decisions are pending.

No bot chooses priority or auto-completes triage. Existing security, checks
and release safeguards remain authoritative; do not create new PR gates.
