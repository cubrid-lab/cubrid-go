# PRD: cubrid-go — Pure Go CUBRID Driver & GORM Dialector

## 1. Overview

**Project**: cubrid-go
**Current Version**: 0.1.0
**Status**: Working (beta)
**Repository**: [github.com/cubrid-lab/cubrid-go](https://github.com/cubrid-lab/cubrid-go)
**License**: MIT

### 1.1 Problem Statement

CUBRID had no Go driver. Developers using Go with CUBRID had to:

- Use ODBC wrappers (complex setup, CGO dependency)
- Connect through HTTP middleware (additional infrastructure)
- Avoid CUBRID entirely in Go projects

Go developers expect:

- `database/sql` compatible driver — the standard Go database interface
- `go get` installation — no CGO, no shared libraries
- GORM support — the most popular Go ORM
- Pure TCP connection — no middleware required

### 1.2 What Was Built

A complete pure Go implementation of the CUBRID CAS protocol:

- **`database/sql` driver** — standard Go database interface
- **GORM dialector** — full GORM ORM support with AutoMigrate
- **Pure Go** — no CGO, no native libraries, `go get` and done
- **Direct CAS protocol** — speaks CUBRID's binary protocol natively over TCP
- **Parameterized queries** — safe `?` placeholder parameter binding
- **Transactions** — `Begin` / `Commit` / `Rollback` via `database/sql`
- **Server-side cursors** — lazy fetch for large result sets
- **Auto-increment** — correct DDL generation for GORM AutoMigrate

---

## 2. Technical Architecture

### 2.1 Module Structure

```mermaid
flowchart TD
    R[cubrid-go/] --> C1[constants.go\nCAS protocol constants function codes, data types]
    R --> C2[packet.go\nPacketWriter / PacketReader big-endian binary codec]
    R --> C3[protocol.go\nHigh-level packet builders and parsers]
    R --> C4[errors.go\nCubridError, IntegrityError, ProgrammingError]
    R --> C5[types.go\nClient-side parameter interpolation]
    R --> C6[conn.go\nTCP connection + broker handshake]
    R --> C7[stmt.go\ndatabase/sql Stmt PrepareAndExecute]
    R --> C8[rows.go\ndatabase/sql Rows lazy server-side cursor]
    R --> C9[tx.go\ndatabase/sql Tx COMMIT / ROLLBACK]
    R --> C10[driver.go\nDriver registration + DSN parser]
    R --> D[dialector/]
    D --> D1[cubrid.go\nGORM Dialector + Migrator]
```

### 2.2 Dependency Matrix

| Package | Version | Purpose |
|---|---|---|
| Go | ≥ 1.21 | Runtime |
| GORM | ≥ 1.25 | ORM (optional, for dialector) |

**Zero runtime dependencies** for the core driver — uses only Go standard library.
GORM is required only when using the dialector.

### 2.3 DSN Format

```
cubrid://[user[:password]]@host[:port]/database[?autocommit=true&timeout=30s]
```

| Parameter | Default | Description |
|---|---|---|
| `host` | `localhost` | CUBRID broker host |
| `port` | `33000` | CUBRID broker port |
| `database` | (required) | Target database name |
| `user` | (empty) | Database user |
| `password` | (empty) | Database password |
| `autocommit` | `true` | Enable/disable auto-commit |
| `timeout` | `30s` | Connection timeout |

---

## 3. Implemented Features

### 3.1 database/sql Driver

| Feature | Status |
|---|---|
| Pure TCP (no shared library) | ✅ |
| `database/sql` driver | ✅ |
| Parameterized queries (`?`) | ✅ |
| Transactions (`Begin/Commit/Rollback`) | ✅ |
| Server-side cursor / lazy fetch | ✅ |
| Result streaming (FETCH) | ✅ |
| Last insert ID | ✅ |
| Connection pool (via `database/sql`) | ✅ |

### 3.2 GORM Dialector

| Feature | Status |
|---|---|
| GORM dialector | ✅ |
| GORM AutoMigrate | ✅ |
| AUTO_INCREMENT DDL | ✅ |
| Full CRUD operations | ✅ |
| Model-based queries | ✅ |

### 3.3 Known Limitations

| Feature | Status | Reason |
|---|---|---|
| LOB (BLOB/CLOB) | ⚠️ Raw bytes only | Full LOB streaming not implemented |
| Timezone-aware types | ⚠️ UTC only | CUBRID stores naive datetimes |
| Named parameters | ❌ | CUBRID uses positional `?` only |
| Multiple result sets | ❌ | CUBRID CAS doesn't support this |

---

## 4. CAS Protocol Implementation

cubrid-go speaks CUBRID's CAS (Client Application Server) protocol directly over TCP:

1. **Broker handshake** — connect to `host:port`, send 10-byte magic string,
   receive redirected CAS port (or port 0 = reuse connection)
2. **Open database** — connect to CAS port, send credentials (628 bytes),
   receive session info
3. **Execute queries** — `PREPARE_AND_EXECUTE` (function code 41)
4. **Fetch results** — `FETCH` (function code 8) for server-side cursors
5. **Transactions** — explicit `COMMIT` / `ROLLBACK` function codes

### Protocol Bugs Fixed (v0.1.0)

| Bug | Fix |
|---|---|
| Broker port 0 handling | Reuse broker connection when `newPort == 0` |
| `recv()` missing CAS_INFO | Read `dataLen + SizeCASInfo` bytes |
| Statement type constants wrong | SELECT=21, INSERT=20, UPDATE=22, DELETE=23 |
| Server-side bind params fail | Switched to client-side SQL interpolation |
| `fetchLastInsertID` returns 0 | Changed to `SELECT LAST_INSERT_ID()` |

---

## 5. Test Coverage

### 5.1 Integration Tests

```go
// integration_test.go — requires Docker CUBRID
func TestPingDatabase(t *testing.T)
func TestCreateTableAndInsert(t *testing.T)
func TestTransactions(t *testing.T)
func TestAutoMigrate(t *testing.T)
```

All tests validated against CUBRID 11.2 via Docker.

---

## 6. Documentation

| Document | Content |
|---|---|
| [`README.md`](README.md) | Landing page with `database/sql` and GORM examples |

---

## 7. Roadmap

### v0.2.0 — Stability & Testing

| Item | Description | Priority |
|---|---|---|
| Offline unit tests | Add tests that don't require a live database | High |
| Error mapping | Map CUBRID error codes to Go errors | High |
| Connection retry | Auto-reconnect on transient failures | Medium |
| Context support | Respect `context.Context` for timeouts/cancellation | Medium |

### v0.3.0 — Feature Expansion

| Item | Description | Priority |
|---|---|---|
| LOB streaming | Full BLOB/CLOB read/write support | Medium |
| Prepared statement cache | Cache prepared statements for repeated queries | Medium |
| Batch insert | Optimized multi-row insert | Low |
| Named parameters | Support `@name` parameter style | Low |

### v1.0.0 — Stable Release

| Item | Description | Priority |
|---|---|---|
| API freeze | Stabilize public API | High |
| Performance benchmarks | Benchmark vs ODBC wrapper | Medium |
| GORM v2 full compatibility | Test all GORM features | Medium |
| pkg.go.dev documentation | Complete Go doc comments | Low |

---

## 8. Architecture Decisions

### 8.1 Why Pure Go (no CGO)

CGO introduces build complexity — cross-compilation breaks, Docker images need C
toolchains, and many CI environments don't have CUBRID C libraries. Pure Go means
`go get` works everywhere with zero additional setup.

### 8.2 Why Client-side Parameter Interpolation

CUBRID's CAS protocol supports server-side parameter binding, but testing revealed
inconsistencies with certain data types. Client-side SQL interpolation with proper
escaping provides more reliable behavior across CUBRID versions.

### 8.3 Why GORM Dialector in Same Repo

The dialector is small (~300 lines) and tightly coupled to the driver's behavior.
Keeping it in the same repository avoids version synchronization issues and makes
it easier to test together.

---

## 9. Ecosystem Integration

cubrid-go is the Go arm of the CUBRID Lab ecosystem:

```mermaid
flowchart TD
    E[CUBRID Lab ecosystem] --> P[Python]
    P --> P1[pycubrid DB-API 2.0 driver]
    P --> P2[sqlalchemy-cubrid SQLAlchemy 2.0 dialect]
    E --> T[TypeScript]
    T --> T1[cubrid-client TypeScript driver]
    T --> T2[drizzle-cubrid Drizzle ORM dialect]
    E --> G[Go]
    G --> G1[cubrid-go database/sql driver this project]
    G --> G2[cubrid-go/dialector GORM dialector]
    E --> C[cubrid-cookbook runnable examples for all languages]
```

---

## 10. Example-first Design Philosophy

### Why Example-first

CUBRID's ecosystem is small compared to PostgreSQL or MySQL. For a small-ecosystem
project, the entry barrier must be minimized — users should be able to copy-paste
working code within 30 seconds of reading the documentation.

> Because the ecosystem is still small, the project provides extensive examples
> and cookbook-style documentation to lower the adoption barrier.

### Hello World — database/sql

```bash
go get github.com/cubrid-lab/cubrid-go
```

```go
package main

import (
    "database/sql"
    "fmt"
    _ "github.com/cubrid-lab/cubrid-go"
)

func main() {
    db, err := sql.Open("cubrid", "cubrid://dba:@localhost:33000/demodb")
    if err != nil {
        panic(err)
    }
    defer db.Close()

    rows, err := db.Query("SELECT * FROM athlete WHERE nation_code = ?", "KOR")
    if err != nil {
        panic(err)
    }
    defer rows.Close()

    for rows.Next() {
        var code int
        var name string
        rows.Scan(&code, &name)
        fmt.Println(code, name)
    }
}
```

### Hello World — GORM

```go
package main

import (
    "fmt"
    "gorm.io/gorm"
    cubrid "github.com/cubrid-lab/cubrid-go/dialector"
)

type Athlete struct {
    Code       int    `gorm:"primaryKey;autoIncrement"`
    Name       string `gorm:"size:40;not null"`
    NationCode string `gorm:"size:3"`
}

func main() {
    db, err := gorm.Open(cubrid.Open("cubrid://dba:@localhost:33000/demodb"), &gorm.Config{})
    if err != nil {
        panic(err)
    }

    db.AutoMigrate(&Athlete{})
    db.Create(&Athlete{Name: "Hong Gildong", NationCode: "KOR"})

    var athletes []Athlete
    db.Where("nation_code = ?", "KOR").Find(&athletes)
    fmt.Println(athletes)
}
```

### AI Agent Discoverability

Modern developers increasingly rely on AI coding assistants — Claude Code, OpenCode,
Cursor, GitHub Copilot, Devin, and others. These agents read README files, PRDs,
documentation, and example code to decide which libraries to recommend.

**More examples in documentation = higher probability of AI recommendation.**

By embedding working code directly in the PRD, README, and docs, we ensure that
AI agents can discover, learn, and recommend cubrid-go accurately.

### Cookbook Integration

There is no Go cookbook repository yet (the former `cubrid-cookbook` links are gone; [cubrid-cookbook-python](https://github.com/cubrid-lab/cubrid-cookbook-python) covers Python only). Runnable Go examples live in this repository's [README](README.md) and [docs/GORM.md](docs/GORM.md) until a Go cookbook exists.

### Inspiration from Successful Projects

Projects that succeeded partly through example-heavy documentation:

| Project | What They Did |
|---|---|
| **FastAPI** | Every endpoint documented with runnable examples; became the fastest-growing Python web framework |
| **LangChain** | Cookbook-first approach drove explosive adoption in the AI space |
| **SQLAlchemy** | Extensive ORM cookbook and tutorial; de facto Python ORM for 15+ years |
| **Pandas** | "10 Minutes to pandas" and cookbook lowered entry barrier for data science |

cubrid-go follows the same philosophy: **examples are not supplementary — they are the primary documentation.**

---

*Last updated: March 2026 · cubrid-go v0.1.0*
