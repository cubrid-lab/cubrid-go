# ADR 0002: CUBRID correctness and failure-evidence boundaries

- **Status:** Proposed
- **Date:** 2026-11-07
- **Scope:** cubrid-go correctness obligations and review evidence

## Context

cubrid-go owns the Go database/sql CAS driver. This project's correctness decisions must cover Go database/sql/driver interfaces, physical CAS sessions, Go module imports, context cancellation, and real CUBRID tests, including negative and failure-path behavior. Passing offline tests alone is not proof of database compatibility. The accepted public API, local implementation, and actual CUBRID server behavior are the basis for requirements and test evidence.

## Decision

Follow Go database/sql/driver contracts and classify connection errors with database/sql's retry behavior in mind. In particular, only use driver.ErrBadConn when a statement is known not to have reached the server; do not allow implicit replay after a possible execution. Keep CAS session and statement/row handles tied to a physical connection; a successful END_TRAN/OUT_TRAN does not imply reconnection. Use context.Context safely, preserve numeric precision and typed scan results, and test race/cancellation plus live CUBRID recovery semantics.

Create targeted tests for this project's observable contracts. Record whether tests actually communicated with CUBRID, the server/broker and dependency versions, asserted SQL outcomes, and skipped cases. External test results never certify this package.

## Alternatives considered

- **Rejected:** Porting the Python DB-API class shape; returning ErrBadConn for any mid-response EOF; treating *sql.DB as a physical connection; inferring all DB version support from 11.0 and 11.2 tests.
- **Rejected:** Treating another package's API, implementation, or test results as this project's contract.
- **Rejected:** Marking an unsupported/unverified feature as absent simply because one server or connector failed in a particular test.

## Consequences and trade-offs

Go idiomatic error classifications and failure injection add complexity; consumers are protected from duplicate writes, stale cursors and unexpected module compatibility breakage. Ordinary documentation-only PRs need not run an expensive database matrix; **the final release candidate still must run all required real-CUBRID checks** for its promised compatibility.

## Verification / acceptance criteria

- [ ] Normal and failure-path tests cover the actual decision's affected behavior.
- [ ] No silent statement replay, fabricated database values, unknown-success transaction or stale-handle reuse for the supported public API.
- [ ] Real DB tests use an isolated ready-checked CUBRID instance, assert actual stored/returned values and report nonzero executed-case counts; skip/missing is not success.
- [ ] Independent JDBC (or verified official driver) comparisons evaluate meaning and type, not matching source implementation.
- [ ] Public docs, type declarations and compatibility claims are reconciled with observed behavior; unsupported operations fail clearly.
- [ ] Maintainer accepts this ADR and separately reviews release/code changes; neither this record nor a checklist fixes existing defects.

## References

[engineering checks](../ENGINEERING_INVARIANTS.md), [Go driver #60](https://github.com/cubrid-lab/cubrid-go/issues/60), [live release gate #63](https://github.com/cubrid-lab/cubrid-go/issues/63)

**No claim of implementation completion is made.**
