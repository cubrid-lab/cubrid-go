# API Reference

Complete reference for the `cubrid-go` pure Go database driver.

## Table of Contents

- [Installation](#installation)
- [Driver Registration](#driver-registration)
- [DSN Format](#dsn-format)
- [database/sql Interface](#databasesql-interface)
  - [sql.Open](#sqlopen)
  - [db.Query / db.QueryRow](#dbquery--dbqueryrow)
  - [db.Exec](#dbexec)
  - [db.Prepare](#dbprepare)
  - [db.Begin (Transactions)](#dbbegin-transactions)
  - [db.Ping](#dbping)
- [Connection Pool Configuration](#connection-pool-configuration)
- [Parameter Binding](#parameter-binding)
- [Type Mapping](#type-mapping)
  - [Go → CUBRID SQL Literals](#go--cubrid-sql-literals)
  - [CUBRID → Go Types](#cubrid--go-types)
- [Error Types](#error-types)
- [Server Version](#server-version)
- [Result Interface](#result-interface)

---

## Installation

```bash
go get github.com/cubrid-labs/cubrid-go
```

**Requirements**: Go 1.21 or later.

---

## Driver Registration

The driver registers itself with `database/sql` automatically via `init()`. Import the package with a blank identifier to trigger registration:

```go
import (
    "database/sql"
    _ "github.com/cubrid-labs/cubrid-go"
)
```

The registered driver name is `"cubrid"`.

---

## DSN Format

```
cubrid://[user[:password]]@host[:port]/database[?autocommit=true&timeout=30s]
```

### Parameters

| Parameter    | Type       | Default     | Description                                           |
|:-------------|:-----------|:------------|:------------------------------------------------------|
| `user`       | `string`   | `""`        | Database user (e.g., `dba`)                            |
| `password`   | `string`   | `""`        | Database password                                      |
| `host`       | `string`   | `localhost` | CUBRID broker hostname or IP address                   |
| `port`       | `int`      | `33000`     | CUBRID broker port                                     |
| `database`   | `string`   | *(required)* | Target database name                                  |
| `autocommit` | `bool`     | `true`      | Enable auto-commit mode                                |
| `timeout`    | `duration` | `30s`       | Connection timeout ([Go duration](https://pkg.go.dev/time#ParseDuration) format) |

### DSN Examples

```go
// Minimal — connects as anonymous user to localhost:33000
"cubrid://localhost/demodb"

// With user (no password)
"cubrid://dba@localhost:33000/demodb"

// With user and password
"cubrid://admin:secretpass@db.example.com:33000/production"

// Disable auto-commit (for manual transaction control)
"cubrid://dba@localhost:33000/demodb?autocommit=false"

// Custom timeout
"cubrid://dba@localhost:33000/demodb?timeout=10s"

// All options combined
"cubrid://admin:pass@db.example.com:33000/mydb?autocommit=false&timeout=60s"
```

### DSN Parsing Errors

| Condition | Error |
|:----------|:------|
| Invalid URL syntax | `cubrid: invalid DSN "...": <parse error>` |
| Wrong scheme (not `cubrid://`) | `cubrid: DSN scheme must be 'cubrid', got "..."` |
| Invalid port number | `cubrid: invalid port "...": <parse error>` |
| Missing database name | `cubrid: database name is required in DSN` |

---

## database/sql Interface

cubrid-go implements the standard `database/sql/driver` interfaces:

| Interface | Implementation | Description |
|:----------|:---------------|:------------|
| `driver.Driver` | `Driver` | Parses DSN, creates connections |
| `driver.Conn` | `conn` | TCP connection to CAS broker |
| `driver.Stmt` | `stmt` | Server-side prepared statement |
| `driver.Rows` | `rows` | Lazy result set with server-side cursor |
| `driver.Tx` | `tx` | Transaction (COMMIT / ROLLBACK) |
| `driver.Result` | `result` | LastInsertId + RowsAffected |
| `driver.Pinger` | `conn` | Connection health check |

### sql.Open

Opens a connection pool to the CUBRID database.

```go
db, err := sql.Open("cubrid", "cubrid://dba:@localhost:33000/demodb")
if err != nil {
    log.Fatal("failed to open database:", err)
}
defer db.Close()

// Verify the connection works
if err := db.Ping(); err != nil {
    log.Fatal("cannot reach CUBRID:", err)
}
```

> **Note**: `sql.Open` does NOT immediately connect. The first actual TCP connection happens on the first query or `Ping()`.

### db.Query / db.QueryRow

Execute a SELECT and iterate over rows:

```go
// Query multiple rows
rows, err := db.Query("SELECT name, nation_code FROM athlete WHERE nation_code = ?", "KOR")
if err != nil {
    log.Fatal(err)
}
defer rows.Close()

for rows.Next() {
    var name, nation string
    if err := rows.Scan(&name, &nation); err != nil {
        log.Fatal(err)
    }
    fmt.Printf("%s (%s)\n", name, nation)
}
if err := rows.Err(); err != nil {
    log.Fatal(err)
}
```

Query a single row:

```go
var count int
err := db.QueryRow("SELECT COUNT(*) FROM athlete WHERE nation_code = ?", "KOR").Scan(&count)
if err != nil {
    log.Fatal(err)
}
fmt.Println("Athletes:", count)
```

### db.Exec

Execute INSERT, UPDATE, DELETE, or DDL statements:

```go
// INSERT
result, err := db.Exec(
    "INSERT INTO athlete (name, nation_code, gender, event) VALUES (?, ?, ?, ?)",
    "Hong Gildong", "KOR", "M", "Marathon",
)
if err != nil {
    log.Fatal(err)
}
id, _ := result.LastInsertId()
affected, _ := result.RowsAffected()
fmt.Printf("Inserted ID=%d, affected=%d\n", id, affected)

// UPDATE
result, err = db.Exec(
    "UPDATE athlete SET event = ? WHERE name = ?",
    "Sprint", "Hong Gildong",
)

// DELETE
result, err = db.Exec("DELETE FROM athlete WHERE nation_code = ?", "ZZZ")

// DDL
_, err = db.Exec(`
    CREATE TABLE IF NOT EXISTS products (
        id INTEGER AUTO_INCREMENT PRIMARY KEY,
        name VARCHAR(100) NOT NULL,
        price DOUBLE DEFAULT 0.0,
        created_at DATETIME DEFAULT SYSDATETIME
    )
`)
```

### db.Prepare

Create a reusable prepared statement for repeated execution:

```go
stmt, err := db.Prepare("INSERT INTO athlete (name, nation_code) VALUES (?, ?)")
if err != nil {
    log.Fatal(err)
}
defer stmt.Close()

athletes := []struct{ name, nation string }{
    {"Kim Yuna", "KOR"},
    {"Usain Bolt", "JAM"},
    {"Eliud Kipchoge", "KEN"},
}

for _, a := range athletes {
    _, err := stmt.Exec(a.name, a.nation)
    if err != nil {
        log.Printf("failed to insert %s: %v", a.name, err)
    }
}
```

### db.Begin (Transactions)

Start a transaction — auto-commit is disabled until Commit or Rollback:

```go
tx, err := db.Begin()
if err != nil {
    log.Fatal(err)
}

_, err = tx.Exec("INSERT INTO orders (customer_id, total) VALUES (?, ?)", 42, 99.99)
if err != nil {
    tx.Rollback()
    log.Fatal(err)
}

_, err = tx.Exec("UPDATE inventory SET quantity = quantity - 1 WHERE product_id = ?", 7)
if err != nil {
    tx.Rollback()
    log.Fatal(err)
}

if err := tx.Commit(); err != nil {
    log.Fatal("commit failed:", err)
}
```

> **Behavior**: `Begin()` sets `autoCommit = false`. After `Commit()` or `Rollback()`, auto-commit is restored to `true`.

### db.Ping

Check that the connection to CUBRID is alive:

```go
if err := db.Ping(); err != nil {
    log.Fatal("CUBRID is unreachable:", err)
}
```

Ping sends a `GET_DB_VERSION` request (FC=15) to the server and expects a version string back.

---

## Connection Pool Configuration

cubrid-go uses Go's built-in `database/sql` connection pool. Configure it after `sql.Open`:

```go
db, _ := sql.Open("cubrid", dsn)

// Maximum number of open connections (default: unlimited)
db.SetMaxOpenConns(25)

// Maximum idle connections kept in the pool (default: 2)
db.SetMaxIdleConns(5)

// Maximum lifetime for a connection (default: unlimited)
db.SetConnMaxLifetime(30 * time.Minute)

// Maximum idle time for a connection (default: unlimited)
db.SetConnMaxIdleTime(5 * time.Minute)
```

### Recommended Settings

| Workload | MaxOpen | MaxIdle | MaxLifetime | MaxIdleTime |
|:---------|:--------|:--------|:------------|:------------|
| Low traffic | 5 | 2 | 30m | 5m |
| Web server | 25 | 10 | 30m | 5m |
| High throughput | 50–100 | 25 | 15m | 3m |
| Background worker | 2–5 | 1 | 1h | 10m |

---

## Parameter Binding

cubrid-go uses `?` as the placeholder marker, consistent with MySQL-style drivers.

```go
// Positional parameters (the only supported style)
db.Query("SELECT * FROM t WHERE col1 = ? AND col2 = ?", val1, val2)
```

### How It Works

Parameters are interpolated **client-side** into the SQL string before sending to the server. The driver:

1. Splits the SQL on `?` placeholders
2. Converts each Go value to a CUBRID SQL literal via `FormatValue()`
3. Concatenates the parts into a complete SQL string
4. Sends the interpolated SQL via `PREPARE_AND_EXECUTE` (FC=41)

> **Security Note**: String values are escaped (single quotes doubled, backslashes escaped) to prevent SQL injection. However, client-side interpolation means the driver is responsible for escaping — not the database server. Always use parameterized queries; never concatenate user input into SQL strings.

### Supported Value Types

| Go Type | CUBRID Literal | Example |
|:--------|:---------------|:--------|
| `nil` | `NULL` | `NULL` |
| `bool` | `0` or `1` | `true` → `1` |
| `int64` | Integer literal | `42` → `42` |
| `float64` | Float literal | `3.14` → `3.14` |
| `string` | Escaped string | `"O'Brien"` → `'O''Brien'` |
| `[]byte` | Hex literal | `[]byte{0xCA, 0xFE}` → `X'cafe'` |
| `time.Time` | DATETIME literal | → `DATETIME'2024-01-15 09:30:00.000'` |

Unsupported types return an error: `cubrid: unsupported value type <T>`.

---

## Type Mapping

### Go → CUBRID SQL Literals

Used when sending parameters to the server:

| Go Type | CUBRID SQL | Notes |
|:--------|:-----------|:------|
| `nil` | `NULL` | |
| `bool` (true) | `1` | CUBRID has no native BOOLEAN |
| `bool` (false) | `0` | Mapped to SMALLINT |
| `int64` | `42` | |
| `float64` | `3.14` | |
| `string` | `'escaped'` | Single quotes doubled, backslashes escaped |
| `[]byte` | `X'cafe'` | Hex-encoded binary |
| `time.Time` | `DATETIME'2024-01-15 09:30:00.123'` | Millisecond precision |

### CUBRID → Go Types

When reading result rows, the driver returns these Go types:

| CUBRID Type | Go Type | Notes |
|:------------|:--------|:------|
| `SMALLINT`, `INTEGER`, `BIGINT` | `int64` | |
| `FLOAT`, `DOUBLE`, `MONETARY` | `float64` | |
| `NUMERIC` | `string` | Arbitrary precision preserved |
| `CHAR`, `VARCHAR`, `NCHAR`, `NVARCHAR`, `STRING` | `string` | |
| `DATE` | `time.Time` | Normalized to UTC |
| `TIME` | `time.Time` | Normalized to UTC |
| `TIMESTAMP`, `DATETIME` | `time.Time` | Normalized to UTC |
| `BIT`, `BIT VARYING` | `[]byte` | |
| `BLOB`, `CLOB` | `[]byte` | Raw bytes only |
| `SET`, `MULTISET`, `SEQUENCE` | `string` | Server-formatted representation |
| `ENUM` | `string` | |
| `NULL` | `nil` | |

---

## Error Types

cubrid-go classifies errors into three categories based on the error message content from the server:

### CubridError (Base)

The base error type. All CUBRID errors embed this struct.

```go
type CubridError struct {
    Code    int    // CUBRID error code
    Message string // Error message from the server
}
```

**Format**: `cubrid: [<code>] <message>`

### IntegrityError

Raised for constraint violations — unique key, foreign key, etc.

```go
type IntegrityError struct{ CubridError }
```

**Triggered by messages containing**: `unique`, `duplicate`, `foreign key`, `constraint violation`

```go
_, err := db.Exec("INSERT INTO t (unique_col) VALUES (?)", duplicateValue)
if err != nil {
    var intErr *cubrid.IntegrityError
    if errors.As(err, &intErr) {
        fmt.Printf("Constraint violation (code %d): %s\n", intErr.Code, intErr.Message)
    }
}
```

### ProgrammingError

Raised for SQL syntax errors, missing tables, or missing columns.

```go
type ProgrammingError struct{ CubridError }
```

**Triggered by messages containing**: `syntax`, `unknown class`, `does not exist`, `not found`

```go
_, err := db.Exec("SELEC * FORM invalid_table")
if err != nil {
    var progErr *cubrid.ProgrammingError
    if errors.As(err, &progErr) {
        fmt.Printf("SQL error (code %d): %s\n", progErr.Code, progErr.Message)
    }
}
```

### OperationalError

Raised for network failures, connection issues, or server-side operational problems.

```go
type OperationalError struct{ CubridError }
```

```go
_, err := db.Query("SELECT 1")
if err != nil {
    var opErr *cubrid.OperationalError
    if errors.As(err, &opErr) {
        fmt.Println("Connection problem:", opErr.Message)
    }
}
```

### Error Classification Logic

The `newError()` function inspects the error message (case-insensitive keyword matching) to determine the specific error type:

```
Message contains "unique", "duplicate", "foreign key", "constraint violation"
  → IntegrityError

Message contains "syntax", "unknown class", "does not exist", "not found"
  → ProgrammingError

Everything else
  → CubridError (base)
```

---

## Server Version

Retrieve the CUBRID server version string:

```go
// Using the underlying driver connection
db, _ := sql.Open("cubrid", dsn)
conn, err := db.Conn(context.Background())
if err != nil {
    log.Fatal(err)
}
defer conn.Close()

// The Ping method internally uses GET_DB_VERSION
// To get the version string, use a raw SQL query:
var version string
err = db.QueryRow("SELECT VERSION()").Scan(&version)
if err != nil {
    log.Fatal(err)
}
fmt.Println("CUBRID version:", version)
```

---

## Result Interface

The `driver.Result` returned by `Exec()` provides:

### LastInsertId

Returns the last AUTO_INCREMENT value generated by an INSERT statement.

```go
result, err := db.Exec("INSERT INTO t (name) VALUES (?)", "test")
id, err := result.LastInsertId()
// id = the auto-generated primary key value
```

> **Implementation detail**: cubrid-go reads `SELECT LAST_INSERT_ID()` on the same CAS session as the INSERT. After an autocommitted statement the driver checks the CAS with `CHECK_CAS` (FC=32) and reconnects only when that check fails, so the session that generated the id is kept.

### RowsAffected

Returns the number of rows affected by an INSERT, UPDATE, or DELETE statement.

```go
result, err := db.Exec("UPDATE products SET price = price * 1.1 WHERE category = ?", "electronics")
affected, err := result.RowsAffected()
fmt.Printf("%d products updated\n", affected)
```

---

## Protocol Details

### Connection Sequence

```mermaid
sequenceDiagram
    participant Client
    participant Broker as Broker (port 33000)
    participant CAS

    Client->>Broker: ClientInfoExchange (10 B)
    Broker-->>Client: CAS port (4 B)
    Client->>CAS: OpenDatabase (628 B)
    CAS-->>Client: Session info
    Client->>CAS: PrepareAndExecute (FC=41)
    CAS-->>Client: Result set + initial rows
    Client->>CAS: Fetch (FC=8)
    CAS-->>Client: More rows
```

### Wire Format

All messages use big-endian byte order. Framed responses follow this structure:

```
[DATA_LENGTH (4 bytes)][CAS_INFO (4 bytes)][body (DATA_LENGTH bytes)]
```

### Key Function Codes

| Code | Name | Purpose |
|:-----|:-----|:--------|
| 1 | `END_TRAN` | COMMIT or ROLLBACK |
| 2 | `PREPARE` | Prepare a SQL statement |
| 3 | `EXECUTE` | Execute a prepared statement |
| 6 | `CLOSE_REQ_HANDLE` | Release a server-side handle |
| 8 | `FETCH` | Fetch rows from a cursor |
| 15 | `GET_DB_VERSION` | Get server version (used for Ping) |
| 31 | `CON_CLOSE` | Close the connection |
| 41 | `PREPARE_AND_EXECUTE` | Prepare + execute in one round-trip |
