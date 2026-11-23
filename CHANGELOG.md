# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0](https://github.com/cubrid-lab/cubrid-go/compare/v0.2.1...v0.3.0) (2026-11-23)


### ⚠ BREAKING CHANGES

* move the Go module to github.com/cubrid-lab/cubrid-go ([#82](https://github.com/cubrid-lab/cubrid-go/issues/82))

### Features

* add context.Context support for query cancellation ([b43a5a6](https://github.com/cubrid-lab/cubrid-go/commit/b43a5a60e01e6a429259c392c8d9ca7d0103250d))


### Bug Fixes

* audit concurrent reconnect handling and document the concurrency contract ([#89](https://github.com/cubrid-lab/cubrid-go/issues/89)) ([142ce8d](https://github.com/cubrid-lab/cubrid-go/commit/142ce8d8b64159caeaee804084fe3bda432dc990))
* **ci:** run govulncheck job on Go 1.25 ([#25](https://github.com/cubrid-lab/cubrid-go/issues/25)) ([598bba9](https://github.com/cubrid-lab/cubrid-go/commit/598bba977d1103518ff5b9c0affae40593887c7e))
* **ci:** run govulncheck on the stable Go release ([#30](https://github.com/cubrid-lab/cubrid-go/issues/30)) ([fce6cf3](https://github.com/cubrid-lab/cubrid-go/commit/fce6cf332297c83dd6c345b3e11b4e5edc5007d9))
* escape string parameters per negotiated no_backslash_escapes mode ([#27](https://github.com/cubrid-lab/cubrid-go/issues/27)) ([#28](https://github.com/cubrid-lab/cubrid-go/issues/28)) ([b7b3a5d](https://github.com/cubrid-lab/cubrid-go/commit/b7b3a5d2f9d04ed2d4b96dff22df6ca5dcebcab6))
* honour context cancellation in Prepare and prepared statements ([#99](https://github.com/cubrid-lab/cubrid-go/issues/99)) ([3dfecc3](https://github.com/cubrid-lab/cubrid-go/commit/3dfecc38fb2489613cdcf9154cb5fe172a671767))
* implement driver.Pinger and driver.Validator so pings reach the server ([#94](https://github.com/cubrid-lab/cubrid-go/issues/94)) ([4bf9c7c](https://github.com/cubrid-lab/cubrid-go/commit/4bf9c7c9c6b5c2be6438edbff6dbaddb4dfe8282))
* keep a statement's handle alive while its rows are open ([#93](https://github.com/cubrid-lab/cubrid-go/issues/93)) ([d057821](https://github.com/cubrid-lab/cubrid-go/commit/d0578212cfb6eabefb12a75640141b4b0c2224ae))
* keep request cancellation when the CHECK_CAS probe sets its deadline ([#85](https://github.com/cubrid-lab/cubrid-go/issues/85)) ([8f854ae](https://github.com/cubrid-lab/cubrid-go/commit/8f854aedf4ad61846799c7b097e03f48aa36d92f))
* prevent database/sql replay of ambiguous CAS writes ([11c6588](https://github.com/cubrid-lab/cubrid-go/commit/11c6588ae397920df6bcf2708d9a8c62581f0ded))
* reconnect when CAS released under KEEP_CONNECTION=AUTO ([#12](https://github.com/cubrid-lab/cubrid-go/issues/12)) ([e9db355](https://github.com/cubrid-lab/cubrid-go/commit/e9db355e64f6b191e512faa882340d31475a3665))
* refuse prepared statement handles from an earlier physical session ([#42](https://github.com/cubrid-lab/cubrid-go/issues/42)) ([8093f61](https://github.com/cubrid-lab/cubrid-go/commit/8093f6108eeedcf69b379c05e576a89e04d64310))
* return the generated AUTO_INCREMENT id from LastInsertId() ([#38](https://github.com/cubrid-lab/cubrid-go/issues/38)) ([951690a](https://github.com/cubrid-lab/cubrid-go/commit/951690a451d78dab86fc9a06c1f3cedf755b20bf))


### Miscellaneous Chores

* move the Go module to github.com/cubrid-lab/cubrid-go ([#82](https://github.com/cubrid-lab/cubrid-go/issues/82)) ([70061ec](https://github.com/cubrid-lab/cubrid-go/commit/70061ec84d64fb4f4800523d427a1cfae37e8b0a))

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
- **A failed `Open` left the CAS socket open (#20):** When the broker
  accepted the connection but the OpenDatabase step failed (for example, a
  wrong password or an unknown database name), `Driver.Open` returned the
  error without closing the socket. No connection was returned, so nothing
  could close it later and the CAS kept the session until its own timeout.
  `Open` now closes the socket on every handshake failure.
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
- [Concurrency and reconnects](docs/CONCURRENCY.md) (#20): what is safe to
  share between goroutines, how `database/sql` and the driver serialize the
  requests of one connection, which lock protects each piece of connection
  state, and how an automatic reconnect interacts with requests in flight.
  Race-detector tests through `database/sql` cover a statement closed during a
  fetch, rows closed on cancellation during a reconnect, and concurrent use of
  one pool across broker restarts. The README and API reference no longer
  claim that `db.Ping` sends `GET_DB_VERSION` on a pooled connection: the
  driver does not implement `driver.Pinger` yet (#88).
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
