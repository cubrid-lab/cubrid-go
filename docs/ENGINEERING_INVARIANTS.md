# Engineering invariants learned from the Python CUBRID projects

> **Status:** Engineering review checklist and test-design requirements, not evidence that this repository already passes them. Adopted from verified scenarios in [pycubrid](https://github.com/cubrid-lab/pycubrid) and [sqlalchemy-cubrid](https://github.com/cubrid-lab/sqlalchemy-cubrid), with language-native adaptation. Review together with this repository's release policy before claiming stable support.

## Why these invariants exist

The Python driver and SQLAlchemy dialect revealed that a passing query or green offline suite is not enough. Bugs occurred around transaction boundaries, CAS session replacement, stale handles, partial fetches, value fidelity, state transitions, unsupported SQL and skipped live tests. They also established reusable **methods**: protocol fixtures, modeled lifecycle tests, JDBC/driver differential cases, explicit failure modes, catalog/DDL tests, and candidate-SHA evidence. **Do not port Python API shapes, source code, or version rules into another language.**

Useful concrete sources:
- [pycubrid CAS reconnect architecture](https://github.com/cubrid-lab/pycubrid/blob/main/docs/ARCHITECTURE.md#cas-reconnection), [session persistence tests](https://github.com/cubrid-lab/pycubrid/blob/main/tests/test_cas_session_persistence.py), [cursor ownership tests](https://github.com/cubrid-lab/pycubrid/blob/main/tests/test_autocommit_handle_ownership.py)
- [pycubrid connection state-machine tests](https://github.com/cubrid-lab/pycubrid/blob/main/tests/test_connection_state_machine.py), [official-driver differential cases](https://github.com/cubrid-lab/pycubrid/blob/main/tests/test_official_differential.py)
- [SQLAlchemy CUBRID architecture](https://github.com/cubrid-lab/sqlalchemy-cubrid/blob/main/docs/ARCHITECTURE.md), [transactional DDL tests](https://github.com/cubrid-lab/sqlalchemy-cubrid/blob/main/test/test_transactional_ddl.py), [live integration gate tests](https://github.com/cubrid-lab/sqlalchemy-cubrid/blob/main/test/test_integration_gate.py), [release-note formatting](https://github.com/cubrid-lab/sqlalchemy-cubrid/blob/main/docs/RELEASE_NOTES_STYLE.md)

**Evidence rule:** the Python code/tests are counterexamples and research references, not proof of the corresponding Go, Rust or TypeScript behavior. Every native implementation must demonstrate its own actual behavior on a real CUBRID server.

## Go `database/sql` driver invariants

1. **Never trigger unsafe implicit retries.** Go `database/sql` may retry operations when a driver returns `driver.ErrBadConn`. Use that error only if it is known that the statement did **not** reach CUBRID; after send/partial reply, surface the uncertain outcome and retire the connection instead of allowing a duplicate INSERT/COMMIT.
2. **Respect physical-connection ownership.** `*sql.DB` owns its goroutine-safe pool; the driver owns a single CAS session with its statement/row handles. `CAS_INFO OUT_TRAN` does not mean the socket is dead. CHECK_CAS/probe and reset policy need state-specific tests. Reconnected sessions must not inherit stale statements/rows.
3. **Cancellation and cleanup.** Treat `Context` cancellation during request I/O as ambiguous unless absence of execution is established. Restore socket deadlines carefully; ensure statement/cursor cleanup cannot send a stale handle to a new CAS process.
4. **Data truthfulness.** Preserve `database/sql/driver.Value` and `sql.Scanner` semantics for BIGINT, precise NUMERIC, Unicode, NULL, bytes, timestamps, collections and LOBs where supported; otherwise return a typed conversion error. Detect lossy coercion before release.
5. **Protocol security:** reject invalid/truncated length fields, protect against uncontrolled allocation, avoid interpolated-SQL injection and credential leakage, and test bad encodings and broker redirects.
6. **Driver versus ORM boundary:** `cubrid-go` owns CAS transport and `database/sql`; `gorm-cubrid` owns GORM-specific SQL and migrations. The existing in-driver dialector requires a compatibility migration rather than immediate deletion.

## Required code-review and test evidence

- [ ] For any `conn.go`, `stmt.go`, `rows.go`, `tx.go` or `protocol.go` change, record before-send/in-flight/after-reply state and prove error classification.
- [ ] Go-native unit/fuzz/state tests, `go test -race` on affected concurrent code and real CUBRID tests cover both normal and failure cases.
- [ ] Explicitly cover CAS restart, autocommit, transaction rollback, stale handle invalidation and request cancellation where relevant.
- [ ] Compare important outcomes to official JDBC/pycubrid on the same server version without copying their API.
- [ ] Real database assertions must be nonempty, scoped to disposable test databases and accompanied by test counts. The CI's 11.0/11.2 services do not certify every roadmap-supported version.
- [ ] Verify module import path and external Go consumer before a release; v2+ follows Go semantic import versioning.
- [ ] No unverified performance claim, unsupported-database label or stale support badge.

## Existing tracked problems

- [CAS reconnect #20](https://github.com/cubrid-lab/cubrid-go/issues/20), [cancel deadline #39](https://github.com/cubrid-lab/cubrid-go/issues/39), [ambiguous ErrBadConn #41](https://github.com/cubrid-lab/cubrid-go/issues/41), [stale handles #43](https://github.com/cubrid-lab/cubrid-go/issues/43)
- [real DB release gate #63](https://github.com/cubrid-lab/cubrid-go/issues/63), [Go module policy #57](https://github.com/cubrid-lab/cubrid-go/issues/57)

This checklist does not make the old or new GORM dialector interchangeable. Follow [release policy](RELEASE_POLICY.md) before approving the [native Go architecture draft PR #61](https://github.com/cubrid-lab/cubrid-go/pull/61).
