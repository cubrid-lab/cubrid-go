# Concurrency and reconnects

This document states what cubrid-go guarantees when it is used from several
goroutines, and how that interacts with the driver's automatic reconnect. It
records the result of the audit in
[#20](https://github.com/cubrid-lab/cubrid-go/issues/20) and the tests that
check it.

## Summary

- **Use the driver through `database/sql`.** `*sql.DB` is safe for concurrent
  use by many goroutines. So are `*sql.Stmt`, and the methods of `*sql.Tx` and
  `*sql.Conn`, as the `database/sql` documentation describes. `*sql.Rows` is
  for one goroutine at a time, except that `database/sql` itself may close it
  from another goroutine when its context is canceled.
- **One physical connection serves one request at a time.** Each pooled
  connection is one CAS session on one TCP socket. The CAS protocol has one
  outstanding request per session and replies carry no request identifier, so
  a second request must not be sent before the reply to the first has been
  read. Concurrency comes from the pool: concurrent goroutines use different
  connections. Size it with `SetMaxOpenConns` (see
  [Connection Pooling](../README.md#connection-pooling)).
- **This limit comes from the CAS protocol and the driver's session state,
  not from Go's `net.Conn`.** `net.Conn` allows concurrent method calls; for
  example the driver's cancel watcher calls `SetDeadline` while another
  goroutine is blocked in `Read`. What must not happen concurrently is two CAS
  request/reply exchanges, or an exchange and a reconnect, on one session.
- **A reconnect only happens inside a request, on that request's
  connection.** It never affects another pooled connection, and it cannot run
  while another request on the same connection is in flight.
- **Handles never cross sessions.** Prepared statements and result sets
  remember which physical session created them. After a reconnect they are
  refused before anything is sent (see
  [Troubleshooting](TROUBLESHOOTING.md#prepared-statement-fails-after-a-broker-restart)).
- **Direct use of the driver types is single-goroutine.** If you use the
  `driver.Conn`, `driver.Stmt`, `driver.Rows` or `driver.Tx` values directly
  (for example through `(*sql.Conn).Raw`), use each connection and everything
  created from it from one goroutine at a time, as the `database/sql/driver`
  contract requires. The driver still serializes requests on one connection
  with its own mutex, but that is a safety net, not a supported way to share a
  connection.

## How `database/sql` calls the driver

Verified against the `database/sql` source of Go 1.21.0, 1.22.0 and 1.23.4,
where the relevant functions are identical. CI runs the tests below on Go 1.21
and 1.22.

`database/sql` keeps one mutex per physical connection (its `driverConn`) and
holds it for every call into the driver for that connection:

| `database/sql` operation | Driver call | Lock held by `database/sql` |
|:--|:--|:--|
| `Query`, `Exec`, `Begin`, `Prepare` (on `DB`, `Conn` or `Tx`) | `conn.QueryContext`, `ExecContext`, `BeginTx`, `Prepare` | connection lock |
| `Stmt.Query`, `Stmt.Exec` | `stmt.Query`, `stmt.Exec` | statement lock, which is the connection lock |
| `Rows.Next` | `rows.Next` | connection lock |
| `Rows.Close`, including the close `database/sql` runs on its own goroutine when the query's context is canceled | `rows.Close` | connection lock, after any running `Rows.Next` returns |
| `Stmt.Close` of a statement prepared on a `Tx` or `Conn` | `stmt.Close` | statement lock, which is the connection lock |
| `Stmt.Close` of a `DB` statement | `stmt.Close` | statement lock, which is the connection lock; while a request uses the connection, the close is deferred until the connection is returned to the pool |
| `Tx.Commit`, `Tx.Rollback`, including the rollback when the transaction's context is canceled | `tx.Commit`, `tx.Rollback` | connection lock |
| closing a pooled connection | `conn.Close` | connection lock |

So `database/sql` never calls into one driver connection from two goroutines
at once. The only goroutine that runs alongside a driver call on the same
connection is the driver's own cancel watcher, described next.

## Inside the driver

### The connection mutex

`conn.mu` is held for the whole of every request: from building the request,
through the out-of-transaction `CHECK_CAS` probe and any reconnect, to reading
the reply. Every path that can send a request or reconnect holds it:

| Entry point | Can reconnect | Holds `conn.mu` | Cancel watcher |
|:--|:--|:--|:--|
| `conn.QueryContext`, `conn.ExecContext` (including the escaping-mode probe, `LAST_INSERT_ID()` lookup and handle cleanup inside them) | yes | yes | yes |
| `conn.Ping` | yes | yes | yes |
| `conn.Prepare`, `conn.ServerVersion` | yes | yes | no |
| `stmt.Exec`, `stmt.Query`, `stmt.Close` | yes | yes | no |
| `rows.Next` (when it fetches more rows), `rows.Close` (when the rows own their handle), and either of them when it sends a closed statement's deferred `CLOSE_REQ` | yes | yes | no |
| `tx.Commit`, `tx.Rollback` | yes | yes | no |
| `conn.Begin`, `conn.BeginTx` | no (nothing is sent) | yes | no |
| `conn.Close` | no | yes | no |

### Connection state and what protects it

| State | Written by | Protection |
|:--|:--|:--|
| `socket` | `connect` (handshake and reconnect), `checkReconnect` | `conn.mu` and `sockMu`, through `setSocket`; or before `Driver.Open` returns the connection. The cancel watcher reads it under `sockMu` only. |
| `generation` (physical session number) | `connect` | `conn.mu` |
| `casInfo` (CAS status, including in/out of transaction) | `connect`, every reply, `CHECK_CAS` | `conn.mu` |
| `closed` | `Close`, `retireTransport`, an interrupted probe | `conn.mu`, or before `Driver.Open` returns the connection (`retireTransport` on a failed handshake) |
| `autoCommit` | `Begin`, `BeginTx`, `Commit`, `Rollback` | `conn.mu` |
| `protoVer`, `stringEscapeMode` | `connect`, first parameterized execution | `conn.mu` |
| `reqCtx`, `reqDeadline` (running request) | start and end of `QueryContext`, `ExecContext`, `BeginTx`, `Ping` | `conn.mu` |
| `canceled` (sticky cancellation of the running request) | cancel watcher; cleared when the watcher stops | atomic |
| host, port, credentials, `timeout` | `Driver.Open` | not changed after `Open` |
| `stmt.closed`, `stmt.queryHandle`, `stmt.openRows` (rows that may still fetch with the statement's handle) | `stmt.Close`, `stmt.Query`, and `rows.Next`/`rows.Close` when they stop using the handle | `conn.mu` |
| statement SQL, type, columns, `generation` | `conn.Prepare` | not changed after `Prepare` |
| `rows` buffer, offsets, `closed`, `queryHandle`, `stmt` | `rows.Next`, `rows.Close` | the caller (`database/sql` connection lock) |

### The cancel watcher

`QueryContext`, `ExecContext` and `Ping` start a goroutine that waits for the
request's context. When the context is done it sets `canceled`, reads the
current socket under `sockMu` and expires its deadline, which makes the
blocked `Read` or `Write` return. A reconnect may replace the socket, or leave
none while it dials; `setSocket` gives a new socket an expired deadline if the
request was already canceled, and every later deadline the request sets keeps
the socket expired. The watcher is stopped and waited for before the request
releases `conn.mu`, so it never touches the next request on the connection.

### Reconnects and requests in flight

A reconnect runs only from `checkReconnect`, at the start of a request, with
`conn.mu` held. Because every request path holds `conn.mu`, no other request
on that connection can be in flight, and the replaced socket is never used by
another goroutine. A reconnect that fails retires the connection, and a
request whose reply was lost is never retried (see
[Troubleshooting](TROUBLESHOOTING.md#an-insert-failed-after-send-and-its-outcome-is-unknown)). Other pooled connections have their
own socket and state and are not affected.

### Closing a statement while its rows are open

`database/sql` closes a statement prepared on a `*sql.DB` only after its
open `Rows` are closed, but it closes a statement prepared on a `Tx` or a
`*sql.Conn` at once, even while `Rows` from it are open. Those rows fetch
with the statement's server handle, and CUBRID gives a released handle
number to the next `PREPARE` on the session: on CUBRID 11.0 and 11.2, rows
whose handle was released and reused went on to return the other
statement's rows, with no error ([#86](https://github.com/cubrid-lab/cubrid-go/issues/86)). The driver
therefore counts, under `conn.mu`, the rows that may still fetch with a
statement's handle. While there are any, `stmt.Close` only marks the
statement closed, and the last of those rows to be closed, or to read its
last row, sends the `CLOSE_REQ`. When it is the final `rows.Next` that sends
it, that `Next` makes one extra round trip, under `conn.mu` like any other
request. The rows keep working after the statement is closed, as they do
for a `*sql.DB` statement. If the connection has reconnected in the
meantime, the deferred close sends nothing, like any other handle from an
earlier session.

Executing the statement again while such rows are open has the same root:
`EXECUTE` on the prepared handle makes the CAS replace the cursor those rows
fetch from, and on CUBRID 11.2 they went on to read the new result from
their own offset, with no error. While rows from a statement can still
fetch, `stmt.Query` and `stmt.Exec` without arguments therefore run the
statement's SQL with `PREPARE_AND_EXECUTE`, which gives the new execution
its own handle, as they always do with arguments. Each result set reads its
own rows.

## Known limitations

These are not data races, but they affect what a concurrent application can
expect:

- Only `QueryContext`, `ExecContext` and `Ping` watch their context.
  Preparing a statement, running a prepared statement, fetching more rows,
  `Commit` and `Rollback` have no deadline of their own: the `timeout` DSN
  option bounds only a reconnect handshake and the `CHECK_CAS` probe before
  them. A CAS that stops answering mid-request can block them, holding their
  connection, and canceling the context passed to `database/sql` does not
  interrupt them. Tracked by
  [#87](https://github.com/cubrid-lab/cubrid-go/issues/87).
- A statement closed while rows from it are open keeps its server handle,
  one of the CAS's limited query entries, until those rows are closed or
  have read their last row. `database/sql` closes its `Rows` when they are
  drained, the `Tx` ends or the context is canceled; rows that are never
  closed hold the connection, and with it the handle.

## Tests

Run with `go test -race`:

- `TestTxStmtCloseDoesNotInterleaveWithRowsFetch`: a statement prepared on a
  `Tx` is closed from another goroutine while `Rows.Next` waits for a FETCH
  reply on the same connection; the CAS must receive nothing until that reply
  has been read.
- `TestTxStmtCloseReleasesHandleOfOpenRows` and the tests in
  `stmt_close_test.go`: closing a statement while its rows are open defers
  the `CLOSE_REQ` until the last of them is done, the CAS never receives a
  `FETCH` after it, a deferred close of a handle from an earlier session
  sends nothing, and executing the statement again while its rows are open
  uses a new handle (#86). `TestTxStmtCloseWithOpenRowsIntegration` and
  `TestStmtReexecuteWithOpenRowsIntegration` check both on a live server.
- `TestRowsClosedByCancelWhileNextReconnects`: `database/sql` closes rows on
  context cancellation while `Next` reconnects; no later session receives the
  old handle.
- `TestConcurrentDBUseAcrossBrokerRestarts`: queries, prepared statements,
  fetches and transactions from eight goroutines while the broker restarts;
  no session receives a handle it did not issue.
- `TestRowsFetchHoldsConnMutex`, `TestRowsCloseHoldsConnMutex`: direct driver
  use; FETCH and CLOSE_REQ hold the connection mutex until their reply has
  been read.
- `TestPingContextCanceled`, `TestDBPingContextDiscardsDeadConn` and the
  other tests in `ping_test.go`: `db.PingContext` sends `GET_DB_VERSION` on a
  pooled connection, returns promptly when its context is canceled or its
  deadline passes, and a dead connection returns `driver.ErrBadConn` and is
  discarded from the pool (#88).
- `TestCancelWatcherSkipsSocketClearedByReconnect`,
  `TestLateCancelDoesNotLeakIntoNextRequest`, `TestCancelDuringReconnectDial`
  and the stale-handle tests in `stale_handle_test.go` cover the cancel
  watcher and handle ownership across reconnects.
