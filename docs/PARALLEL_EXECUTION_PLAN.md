# Parallel issue and PR execution plan

> **Snapshot:** 2026-11-08. **Scope:** Open issues and PRs in `cubrid-lab/cubrid-go`. Triage labels have been applied directly to all listed GitHub items. This plan does **not** approve an ADR, mark an issue fixed or authorize a merge. Refresh issue/PR/CI state before execution, as new items may appear.

## Scheduling rule

Use independent red regression cases for ErrBadConn, Context deadline, stale cursor and concurrent reconnect. Integrate only where the same CAS state-machine implementation requires a shared change. Module-path consumer checks and real DB harness proceed independently.

- **Start in parallel** unless a concrete technical interface or release artifact really requires an upstream result. A policy/ADR is a prerequisite for **final architecture acceptance or release**, not a blanket ban on independent research, tests, developer setup and CI improvements.
- Scope each PR to one testable outcome; start RED tests or feasibility spikes independently. Coordinate only the final merge on shared files/interfaces, not every investigation.
- Real CUBRID setup, JDBC comparison, failure-case tests and build/tool CI improvements are independent workstreams. Do not call a skipped or zero-case DB lane green.
- Developer Node 22 tooling must not raise the Node 18 consumer minimum during Node driver/Drizzle 1.x; other languages use their own native toolchains.
- Preserve established CI/security/release requirements. A green docs PR is not a release candidate, and no red, cancelled or skipped mandatory check is an automatic merge.
- Use native `priority: critical/high/medium/low` labels on issues and PRs; numeric internal priority codes are not used as titles. Workstream assignment is **not** a hard dependency.

## Open issues — complete inventory (11)

| Issue | Priority | Size | Independent workstream | Actual hard gate / merge coordination |
| --- | --- | --- | --- | --- |
| [#41](https://github.com/cubrid-lab/cubrid-go/issues/41) bug: stop returning ErrBadConn after a request was already sent | critical | S | CAS session safety and driver correctness | No dependency for before-send/after-send failure tests. Final fix must preserve Go database/sql ErrBadConn semantics. |
| [#39](https://github.com/cubrid-lab/cubrid-go/issues/39) bug: keep request cancellation when the CHECK_CAS probe sets its deadline | high | S | CAS session safety and driver correctness | None: design, tests and work may start independently. |
| [#43](https://github.com/cubrid-lab/cubrid-go/issues/43) test: cover stale handles for stmt.Query, prepared rows and the ExecContext close path | high | S | CAS session safety and driver correctness | Cursor/statement stale-handle tests can run independent of reconnect fix #20. |
| [#20](https://github.com/cubrid-lab/cubrid-go/issues/20) chore: audit concurrent reconnect handling in the Go client | high | M | CAS session safety and driver correctness | Concurrent reconnect review/tests parallel with cancellation #39 and retry #41. |
| [#63](https://github.com/cubrid-lab/cubrid-go/issues/63) ci: require verified CUBRID matrix before Go driver releases | high | M | Release, module and architecture decisions | Real CUBRID release harness can be implemented now. Full 1.0 sign-off later depends on named supported version coverage. |
| [#60](https://github.com/cubrid-lab/cubrid-go/issues/60) feat: design the Go-native CUBRID driver around database/sql contracts | high | M | Release, module and architecture decisions | Architecture acceptance follows reviewed release/module policy #57/#62; design research proceeds now. |
| [#57](https://github.com/cubrid-lab/cubrid-go/issues/57) docs: define Go driver compatibility and module versioning policy | high | M | Release, module and architecture decisions | None: design, tests and work may start independently. |
| [#58](https://github.com/cubrid-lab/cubrid-go/issues/58) ci: skip Go database integration for documentation-only pull requests | medium | M | CI and repository maintenance | None: design, tests and work may start independently. |
| [#45](https://github.com/cubrid-lab/cubrid-go/issues/45) chore: standardize issue intake and existing five-level sizing | medium | M | CI and repository maintenance | None: design, tests and work may start independently. |
| [#36](https://github.com/cubrid-lab/cubrid-go/issues/36) ci: run the stale workflow under the cubrid-lab organization name | low | S | CI and repository maintenance | None: design, tests and work may start independently. |
| [#21](https://github.com/cubrid-lab/cubrid-go/issues/21) chore: narrow the gosec G115 suppression to specific conversions | low | M | CI and repository maintenance | None: design, tests and work may start independently. |

## Open pull requests — complete inventory (5)

| PR | Priority | Workstream | Readiness / merge coordination |
| --- | --- | --- | --- |
| [#62](https://github.com/cubrid-lab/cubrid-go/pull/62) docs: establish Go driver release and module versioning rules | high | Release and architecture decisions | Review release/setup ADR and final CI first; no runtime change. |
| [#61](https://github.com/cubrid-lab/cubrid-go/pull/61) docs: define Go-native CAS driver responsibilities and test contracts (Draft) | high | Release and architecture decisions | Draft design. Contract research proceeds in parallel; merge only after policy acceptance. |
| [#59](https://github.com/cubrid-lab/cubrid-go/pull/59) ci: skip runtime and CUBRID matrices on docs-only PRs | medium | CI routing and issue intake | Docs-only matrix optimization independent of CAS correctness; final release never skips live DB. |
| [#46](https://github.com/cubrid-lab/cubrid-go/pull/46) chore(triage): require reviewed issue priority and size | medium | CI routing and issue intake | Issue intake improvement independent of driver implementation. |
| [#37](https://github.com/cubrid-lab/cubrid-go/pull/37) ci: run the stale workflow under the cubrid-lab organization name | low | CI routing and issue intake | Stale-workflow correction has failing CI; verify and rebase before merge. |

## Priority and merge discipline

1. Tackle **critical** transactional/data-safety defects immediately and independently; do not mix them into a release policy PR.
2. Review **high** policy/setup/feasibility issues and live DB test harnesses in parallel. Policy approval is needed for stable-release sign-off, but not for independent RED tests.
3. Progress **medium** enhancements and CI cleanup on separate branches without forcing the whole roadmap to wait on one issue.
4. Schedule **low** maintenance/cosmetic items opportunistically; do not close or downgrade merely to improve issue counts.
5. Validate actual final-head CI, review comments, supported versions, rollback behavior and registry/consumer evidence before merging or releasing.

**Existing parent tracking:** [#60](https://github.com/cubrid-lab/cubrid-go/issues/60). Keep this inventory up to date when an item closes, a PR merges or a new issue is filed; a new issue belongs to its own repository and owner, not a cross-repository implementation dependency.
