# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
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
