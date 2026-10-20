# Troubleshooting

Common issues and solutions when using cubrid-go.

## Table of Contents

- [Connection Issues](#connection-issues)
  - [Cannot connect to CUBRID broker](#cannot-connect-to-cubrid-broker)
  - [Broker rejected connection](#broker-rejected-connection)
  - [Connection timeout](#connection-timeout)
  - [dial CAS error after broker handshake](#dial-cas-error-after-broker-handshake)
  - [Database name is required](#database-name-is-required)
  - [Prepared statement fails after a broker restart](#prepared-statement-fails-after-a-broker-restart)
- [Query Issues](#query-issues)
  - [Expected N bind args, got M](#expected-n-bind-args-got-m)
  - [Unsupported value type](#unsupported-value-type)
  - [LastInsertId returns 0](#lastinsertid-returns-0)
  - [Numeric precision loss](#numeric-precision-loss)
- [Date/time values are returned as time.Time (UTC)](#datetime-values-are-returned-as-timetime-utc)
- [Transaction Issues](#transaction-issues)
  - [DDL statements auto-commit](#ddl-statements-auto-commit)
  - [Connection left in non-autocommit mode](#connection-left-in-non-autocommit-mode)
- [GORM Issues](#gorm-issues)
  - [ON CONFLICT / upsert silently ignored](#on-conflict--upsert-silently-ignored)
  - [BOOLEAN field stored as SMALLINT](#boolean-field-stored-as-smallint)
  - [AutoMigrate fails on existing table](#automigrate-fails-on-existing-table)
  - [AUTOINCREMENT vs AUTO_INCREMENT](#autoincrement-vs-auto_increment)
  - [Empty DEFAULT clause in DDL](#empty-default-clause-in-ddl)
- [Type Issues](#type-issues)
  - [BLOB/CLOB handling](#blobclob-handling)
  - [SET/MULTISET/SEQUENCE columns](#setmultisetsequence-columns)
  - [MONETARY type](#monetary-type)
  - [No native BOOLEAN](#no-native-boolean)
- [Performance Issues](#performance-issues)
  - [Slow queries with large result sets](#slow-queries-with-large-result-sets)
  - [Connection pool exhaustion](#connection-pool-exhaustion)
  - [High memory usage on large fetches](#high-memory-usage-on-large-fetches)
- [Security Considerations](#security-considerations)
  - [Client-side parameter interpolation](#client-side-parameter-interpolation)
  - [SQL injection prevention](#sql-injection-prevention)
- [Docker / Development Environment](#docker--development-environment)
  - [Setting up a test CUBRID instance](#setting-up-a-test-cubrid-instance)
  - [Container won't start](#container-wont-start)

---

## Connection Issues

### Cannot connect to CUBRID broker

**Error**: `cubrid: [-1] dial broker localhost:33000: connect: connection refused`

**Causes and solutions**:

1. **CUBRID is not running**
   ```bash
   # Check if CUBRID broker is listening
   netstat -tlnp | grep 33000

   # Start CUBRID (Docker)
   docker compose up -d
   docker compose logs cubrid
   ```

2. **Wrong host or port**
   ```go
   // Verify the DSN
   db, err := sql.Open("cubrid", "cubrid://dba:@localhost:33000/demodb")
   //                                       ^^^^^^^^^ ^^^^^
   //                                       host      port
   ```

3. **Firewall blocking port 33000**
   ```bash
   # Allow broker port
   sudo ufw allow 33000/tcp
   ```

### Broker rejected connection

**Error**: `cubrid: [-N] broker rejected connection`

**Causes**:

- CUBRID broker has reached its maximum connection limit
- The database name doesn't exist on the server
- Broker is in maintenance mode

**Solutions**:

```bash
# Check broker status
cubrid broker status

# Increase max connections in cubrid_broker.conf
# MAX_NUM_APPL_SERVER=50

# Verify the database exists
cubrid server status
```

### Connection timeout

**Error**: Hangs or returns timeout error after 30 seconds.

**Solutions**:

```go
// Increase the timeout in the DSN
dsn := "cubrid://dba:@remote-host:33000/demodb?timeout=60s"

// Or for very slow networks
dsn := "cubrid://dba:@remote-host:33000/demodb?timeout=120s"
```

### dial CAS error after broker handshake

**Error**: `cubrid: [-1] dial CAS <host>:<port>: connect: connection refused`

This means the broker handshake succeeded, but the driver cannot connect to the redirected CAS port.

**Common cause**: The CAS port is on a different interface or blocked by a firewall.

**Solution**: Ensure all CAS ports (configured in `cubrid_broker.conf`) are accessible from the client.

### Database name is required

**Error**: `cubrid: database name is required in DSN`

The DSN must include a database path:

```go
// WRONG
sql.Open("cubrid", "cubrid://dba@localhost:33000")

// CORRECT
sql.Open("cubrid", "cubrid://dba@localhost:33000/demodb")
```

---

### Prepared statement fails after a broker restart

Symptoms: after `cubrid broker restart`, a CAS restart or a dropped CAS
connection, a statement from `sql.Conn.PrepareContext` or `tx.Prepare` returns
`driver: bad connection` or `sql: connection is already closed`, or
`rows.Next()` stops with `result set was opened on an earlier physical session`.

When the CAS stops answering, the driver opens a new CAS session on the same
`database/sql` connection. Prepared statement handles and server cursors belong
to the session that created them, and the new session numbers its own handles
from the start, so an old handle number can name a different statement there.
cubrid-go therefore never sends an old handle on a new session:

- `stmt.Exec` / `stmt.Query` without arguments return `driver.ErrBadConn`
  before anything is sent. For a statement from `db.Prepare`, `database/sql`
  discards the connection and prepares the statement again on another one, so
  the call succeeds without application changes.
- A statement bound to one connection (`sql.Conn`, `sql.Tx`) cannot move to
  another connection. Prepare it again on a new connection or transaction and
  retry.
- `rows.Next()` on a result set opened before the reconnect returns an
  `*OperationalError`, because rows already read cannot be retried. Run the
  query again.
- `stmt.Close()` and `rows.Close()` release nothing on the new session.

Statements executed with arguments send the full SQL text, not the handle, and
keep working.

## Query Issues

### Expected N bind args, got M

**Error**: `cubrid: expected 3 bind args, got 2`

The number of `?` placeholders doesn't match the number of arguments:

```go
// WRONG: 3 placeholders but only 2 args
db.Query("SELECT * FROM t WHERE a = ? AND b = ? AND c = ?", val1, val2)

// CORRECT
db.Query("SELECT * FROM t WHERE a = ? AND b = ? AND c = ?", val1, val2, val3)
```

### Unsupported value type

**Error**: `cubrid: unsupported value type <type>`

cubrid-go only accepts these Go types as parameters:
- `nil`, `bool`, `int64`, `float64`, `string`, `[]byte`, `time.Time`

```go
// WRONG: int32 is not supported
db.Exec("INSERT INTO t (val) VALUES (?)", int32(42))

// CORRECT: use int64
db.Exec("INSERT INTO t (val) VALUES (?)", int64(42))

// CORRECT: database/sql auto-converts standard types
db.Exec("INSERT INTO t (val) VALUES (?)", 42)  // int → int64 by database/sql
```

### LastInsertId returns 0

`LastInsertId()` only returns a meaningful value after an `INSERT` into a table with an `AUTO_INCREMENT` column.

```go
result, _ := db.Exec("INSERT INTO t (name) VALUES (?)", "test")
id, _ := result.LastInsertId()
// id = 0 if the table has no AUTO_INCREMENT column
```

> **Implementation note**: cubrid-go reads `SELECT LAST_INSERT_ID()` on the same CAS session as the INSERT. After an autocommitted statement the driver checks the CAS with `CHECK_CAS` (FC=32) and reconnects only when that check fails, so the session that generated the id is kept.

### Numeric precision loss

`NUMERIC` and `MONETARY` values are returned as `string` or `float64`. For financial calculations requiring exact precision, use `string` and parse manually:

```go
var priceStr string
db.QueryRow("SELECT CAST(price AS VARCHAR) FROM products WHERE id = ?", 1).Scan(&priceStr)
// Parse priceStr with decimal library for exact arithmetic
```

### Date/time values are returned as time.Time (UTC)

CUBRID `DATE`, `TIME`, `TIMESTAMP`, and `DATETIME` columns are returned as `time.Time`, and the driver normalizes them to UTC:

```go
var eventTime time.Time
db.QueryRow("SELECT event_date FROM events WHERE id = 1").Scan(&eventTime)
// eventTime.Location() == time.UTC
```

---

## Transaction Issues

### DDL statements auto-commit

CUBRID automatically commits any pending transaction when a DDL statement is executed:

```go
tx, _ := db.Begin()
tx.Exec("INSERT INTO t (val) VALUES (?)", 1)
tx.Exec("CREATE TABLE t2 (id INT)")  // AUTO-COMMITS everything!
tx.Rollback()                          // Too late — the INSERT is already committed
```

**Solution**: Never mix DDL and DML in the same transaction. Run DDL operations outside transactions.

### Connection left in non-autocommit mode

If a transaction is started but neither committed nor rolled back (e.g., due to a panic), the connection is returned to the pool with auto-commit disabled.

**Solution**: Always use `defer tx.Rollback()`:

```go
tx, _ := db.Begin()
defer tx.Rollback() // No-op if committed, safety net for panics

tx.Exec("INSERT INTO t (val) VALUES (?)", 1)
tx.Commit()
```

---

## GORM Issues

### ON CONFLICT / upsert silently ignored

GORM's `OnConflict` clause is silently dropped because CUBRID doesn't support it:

```go
// This is just a plain INSERT — ON CONFLICT is ignored
db.Clauses(clause.OnConflict{...}).Create(&record)
```

**Workaround**: Use raw SQL:

```go
db.Exec(`
    INSERT INTO users (email, name) VALUES (?, ?)
    ON DUPLICATE KEY UPDATE name = VALUES(name)
`, email, name)
```

### BOOLEAN field stored as SMALLINT

CUBRID (older versions) has no native `BOOLEAN` type. GORM `bool` fields become `SMALLINT`:

```go
type Setting struct {
    Enabled bool // DDL: "enabled" SMALLINT
}

// Query with 0/1 in raw SQL
db.Where("enabled = ?", 1).Find(&settings)

// GORM handles Go bool → 0/1 conversion automatically
db.Where("enabled = ?", true).Find(&settings) // also works
```

### AutoMigrate fails on existing table

If the table already exists with a different schema, AutoMigrate adds missing columns but cannot modify existing ones. For schema changes to existing columns:

```go
// Manual migration
db.Exec("ALTER TABLE users MODIFY COLUMN email VARCHAR(500)")
```

### AUTOINCREMENT vs AUTO_INCREMENT

GORM's base migrator may emit `AUTOINCREMENT` (SQLite style). The cubrid-go `FullDataTypeOf` automatically replaces it with CUBRID's `AUTO_INCREMENT`:

```go
// This is handled automatically — no action needed
// GORM: "id" INTEGER AUTOINCREMENT
// cubrid-go fixes to: "id" INTEGER AUTO_INCREMENT
```

### Empty DEFAULT clause in DDL

GORM's base migrator may add `DEFAULT ` (with trailing space) for auto-increment fields. The cubrid-go `FullDataTypeOf` strips this automatically:

```go
// GORM base: "id" INTEGER AUTO_INCREMENT DEFAULT
// cubrid-go fixes to: "id" INTEGER AUTO_INCREMENT
```

---

## Type Issues

### BLOB/CLOB handling

cubrid-go supports BLOB and CLOB as raw bytes only:

```go
// Writing
data := []byte("Hello, CUBRID!")
db.Exec("INSERT INTO docs (content) VALUES (?)", data)

// Reading
var content []byte
db.QueryRow("SELECT content FROM docs WHERE id = ?", 1).Scan(&content)
```

> **Limitation**: CUBRID's LOB locator protocol is not implemented. Large objects are transferred as raw bytes in a single round-trip.

### SET/MULTISET/SEQUENCE columns

Collection types are returned as server-formatted strings:

```go
var tags string
db.QueryRow("SELECT tags FROM products WHERE id = 1").Scan(&tags)
// tags = "{go, database, cubrid}" — parse as needed
```

### MONETARY type

MONETARY values are returned as `float64`:

```go
var price float64
db.QueryRow("SELECT price FROM products WHERE id = 1").Scan(&price)
```

### No native BOOLEAN

CUBRID maps booleans to `SMALLINT` (0/1). The driver handles `bool` → `0`/`1` conversion in parameters automatically.

---

## Performance Issues

### Slow queries with large result sets

cubrid-go fetches results in batches of 100 rows by default. For very large result sets, the round-trips may add latency.

**Optimization strategies**:

```go
// Use LIMIT to reduce result size
db.Query("SELECT * FROM large_table LIMIT 1000")

// Process rows one at a time (streaming)
rows, _ := db.Query("SELECT * FROM large_table")
defer rows.Close()
for rows.Next() {
    // Process each row immediately
}
```

### Connection pool exhaustion

Symptoms: queries hang or return `driver.ErrBadConn`.

```go
db, _ := sql.Open("cubrid", dsn)

// Set pool limits
db.SetMaxOpenConns(25)   // Prevent unlimited connections
db.SetMaxIdleConns(5)    // Keep some warm connections
db.SetConnMaxLifetime(30 * time.Minute)

// ALWAYS close rows, statements, and connections
rows, _ := db.Query("SELECT ...")
defer rows.Close()       // Don't forget this!
```

### High memory usage on large fetches

The driver buffers fetched rows in memory. For tables with millions of rows:

```go
// BAD: loads everything into memory
rows, _ := db.Query("SELECT * FROM huge_table")

// GOOD: paginate
for offset := 0; ; offset += 1000 {
    rows, _ := db.Query("SELECT * FROM huge_table LIMIT 1000 OFFSET ?", offset)
    // ... process batch
    rows.Close()
    if /* no more rows */ { break }
}
```

---

## Security Considerations

### Client-side parameter interpolation

cubrid-go interpolates query parameters **client-side** before sending to the server. This means:

1. **String escaping** is done by the driver, not the server
2. The escaping covers single quotes (`'` → `''`) and backslashes (`\` → `\\`)
3. Always use parameterized queries — never string concatenation

```go
// SAFE: uses parameterized query
db.Query("SELECT * FROM users WHERE name = ?", userInput)

// DANGEROUS: SQL injection risk!
db.Query("SELECT * FROM users WHERE name = '" + userInput + "'")
```

### SQL injection prevention

The driver's `FormatValue()` function escapes all string values. However, for maximum safety:

1. **Always use `?` placeholders** for user-controlled values
2. **Never use `fmt.Sprintf`** to build SQL with user input
3. **Validate input** at the application layer before passing to queries

---

## Docker / Development Environment

### Setting up a test CUBRID instance

```bash
# Create docker-compose.yml
cat > docker-compose.yml <<EOF
services:
  cubrid:
    image: cubrid/cubrid:11.2
    container_name: cubrid-test
    ports:
      - "33000:33000"
    environment:
      CUBRID_DB: testdb
EOF

# Start
docker compose up -d

# Wait for readiness (CUBRID takes ~10 seconds to initialize)
sleep 15

# Test connection
go run -mod=mod main.go
```

### Container won't start

**Check logs**:

```bash
docker compose logs cubrid
```

**Common issues**:

1. **Port 33000 already in use**
   ```bash
   # Find what's using the port
   lsof -i :33000

   # Use a different port
   ports:
     - "33001:33000"
   ```

2. **Not enough memory** — CUBRID needs at least 512MB
   ```yaml
   services:
     cubrid:
       deploy:
         resources:
           limits:
             memory: 1G
   ```

3. **Volume permission issues**
   ```bash
   # Remove old volumes
   docker compose down -v
   docker compose up -d
   ```
