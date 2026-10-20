# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
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
