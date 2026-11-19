# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Upgrade notes

- **Module path moved to `github.com/cubrid-lab/cubrid-go`.** The repository
  lives under the `cubrid-lab` organization, and starting with the first
  release after v0.2.1 the Go module path is `github.com/cubrid-lab/cubrid-go`
  (GORM dialector: `github.com/cubrid-lab/cubrid-go/dialector`). Releases up
  to and including v0.2.1 keep their published path,
  `github.com/cubrid-labs/cubrid-go`, and stay fetchable under it through the
  module proxy and the GitHub redirect. Newer releases exist only under the
  new path: requesting one through the old path fails with a "module declares
  its path as github.com/cubrid-lab/cubrid-go" error. To migrate:

  ```sh
  go get github.com/cubrid-lab/cubrid-go@latest
  grep -rl 'github.com/cubrid-labs/cubrid-go' --include='*.go' . \
    | xargs sed -i 's#github.com/cubrid-labs/cubrid-go#github.com/cubrid-lab/cubrid-go#g'
  go mod tidy
  ```

  `go mod tidy` drops the old `github.com/cubrid-labs/cubrid-go` requirement
  once no file imports it. On macOS use `sed -i ''`. Do not import both paths
  in one program: each path registers the `cubrid` driver name, and
  `database/sql` panics on the second registration.

### Fixed
- **Fetching and closing rows now hold the connection lock (#20):**
  `Rows.Next` (when it fetches the next block of rows) and `Rows.Close` (when
  the rows own their server handle) sent their request, and could reconnect,
  without the connection's mutex, which every other request path holds. A
  caller that used the driver's `driver.Conn` directly from two goroutines
  could therefore interleave two requests on one CAS socket, read another
  request's reply, or have a reconnect replace the socket under a running
  request. Applications that go through `database/sql` were not affected,
  because `database/sql` already serializes every call on one connection.
- **A canceled request could wait for the `CHECK_CAS` deadline, or crash the
  process (#39):** When a context was canceled just before the
  out-of-transaction `CHECK_CAS` check set its own socket deadline, that
  deadline replaced the cancellation, so the request returned only after the
  `timeout` DSN option (30s by default) or never with `timeout=0`, if the CAS
  stopped answering. A cancellation is now sticky for the rest of the request:
  every socket deadline the request sets afterwards keeps the socket expired,
  and the request returns `context.Canceled` promptly. The cancel watcher also
  read the connection's socket while a reconnect replaced it; canceling while
  the driver was dialing the broker dereferenced a nil socket and panicked.
  The watcher now reads the socket under a lock and skips a missing one, and
  it is stopped and waited for before the request ends, so a late
  cancellation cannot expire the socket of the next request on the
  connection.
- **Canceling a request during a reconnect (#39):** The reconnect after a
  failed `CHECK_CAS` now dials the broker and the CAS with the request's
  context and lets the cancel watcher interrupt the broker and OpenDatabase
  handshake, so canceling the request (or reaching its deadline) ends the
  reconnect promptly, also with `timeout=0`. A reconnect that fails for any
  reason now retires the connection: a handshake interrupted part way could
  leave the OpenDatabase reply unread on the new socket, and the next request
  on that connection read it as the answer to its own request. The next
  request now returns `driver.ErrBadConn`, so `database/sql` opens a new
  connection.
- **Prevent duplicated writes after a lost CAS response (#41):** A request
  accepted by the socket can already have executed at the server even if
  CUBRID drops the reply. `roundTrip` now returns a non-retriable
  `*OperationalError` and retires the physical session for a lost reply
  or a partially sent request. Only a write that accepted zero request
  bytes can return `driver.ErrBadConn` for `database/sql` to retry.
  Deterministic fake-CAS regressions cover lost replies, zero-byte
  writes, partial writes and unexpected short writes.
- **A prepared statement could run another statement after a reconnect
  (#40)**: Prepared statement handles were sent on whatever CAS session the
  connection had at the time. After a reconnect (for example after
  `cubrid broker restart`), the new session could have prepared a different
  statement under the same handle number, so executing an older statement ran
  that one instead; a `DELETE` could run in place of an `INSERT`. The driver
  now records which physical session created each statement and result-set
  handle and checks it after the reconnect check and before sending. A
  statement from an earlier session returns `driver.ErrBadConn` before anything
  is sent, so `database/sql` prepares it again on another connection; fetching
  from an earlier session's result set returns an `*OperationalError`; and
  closing either sends no `CLOSE_REQ_HANDLE` on the new session.
- **`LastInsertId()` returned 0 (#26)**: After an autocommitted statement
  the CAS reports "out of transaction", and the driver treated that as a
  released CAS and opened a new session before every following request. The
  `SELECT LAST_INSERT_ID()` lookup then ran on a new session and returned
  NULL. The driver now checks the CAS with `CHECK_CAS` (FC=32) on the same
  socket and reconnects only when that check fails, as the official JDBC
  driver does. Session state, including the generated AUTO_INCREMENT id, is
  kept, and autocommit mode no longer reconnects after every statement.
  The check is bounded by the `timeout` DSN option and the request deadline.
  If the request is canceled during the check, the driver does not reconnect
  or send the statement, and the connection is discarded.
- **`no_backslash_escapes` data corruption (#27)**: String parameters were
  always escaped MySQL-style (`\` → `\\`), which corrupts data on CUBRID's
  default `no_backslash_escapes=yes` setting. The driver now negotiates the
  server's escaping mode once per physical connection (probing with
  `SELECT CHAR_LENGTH('\\')`) and escapes string literals accordingly. If the
  mode cannot be determined, execution fails instead of guessing. `string`
  parameters containing NUL (`0x00`) or Ctrl-Z (`0x1A`) are now rejected; use a
  `[]byte` parameter for binary data.

### Added
- `InterpolateArgsWithMode` / `FormatValueWithMode` and the `StringEscapeMode`
  type for explicit, connection-aware string escaping. `InterpolateArgs` and
  `FormatValue` remain as literal-backslash (CUBRID-default) wrappers for
  conn-less callers such as the GORM dialector's `Explain`.

## [0.1.0] - 2026-03-13

### Fixed
- **Broker port 0 handling**: Reuse broker connection when newPort == 0
- **recv() missing CAS_INFO**: Read dataLen + SizeCASInfo bytes
- **Statement type constants**: Corrected SELECT=21, INSERT=20, UPDATE=22, DELETE=23
- **Server-side bind params**: Switched to client-side SQL interpolation
- **fetchLastInsertID**: Changed to `SELECT LAST_INSERT_ID()` query

### Added
- GORM dialector with full DDL support
- AUTO_INCREMENT DDL emission for GORM models
- Integration tests against live CUBRID instance
- llms.txt for AI agent discoverability
- PRD with Example-first Design Philosophy
