# ADR 0003: Native CUBRID architecture boundaries

- **Status:** Proposed
- **Date:** 2026-11-07
- **Prerequisite:** Approval and merge of this repository's release policy ADR in its earlier release-policy PR
- **Decision scope:** Go database/sql driver boundaries and GORM ownership

## Context

cubrid-go implements CAS communication through database/sql and also includes an in-driver GORM dialector, while gorm-cubrid is a standalone GORM project. database/sql may retry ErrBadConn, so ambiguous execution and session ownership are critical. The existing public module path differs from the GitHub repository owner; migration cannot be assumed safe.

The CUBRID Python driver and SQLAlchemy dialect provide engineering lessons about protocol, SQL and transaction correctness, **not** implementations or cross-language public APIs to copy.

## Decision

Keep the Go CAS driver focused on database/sql/driver, including physical broker/CAS lifecycle, Statement/Rows/Tx, context cancellation, server values and errors. Rely on database/sql for pooling, not a private competing SQL pool. Make gorm-cubrid the **proposed** canonical GORM owner after consumer inventory and a versioned transition; never remove cubrid-go/dialector as part of a design-only change. Only trigger ErrBadConn retry where request non-execution is certain. Keep migration logic in the ORM layer and honor Go module Semantic Import Versioning.

## Alternatives considered

Port pycubrid DB-API shape into Go (rejected); maintain duplicate GORM implementations without a canonical owner (rejected as long-term direction); remove existing in-driver dialect immediately (rejected due to unknown consumer impact); copy MySQL's GORM grammar (rejected as correctness oracle).

## Consequences and trade-offs

Staged ownership cleanup preserves compatibility but leaves short-term duplicated functionality. Lifecycle and ambiguous-execution testing cost more than superficial pass/fail tests. Module path changes must be independently reviewed.

## Verification / acceptance criteria

Prove Go module consumer imports with current path and any proposed migration; validate context cancellation, ErrBadConn/no replay, CAS session replacement and stale handles on actual CUBRID; review GORM consumer migration with existing users before removing exports.

- [ ] Approve release/compatibility policy ADR 0001 **before** accepting this architecture ADR.
- [ ] The feasibility evidence and exact supported versions are recorded; unknown or unsupported behavior is not reported as implemented.
- [ ] Separate implementation issues, regression tests and release documentation agree with this decision.
- [ ] A maintainer reviews the decision and records acceptance explicitly; draft status alone never authorizes 1.0.

## References

[Go native design](../GO_NATIVE_DRIVER_DESIGN.md), [Go design #60](https://github.com/cubrid-lab/cubrid-go/issues/60), [release policy PR #62](https://github.com/cubrid-lab/cubrid-go/pull/62), [GORM design](https://github.com/cubrid-lab/gorm-cubrid/issues/44)

**This ADR is an architecture proposal, not a runtime implementation, migration completion, CI pass or approved release.**
