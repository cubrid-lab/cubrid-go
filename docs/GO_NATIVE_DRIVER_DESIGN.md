# CUBRID Go driver architecture — proposed

**Status:** Design proposal (2026-11-07). Not a claim that all features work or are covered by live tests. Parent issue: [#60](https://github.com/cubrid-lab/cubrid-go/issues/60).

## Foundation: CUBRID semantics, Go interfaces

`cubrid-go` is a pure-Go CAS-protocol driver exposed through Go's `database/sql` and `database/sql/driver`. Its users should not have to learn Python DB-API or use a MySQL compatibility mode. The separation of `pycubrid` (driver) and `sqlalchemy-cubrid` (ORM integration) provides an **ownership model**, not code or API to port.

Only CUBRID's documented and live-verified SQL and CAS semantics determine driver behavior. It is reasonable to study other CUBRID connectors for protocol research and differential evidence; use Go standard-library interfaces and errors as the public API design authority.

```text
Go application / GORM             <--- GORM belongs in gorm-cubrid
        |
database/sql: DB + pool, Rows, Tx, Context
        |
cubrid-go: driver.Conn/Stmt/Rows/Tx
        |
CUBRID session state and CAS binary codec
        |
Broker / CAS / CUBRID
```

## Boundaries and required review

| Layer | Owns | Does not own |
| --- | --- | --- |
| `database/sql` | Pool sizing, reuse, `sql.DB`, `sql.Tx` management | CAS socket protocol, server-side handle validity |
| `cubrid-go` driver | Handshake, request framing, parameter/value mapping, physical session, query handles, error classification | GORM model semantics or migrations |
| `gorm-cubrid` | GORM Dialector, CUBRID SQL clauses, GORM type mapping, schema migrations | Reimplementing CAS I/O or a second pool |

The existing `cubrid-go/dialector` package creates an ownership overlap with `gorm-cubrid`. **Decision for review:** make `gorm-cubrid` canonical for future GORM work, then evaluate users, compatibility and a documented deprecation/transition before any removal. Do not break existing imports during the design phase.

## Go-specific public contract

Review actual interfaces used by Go's `database/sql/driver` (not all optional interfaces are automatically required): `DriverContext`/`Connector`, `Pinger`, `SessionResetter`, `Validator`, `QueryerContext`, `ExecerContext`, `ConnPrepareContext`, `ConnBeginTx`, named-value validation and result-column metadata.

- `context.Context` cancellation must either cancel safely or return an explicit error; protocol framing and session state must remain valid, or the connection must be discarded. A timeout is not proof that the server did not execute a statement.
- `driver.ErrBadConn` must not cause duplicate execution after the request may already have reached the server. A dead connection detected **before** a request is sent can be retired and retried through the standard library as appropriate.
- A connection in a transaction has unique state and handles. No query or cursor may accidentally migrate to a replacement session. Stale statements and rows reject safely after session replacement.
- `*sql.DB` may be used by concurrent goroutines; a physical `driver.Conn` is managed exclusively by `database/sql`. Never introduce hidden shared mutation across sockets.
- Bind and scan contracts must preserve numeric accuracy (especially BIGINT/NUMERIC), time representations, CUBRID collection ordering/duplicates, binary/LOB values, NULLs and server error codes. Any unsupported conversion returns an actionable error, not a guessed substitute.
- `sql.Result.LastInsertId` and `RowsAffected` must report only values actually supported and trustworthy for the statement.

## Connection lifecycle for design verification

```text
uninitialized -> broker handshake -> CAS assigned -> database session ready
                                                  |
                                           request in flight
                                           /        |        \
                                     complete    before-send   ambiguous
                                       |           failure      after-send
                                       v              |              |
                                   reusable      reconnect     report error,
                                                if safe         never replay
```

Explicitly specify how `CHECK_CAS`, broker redirect, session reset, autocommit, prepared statements, open result handles and transaction state interact. This should share **invariants**, not source code, with pycubrid's documented reconnect rules.

## Testing and release requirements

- Type/codec unit tests, protocol framing fuzz tests, Go race tests (`go test -race ./...`), cancellation/timeout tests, and `database/sql` contract tests.
- Real CUBRID tests with exact versions and observed pass counts, including broker/CAS restart, stale handles, transactions and ambiguous failures. Compare semantically important outcomes with JDBC as an independent reference where justified.
- API documentation and real examples using Go idioms (`context.WithTimeout`, `db.QueryContext`, `defer rows.Close()`, `rows.Err()`).
- No automatic Go 1.0 release. Stability requires evidence. For Go modules, a future breaking v2+ needs a matching `/v2` module/import path. See existing [compatibility issue #57](https://github.com/cubrid-lab/cubrid-go/issues/57).
- The Go driver and `gorm-cubrid` may coordinate **compatible major generations** once both are stable, but minor/patch versions remain independent; version strings do not replace the tested dependency matrix.

## Follow-up work

- Existing correctness work: [reconnect #20](https://github.com/cubrid-lab/cubrid-go/issues/20), [cancel #39](https://github.com/cubrid-lab/cubrid-go/issues/39), [ErrBadConn #41](https://github.com/cubrid-lab/cubrid-go/issues/41), [stale handles #43](https://github.com/cubrid-lab/cubrid-go/issues/43).
- Establish a CUBRID driver state diagram and a feature/support matrix from the **current** implementation, then file small independent implementation issues only when the gap is verified.
- Related ORM design: [gorm-cubrid](https://github.com/cubrid-lab/gorm-cubrid).

**Not changed by this proposal:** current driver, runtime contract, GORM imports, module path, dependencies, CI requirements or releases.
