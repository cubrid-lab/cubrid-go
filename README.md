# cubrid-go

**Pure Go database driver for CUBRID** — `database/sql` interface + GORM dialector, no CGO required.

<!-- BADGES:START -->
[![Go Reference](https://pkg.go.dev/badge/github.com/cubrid-labs/cubrid-go.svg)](https://pkg.go.dev/github.com/cubrid-labs/cubrid-go)
[![v0.2.1](https://img.shields.io/badge/version-v0.2.1-blue.svg)](https://github.com/cubrid-labs/cubrid-go/releases)
[![Go 1.21+](https://img.shields.io/badge/go-1.21%2B-00ADD8.svg)](https://go.dev)
[![CI](https://github.com/cubrid-labs/cubrid-go/actions/workflows/ci.yml/badge.svg)](https://github.com/cubrid-labs/cubrid-go/actions/workflows/ci.yml)
[![license](https://img.shields.io/github/license/cubrid-labs/cubrid-go)](https://github.com/cubrid-labs/cubrid-go/blob/main/LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/cubrid-labs/cubrid-go)](https://github.com/cubrid-labs/cubrid-go)
<!-- BADGES:END -->

## Why cubrid-go?

| | cubrid-go | CCI (C interface) |
|:---|:---|:---|
| **CGO Required** | No — pure Go | Yes |
| **Cross-compilation** | `GOOS=linux GOARCH=arm64 go build` | Requires C toolchain |
| **database/sql** | Native interface | Wrapper needed |
| **GORM Support** | Built-in dialector | Not available |
| **Connection Pool** | Go standard (`database/sql`) | Manual management |
| **Deployment** | Single binary | Shared library dependency |

cubrid-go speaks the CUBRID CAS protocol directly over TCP. No shared libraries, no CGO, no external dependencies — just `go get` and start coding.

## Installation

```bash
go get github.com/cubrid-labs/cubrid-go
```

**Requirements**: Go 1.21+

## Quick Start

### database/sql

```go
package main

import (
    "database/sql"
    "fmt"
    "log"

    _ "github.com/cubrid-labs/cubrid-go"
)

func main() {
    db, err := sql.Open("cubrid", "cubrid://dba:@localhost:33000/demodb")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    // Verify connection
    if err := db.Ping(); err != nil {
        log.Fatal(err)
    }
    fmt.Println("Connected to CUBRID!")

    // Query
    rows, err := db.Query("SELECT name, nation_code FROM athlete WHERE nation_code = ?", "KOR")
    if err != nil {
        log.Fatal(err)
    }
    defer rows.Close()

    for rows.Next() {
        var name, nation string
        rows.Scan(&name, &nation)
        fmt.Printf("%s (%s)\n", name, nation)
    }
}
```

### GORM

```go
package main

import (
    "fmt"
    "log"

    "gorm.io/gorm"
    cubrid "github.com/cubrid-labs/cubrid-go/dialector"
)

type Athlete struct {
    Code       int    `gorm:"primaryKey;autoIncrement"`
    Name       string `gorm:"size:40;not null"`
    NationCode string `gorm:"size:3"`
    Gender     string `gorm:"size:1"`
    Event      string `gorm:"size:40"`
}

func main() {
    db, err := gorm.Open(cubrid.Open("cubrid://dba:@localhost:33000/demodb"), &gorm.Config{})
    if err != nil {
        log.Fatal(err)
    }

    // Auto-migrate creates the table if it doesn't exist
    db.AutoMigrate(&Athlete{})

    // Create
    db.Create(&Athlete{Name: "Hong Gildong", NationCode: "KOR", Gender: "M", Event: "Marathon"})

    // Read
    var athletes []Athlete
    db.Where("nation_code = ?", "KOR").Find(&athletes)
    for _, a := range athletes {
        fmt.Printf("%s - %s\n", a.Name, a.Event)
    }

    // Update
    db.Model(&Athlete{}).Where("name = ?", "Hong Gildong").Update("event", "Sprint")

    // Delete
    db.Delete(&Athlete{}, "nation_code = ?", "ZZZ")
}
```

## DSN Format

```
cubrid://[user[:password]]@host[:port]/database[?autocommit=true&timeout=30s]
```

| Parameter    | Default     | Description                         |
|:-------------|:------------|:------------------------------------|
| `host`       | `localhost` | CUBRID broker host                  |
| `port`       | `33000`     | CUBRID broker port                  |
| `database`   | *(required)* | Target database name               |
| `user`       | `""`        | Database user                       |
| `password`   | `""`        | Database password                   |
| `autocommit` | `true`      | Enable/disable auto-commit          |
| `timeout`    | `30s`       | Connection timeout ([Go duration](https://pkg.go.dev/time#ParseDuration)) |

**Examples**:

```go
// Local development
"cubrid://dba:@localhost:33000/demodb"

// Remote with credentials
"cubrid://admin:secret@db.example.com:33000/production"

// Manual transaction mode
"cubrid://dba:@localhost:33000/demodb?autocommit=false"

// Custom timeout
"cubrid://dba:@localhost:33000/demodb?timeout=60s"
```

## Connection Pooling

`cubrid-go` uses Go's standard `database/sql` pool, so the usual pool tuning APIs work as expected.

```go
package main

import (
    "context"
    "database/sql"
    "log"
    "time"

    _ "github.com/cubrid-labs/cubrid-go"
)

func main() {
    db, err := sql.Open("cubrid", "cubrid://dba:@localhost:33000/demodb")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    // Configure the shared connection pool.
    db.SetMaxOpenConns(25)
    db.SetMaxIdleConns(5)
    db.SetConnMaxLifetime(5 * time.Minute)
    db.SetConnMaxIdleTime(1 * time.Minute)

    // Verify that the broker is reachable before serving traffic.
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()

    if err := db.PingContext(ctx); err != nil {
        log.Fatal(err)
    }
}
```

### Pool settings

- `SetMaxOpenConns(25)`: caps the total number of open connections. Start with a conservative number and scale up only if your broker and workload need it.
- `SetMaxIdleConns(5)`: keeps a small number of warm idle connections ready for bursts without holding on to the full pool.
- `SetConnMaxLifetime(5 * time.Minute)`: rotates long-lived connections so stale sessions do not accumulate forever.
- `SetConnMaxIdleTime(1 * time.Minute)`: closes connections that have been idle for too long, which is useful for spiky traffic.

### Recommended starting point

For a typical web service, a good baseline is:

- `MaxOpenConns`: `10` to `25`
- `MaxIdleConns`: `2` to `5`
- `ConnMaxLifetime`: `5m` to `30m`
- `ConnMaxIdleTime`: `1m` to `5m`

Measure under production-like load and adjust from there. If requests begin queueing on the Go side, raise `MaxOpenConns` carefully. If the database is saturated, lower it.

### CUBRID-specific notes

- The pool limit should stay below the available CUBRID broker worker capacity. Setting `MaxOpenConns` higher than the broker can actually serve only shifts contention downstream.
- `PingContext()` is a good readiness or startup check because it fails fast when the broker is unavailable or the DSN is invalid.
- If your deployment has multiple application instances, size the pool per instance, not just per service. For example, `25` open connections across `4` replicas can become `100` broker sessions.

## Supported Features

| Feature | Status | Notes |
|:--------|:-------|:------|
| Pure TCP (no shared library) | ✅ | No CGO, no external deps |
| `database/sql` driver | ✅ | Full interface |
| Parameterized queries (`?`) | ✅ | Client-side interpolation |
| Transactions (`Begin/Commit/Rollback`) | ✅ | |
| GORM dialector | ✅ | Models, migrations, CRUD |
| GORM AutoMigrate | ✅ | Create tables, add columns |
| Server-side cursor / lazy fetch | ✅ | Batches of 100 rows |
| Result streaming (FETCH) | ✅ | Memory-efficient |
| Last insert ID | ✅ | Via `SELECT LAST_INSERT_ID()` |
| Connection pool | ✅ | Go standard `database/sql` |
| Connection health check (Ping) | ✅ | Via `GET_DB_VERSION` |
| LOB (BLOB/CLOB) | ⚠️ | Raw bytes only |
| Timezone-aware types | ⚠️ | UTC only |
| ON CONFLICT / UPSERT | ❌ | Use raw SQL workaround |

## Type Mapping

### Go → CUBRID (Parameters)

| Go Type | CUBRID Literal | Example |
|:--------|:---------------|:--------|
| `nil` | `NULL` | |
| `bool` | `0` / `1` | CUBRID has no BOOLEAN |
| `int64` | Integer | `42` |
| `float64` | Float | `3.14` |
| `string` | `'escaped'` | Escaped per the connection's negotiated `no_backslash_escapes` mode (see below) |
| `[]byte` | `X'cafe'` | Hex-encoded |
| `time.Time` | `DATETIME'...'` | Millisecond precision |

#### String escaping and `no_backslash_escapes`

String parameters are interpolated as SQL string literals, so they must be
escaped according to the server's `no_backslash_escapes` system parameter. The
driver negotiates this once per physical connection (on the first parameterized
query) by probing with `SELECT CHAR_LENGTH('\\')` and caches the result:

- **`no_backslash_escapes=yes`** (the CUBRID default): a backslash is an
  ordinary character; only the single quote is doubled (`'` → `''`).
- **`no_backslash_escapes=no`**: backslash-escape processing is active, so the
  backslash is doubled and CR/LF are backslash-escaped in addition to doubling
  the single quote.

If the mode cannot be determined, the driver returns an error rather than
guessing, because an incorrect mode would silently corrupt string data.

> **Binary data**: a `string` parameter containing a NUL (`0x00`) or Ctrl-Z
> (`0x1A`) byte is rejected — CUBRID cannot represent these safely inside a
> string literal. Pass such values as a `[]byte` parameter (encoded as `X'..'`)
> instead.

### CUBRID → Go (Results)

| CUBRID Type | Go Type |
|:------------|:--------|
| `SMALLINT`, `INTEGER`, `BIGINT` | `int64` |
| `FLOAT`, `DOUBLE`, `MONETARY` | `float64` |
| `NUMERIC` | `string` |
| `CHAR`, `VARCHAR`, `STRING` | `string` |
| `DATE`, `TIME`, `DATETIME`, `TIMESTAMP` | `time.Time` (UTC) |
| `BIT`, `BIT VARYING`, `BLOB`, `CLOB` | `[]byte` |
| `SET`, `MULTISET`, `SEQUENCE` | `string` |

## GORM Type Mapping

| GORM Field Type | CUBRID SQL Type |
|:----------------|:----------------|
| `Bool` | `SMALLINT` |
| `Int` (≤16 bits) | `SMALLINT` |
| `Int` (≤32 bits) | `INTEGER` |
| `Int` (>32 bits) | `BIGINT` |
| `Float` (≤32 bits) | `FLOAT` |
| `Float` (>32 bits) | `DOUBLE` |
| `String` | `VARCHAR(n)` (default 256) |
| `String` (very large) | `STRING` |
| `Time` | `DATETIME` |
| `Bytes` | `BLOB` |
| Auto-increment (≤32) | `INTEGER AUTO_INCREMENT` |
| Auto-increment (>32) | `BIGINT AUTO_INCREMENT` |

## Error Types

```go
import "github.com/cubrid-labs/cubrid-go"

// Base error
var cubridErr *cubrid.CubridError

// Constraint violation (unique, foreign key)
var integrityErr *cubrid.IntegrityError

// SQL syntax error, missing table/column
var progErr *cubrid.ProgrammingError

// Network/connection failure
var opErr *cubrid.OperationalError

// Use errors.As for type checking
if errors.As(err, &integrityErr) {
    fmt.Printf("Constraint violation: code=%d msg=%s\n", integrityErr.Code, integrityErr.Message)
}
```

## Architecture

```mermaid
flowchart TD
    A[Application] --> B[cubrid-go database/sql driver]
    B --> C[CAS Protocol net.Conn]
    C --> D[CUBRID Server]
```

```mermaid
flowchart TD
    R[cubrid-go/] --> C1[constants.go\nCAS protocol constants function codes, data types]
    R --> C2[packet.go\nPacketWriter / PacketReader big-endian binary codec]
    R --> C3[protocol.go\nHigh-level packet builders and parsers]
    R --> C4[errors.go\nCubridError, IntegrityError, ProgrammingError, OperationalError]
    R --> C5[types.go\nFormatValue Go to CUBRID type conversion]
    R --> C6[conn.go\nTCP connection + two-step broker handshake]
    R --> C7[stmt.go\ndatabase/sql Stmt PrepareAndExecute FC=41]
    R --> C8[rows.go\ndatabase/sql Rows lazy server-side cursor FC=8]
    R --> C9[tx.go\ndatabase/sql Tx COMMIT / ROLLBACK FC=1]
    R --> C10[driver.go\nDriver registration + DSN parser]
    R --> D[dialector/]
    D --> D1[cubrid.go\nGORM Dialector + Migrator]
```

## Protocol Notes

cubrid-go speaks the CUBRID CAS (Client Application Server) protocol directly over TCP. The two-step connection sequence is:

1. **Broker handshake** — connect to `host:port`, send a 10-byte magic string, receive a redirected CAS port.
2. **Open database** — connect to the CAS port, send credentials (628 bytes), receive session info.

All subsequent requests use the `PREPARE_AND_EXECUTE` (function code 41) combined packet, and large result sets are streamed back via `FETCH` (function code 8).

## Documentation

| Document | Description |
|:---------|:------------|
| [API Reference](docs/API_REFERENCE.md) | Complete `database/sql` driver API, DSN format, type mapping, error types, protocol details |
| [GORM Guide](docs/GORM.md) | GORM dialector setup, models, migrations, CRUD, transactions, querying, schema inspection |
| [Troubleshooting](docs/TROUBLESHOOTING.md) | Connection, query, transaction, GORM, type, and performance issues with solutions |

## FAQ

### How do I connect to CUBRID with Go?

```go
import (
    "database/sql"
    _ "github.com/cubrid-labs/cubrid-go"
)
db, err := sql.Open("cubrid", "cubrid://dba:@localhost:33000/demodb")
```

### How do I use CUBRID with GORM?

```go
import (
    "gorm.io/gorm"
    cubrid "github.com/cubrid-labs/cubrid-go/dialector"
)
db, err := gorm.Open(cubrid.Open("cubrid://dba:@localhost:33000/demodb"), &gorm.Config{})
```

### Does cubrid-go require CGO?

No. cubrid-go is a pure Go implementation that speaks the CUBRID CAS protocol directly over TCP. No shared libraries or CGO needed.

### What Go version is required?

Go 1.21 or later.

### Does cubrid-go support connection pooling?

Yes. See the [Connection Pooling](#connection-pooling) section for a production-oriented example with `PingContext()` and recommended starting values.

### Does cubrid-go support transactions?

Yes. Use `db.Begin()` to start a transaction, then `tx.Commit()` or `tx.Rollback()`.

### Does GORM AutoMigrate work with CUBRID?

Yes. The GORM dialector supports `AutoMigrate` for creating and updating table schemas.

## Ecosystem

| Package | Description |
|:--------|:------------|
| [cubrid-go](https://github.com/cubrid-labs/cubrid-go) | database/sql driver + GORM dialector |
| [gorm-cubrid](https://github.com/cubrid-labs/gorm-cubrid) | GORM dialect for CUBRID |

## Roadmap

See [`ROADMAP.md`](ROADMAP.md) for this project's direction and next milestones.

For the ecosystem-wide view, see the [CUBRID Labs Ecosystem Roadmap](https://github.com/cubrid-labs/.github/blob/main/ROADMAP.md) and [Project Board](https://github.com/orgs/cubrid-labs/projects/2).

## Disclaimer

This project is part of [CUBRID Lab](https://github.com/cubrid-lab), an independent open-source initiative for CUBRID developer tooling, and is not affiliated with, sponsored by, or endorsed by CUBRID Corporation or the official CUBRID project.

## License

MIT
