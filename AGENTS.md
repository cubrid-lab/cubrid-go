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

Start **every new issue** with `status: needs triage` (also when an agent
pre-populates priority and size). The issue-opened workflow marks incoming
reports; `GITHUB_TOKEN` automation must include the label at creation because
its events do not trigger other workflows. Reporters are not required to
manage labels.

The maintainer/triager performs the following before declaring completion:

1. Check whether the reported problem is valid, already resolved or duplicated;
   inspect existing issues and PRs, evidence, local repository scope, issue
   type, dependencies, non-goals and acceptance criteria. Explain duplicate
   or invalid closures.
2. Compare the candidate's urgency with **all open issues in this repository**;
   reconsider existing relative priorities. Choose exactly one
   `priority: critical`, `priority: high`, `priority: medium` or
   `priority: low`, based on impact rather than creation order.
3. Assign exactly one `size: small` (focused), `size: medium` (contained
   multi-file), or `size: large` (cross-cutting), splitting unreviewably
   large implementation work and retaining parent acceptance.
4. Re-fetch the GitHub issue labels and confirm the type, one canonical
   priority and size, full-backlog comparison and any remaining blocker.
   **Only then** remove `status: needs triage`. Metadata alone never
   automatically completes triage.

For historical open issues, XS/S → small; M → medium; L/XL → large.
Preserve priority, assignees, type/area labels, discussions, closed issues
and existing validation/security requirements. Do not introduce a new
required PR/CI gate.

