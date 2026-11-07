## Summary

<!-- Brief description of changes -->

## Changes

<!-- List the specific changes made -->

-

## Type of Change

<!-- Check the relevant option -->

- [ ] Bug fix (non-breaking change that fixes an issue)
- [ ] New feature (non-breaking change that adds functionality)
- [ ] Breaking change (fix or feature that would cause existing functionality to change)
- [ ] Documentation update
- [ ] Refactoring (no functional changes)
- [ ] Chore (maintenance, dependencies, CI, etc.)

## Checklist

- [ ] My code follows the project's code style
- [ ] I have run `make check` (fmt + vet + test)
- [ ] I have added tests for new functionality (if applicable)
- [ ] I have updated documentation (if applicable)
- [ ] My changes do not introduce new warnings

## Related Issues

<!-- Link to related issues: Closes #123, Fixes #456 -->

## CUBRID correctness and regression evidence

<!-- Complete only the items relevant to the code changed; state N/A with a reason for unrelated documentation changes. See docs/ENGINEERING_INVARIANTS.md. -->

- [ ] For changes to connection, CAS handling or transactions, I tested failure/cancellation and stale-handle behavior; no request with uncertain execution is automatically replayed.
- [ ] For changed SQL/value handling, I verified actual CUBRID results (or explicitly marked live evidence as not yet available), including precision and error propagation.
- [ ] I documented the Go version, CUBRID server version and **actual** live/offline tests executed, including skips and failures; I did not treat a successful startup as a DB test.
- [ ] I reviewed public `database/sql`/module compatibility and updated the release notes when user-visible behavior changed.

## Test-first evidence and design approval

<!-- Applicable to behavioral implementation. For docs-only or design feasibility work, write N/A and the actual evidence. See docs/adr/0004-test-first-development.md and docs/DESIGN_SIGNOFF.md (in the design PR where applicable). -->

- [ ] **Design:** The public behavior/SQL/transaction/error contract and owning layer were reviewed before implementation (link ADR/issue or explain N/A).
- [ ] **Red:** A targeted test failed for the expected **behavioral** reason; summarize the observed failure or state why test-first did not apply.
- [ ] **Green:** The smallest implementation passes the new test; name the exact test command and outcome.
- [ ] **Refactor:** Related regression tests and required final-head checks pass, with observed CI links when available.
- [ ] **Live CUBRID:** For behavior depending on the server, distinguish real DB tests from mocks; include exact server/runtime/driver versions and passed/failed/skipped cases, or explicitly mark the behavior unverified.
- [ ] **Release:** No version/support claim or publication is inferred from a green mock or skipped integration job; final release candidates follow the separately reviewed full DB gate.
