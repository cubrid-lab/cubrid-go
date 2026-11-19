# GORM Integration Guide

Complete guide for using CUBRID with [GORM](https://gorm.io), Go's most popular ORM.

## Table of Contents

- [Installation](#installation)
- [Quick Start](#quick-start)
- [Connection](#connection)
  - [From DSN String](#from-dsn-string)
  - [From Existing *sql.DB](#from-existing-sqldb)
- [Model Definition](#model-definition)
  - [Basic Model](#basic-model)
  - [Custom Table Name](#custom-table-name)
  - [Column Tags](#column-tags)
  - [CUBRID Type Mapping](#cubrid-type-mapping)
- [AutoMigrate](#automigrate)
- [CRUD Operations](#crud-operations)
  - [Create](#create)
  - [Read](#read)
  - [Update](#update)
  - [Delete](#delete)
- [Transactions](#transactions)
- [Querying](#querying)
  - [Where Conditions](#where-conditions)
  - [Order, Limit, Offset](#order-limit-offset)
  - [Joins](#joins)
  - [Raw SQL](#raw-sql)
  - [Aggregations](#aggregations)
- [Schema Inspection](#schema-inspection)
- [CUBRID-Specific Notes](#cubrid-specific-notes)
- [Complete Example](#complete-example)

---

## Installation

```bash
go get github.com/cubrid-lab/cubrid-go
go get gorm.io/gorm
```

---

## Quick Start

```go
package main

import (
    "fmt"
    "log"

    "gorm.io/gorm"
    cubrid "github.com/cubrid-lab/cubrid-go/dialector"
)

type Product struct {
    ID    int     `gorm:"primaryKey;autoIncrement"`
    Name  string  `gorm:"size:100;not null"`
    Price float64 `gorm:"type:DOUBLE;default:0"`
}

func main() {
    db, err := gorm.Open(cubrid.Open("cubrid://dba:@localhost:33000/demodb"), &gorm.Config{})
    if err != nil {
        log.Fatal(err)
    }

    // Create table
    db.AutoMigrate(&Product{})

    // Insert
    db.Create(&Product{Name: "Go Book", Price: 29.99})

    // Query
    var product Product
    db.First(&product, "name = ?", "Go Book")
    fmt.Printf("Found: %s ($%.2f)\n", product.Name, product.Price)
}
```

---

## Connection

### From DSN String

The standard approach — pass a CUBRID DSN to `cubrid.Open()`:

```go
import (
    "gorm.io/gorm"
    cubrid "github.com/cubrid-lab/cubrid-go/dialector"
)

db, err := gorm.Open(cubrid.Open("cubrid://dba:@localhost:33000/demodb"), &gorm.Config{})
```

### From Existing *sql.DB

If you already have a configured `*sql.DB` (e.g., with custom pool settings):

```go
import (
    "database/sql"
    "gorm.io/gorm"
    cubrid "github.com/cubrid-lab/cubrid-go/dialector"
    _ "github.com/cubrid-lab/cubrid-go"
)

sqlDB, _ := sql.Open("cubrid", "cubrid://dba:@localhost:33000/demodb")
sqlDB.SetMaxOpenConns(25)
sqlDB.SetMaxIdleConns(5)

db, err := gorm.Open(cubrid.OpenDB(sqlDB), &gorm.Config{})
```

---

## Model Definition

### Basic Model

Define Go structs with GORM tags to control column types and constraints:

```go
type User struct {
    ID        int       `gorm:"primaryKey;autoIncrement"`
    Username  string    `gorm:"size:50;not null;uniqueIndex"`
    Email     string    `gorm:"size:255;not null"`
    Age       int       `gorm:"type:SMALLINT"`
    Balance   float64   `gorm:"type:DOUBLE;default:0"`
    IsActive  bool      `gorm:"default:true"`     // Stored as SMALLINT (0/1)
    Bio       string    `gorm:"type:STRING"`       // CUBRID STRING type
    Avatar    []byte    `gorm:"type:BLOB"`
    CreatedAt time.Time `gorm:"autoCreateTime"`
    UpdatedAt time.Time `gorm:"autoUpdateTime"`
}
```

### Custom Table Name

```go
type User struct {
    // ...
}

func (User) TableName() string {
    return "app_users"
}
```

### Column Tags

| Tag | Effect | CUBRID DDL |
|:----|:-------|:-----------|
| `primaryKey` | Marks as primary key | `PRIMARY KEY` |
| `autoIncrement` | Auto-generated ID | `AUTO_INCREMENT` |
| `size:100` | Sets column size | `VARCHAR(100)` |
| `not null` | Disallow NULL | `NOT NULL` |
| `default:0` | Set default value | `DEFAULT 0` |
| `type:DOUBLE` | Force specific SQL type | `DOUBLE` |
| `uniqueIndex` | Create unique index | `CREATE UNIQUE INDEX` |
| `index` | Create index | `CREATE INDEX` |
| `column:col_name` | Custom column name | Uses `col_name` in DDL |

### CUBRID Type Mapping

The dialector maps GORM schema types to CUBRID SQL types:

| GORM DataType | CUBRID SQL Type | Notes |
|:--------------|:----------------|:------|
| `Bool` | `SMALLINT` | 0 = false, 1 = true |
| `Int` (size ≤ 16) | `SMALLINT` | |
| `Int` (size ≤ 32) | `INTEGER` | |
| `Int` (size > 32) | `BIGINT` | |
| `Uint` (size ≤ 8) | `SMALLINT` | |
| `Uint` (size ≤ 16) | `INTEGER` | |
| `Uint` (size ≤ 32) | `BIGINT` | |
| `Uint` (size > 32) | `BIGINT` | |
| `Float` (size ≤ 32) | `FLOAT` | |
| `Float` (size > 32) | `DOUBLE` | |
| `String` (size < 1073741823) | `VARCHAR(n)` | Default size: 256 |
| `String` (size ≥ 1073741823) | `STRING` | CUBRID's unlimited string |
| `Time` | `DATETIME` | Optional precision: `DATETIME(n)` |
| `Bytes` | `BLOB` | |
| `AutoIncrement` (size ≤ 32) | `INTEGER AUTO_INCREMENT` | |
| `AutoIncrement` (size > 32) | `BIGINT AUTO_INCREMENT` | |

**Custom types**: Use the `type:` tag to force any CUBRID-specific type:

```go
type Event struct {
    ID        int     `gorm:"primaryKey;autoIncrement"`
    Price     float64 `gorm:"type:MONETARY"`
    EventDate string  `gorm:"type:DATE"`
    EventTime string  `gorm:"type:TIME"`
    Tags      string  `gorm:"type:SET(VARCHAR(50))"`
}
```

---

## AutoMigrate

AutoMigrate creates tables that don't exist and adds missing columns to existing tables:

```go
// Single model
db.AutoMigrate(&User{})

// Multiple models
db.AutoMigrate(&User{}, &Product{}, &Order{})
```

**What AutoMigrate does:**
- Creates tables if they don't exist
- Adds missing columns
- Creates indexes defined in struct tags

**What AutoMigrate does NOT do:**
- Delete columns
- Change column types
- Drop tables

> **Note**: CUBRID DDL statements auto-commit. AutoMigrate cannot be rolled back.

---

## CRUD Operations

### Create

```go
// Insert a single record
user := User{Username: "alice", Email: "alice@example.com", Age: 28}
result := db.Create(&user)
fmt.Println(user.ID)           // Auto-generated ID is populated
fmt.Println(result.RowsAffected) // 1

// Insert multiple records
users := []User{
    {Username: "bob", Email: "bob@example.com", Age: 32},
    {Username: "carol", Email: "carol@example.com", Age: 25},
}
db.Create(&users)
```

### Read

```go
// Find by primary key
var user User
db.First(&user, 1) // SELECT * FROM users WHERE id = 1

// Find by condition
db.First(&user, "username = ?", "alice")

// Find all
var users []User
db.Find(&users)

// Find with conditions
db.Where("age > ?", 25).Find(&users)

// Select specific columns
db.Select("username", "email").Find(&users)
```

### Update

```go
// Update a single field
db.Model(&user).Update("age", 29)

// Update multiple fields
db.Model(&user).Updates(User{Age: 30, Email: "newalice@example.com"})

// Update with map
db.Model(&user).Updates(map[string]interface{}{
    "age":   30,
    "email": "new@example.com",
})

// Batch update
db.Model(&User{}).Where("age < ?", 20).Update("is_active", false)
```

### Delete

```go
// Delete by primary key
db.Delete(&User{}, 1)

// Delete by condition
db.Where("username = ?", "bob").Delete(&User{})

// Delete with inline condition
db.Delete(&User{}, "age < ?", 18)
```

---

## Transactions

```go
// Automatic transaction (recommended)
err := db.Transaction(func(tx *gorm.DB) error {
    if err := tx.Create(&User{Username: "dave", Email: "dave@example.com"}).Error; err != nil {
        return err // Rollback on error
    }
    if err := tx.Create(&Order{UserID: 1, Total: 59.99}).Error; err != nil {
        return err // Rollback on error
    }
    return nil // Commit
})

// Manual transaction
tx := db.Begin()
if err := tx.Create(&User{Username: "eve"}).Error; err != nil {
    tx.Rollback()
    log.Fatal(err)
}
tx.Commit()
```

---

## Querying

### Where Conditions

```go
// String condition
db.Where("name = ?", "alice").Find(&users)

// Multiple conditions
db.Where("age > ? AND is_active = ?", 25, 1).Find(&users)

// IN
db.Where("nation_code IN ?", []string{"KOR", "USA", "JPN"}).Find(&athletes)

// LIKE
db.Where("name LIKE ?", "%kim%").Find(&users)

// BETWEEN
db.Where("age BETWEEN ? AND ?", 20, 30).Find(&users)

// NOT
db.Not("nation_code = ?", "KOR").Find(&athletes)

// OR
db.Where("nation_code = ?", "KOR").Or("nation_code = ?", "USA").Find(&athletes)
```

### Order, Limit, Offset

```go
// Order
db.Order("created_at DESC").Find(&users)

// Multiple ordering
db.Order("age DESC, name ASC").Find(&users)

// Limit and offset (pagination)
db.Offset(20).Limit(10).Find(&users) // Page 3, 10 per page

// Distinct
db.Distinct("nation_code").Find(&athletes)
```

### Joins

```go
type OrderWithUser struct {
    OrderID  int
    Total    float64
    Username string
}

var results []OrderWithUser
db.Table("orders").
    Select("orders.id as order_id, orders.total, users.username").
    Joins("JOIN users ON users.id = orders.user_id").
    Where("orders.total > ?", 100).
    Scan(&results)
```

### Raw SQL

```go
// Raw query
var users []User
db.Raw("SELECT * FROM users WHERE age > ?", 25).Scan(&users)

// Raw exec
db.Exec("UPDATE products SET price = price * 1.1 WHERE category = ?", "electronics")
```

### Aggregations

```go
// Count
var count int64
db.Model(&User{}).Where("is_active = ?", 1).Count(&count)

// Sum, Avg, Min, Max — use Raw SQL
var avgAge float64
db.Raw("SELECT AVG(age) FROM users WHERE is_active = 1").Scan(&avgAge)

// Group By
type NationCount struct {
    NationCode string
    Total      int
}
var stats []NationCount
db.Model(&Athlete{}).
    Select("nation_code, COUNT(*) as total").
    Group("nation_code").
    Having("COUNT(*) > ?", 10).
    Scan(&stats)
```

---

## Schema Inspection

The CUBRID GORM migrator provides schema introspection methods:

### HasTable

```go
migrator := db.Migrator()

if migrator.HasTable(&User{}) {
    fmt.Println("users table exists")
}

// Or by string name
if migrator.HasTable("products") {
    fmt.Println("products table exists")
}
```

> **Implementation**: Queries `db_class` where `class_name = ? AND is_system_class = 'NO'`

### HasColumn

```go
if migrator.HasColumn(&User{}, "email") {
    fmt.Println("email column exists")
}
```

> **Implementation**: Queries `db_attribute` where `class_name = ? AND attr_name = ?`

### HasIndex

```go
if migrator.HasIndex(&User{}, "idx_users_email") {
    fmt.Println("index exists")
}
```

> **Implementation**: Queries `db_index` where `class_name = ? AND index_name = ?`

### CurrentDatabase

```go
name := migrator.CurrentDatabase()
fmt.Println("Current database:", name)
```

> **Implementation**: Executes `SELECT DATABASE()`

---

## CUBRID-Specific Notes

### No BOOLEAN Type

CUBRID (older versions) has no native `BOOLEAN` type. The dialector maps Go `bool` to `SMALLINT`:

```go
type Setting struct {
    ID      int  `gorm:"primaryKey;autoIncrement"`
    Enabled bool `gorm:"default:true"` // Stored as SMALLINT: 0 or 1
}

// When querying, use 0/1 instead of true/false in raw SQL:
db.Where("enabled = ?", 1).Find(&settings)
```

### No ON CONFLICT / UPSERT

CUBRID does not support `ON CONFLICT` (PostgreSQL) or `ON DUPLICATE KEY UPDATE` (MySQL) syntax via GORM's standard API. The clause builder silently ignores `ON CONFLICT`:

```go
// This will NOT perform an upsert — the ON CONFLICT clause is silently dropped.
// It behaves as a plain INSERT and will fail on duplicate keys.
db.Clauses(clause.OnConflict{
    Columns:   []clause.Column{{Name: "email"}},
    DoUpdates: clause.AssignmentColumns([]string{"name"}),
}).Create(&user)
```

**Workaround**: Use raw SQL with CUBRID's `INSERT ... ON DUPLICATE KEY UPDATE`:

```go
db.Exec(`
    INSERT INTO users (email, name) VALUES (?, ?)
    ON DUPLICATE KEY UPDATE name = ?
`, email, name, name)
```

### DDL Auto-Commits

All DDL operations (CREATE TABLE, ALTER TABLE, DROP TABLE) are automatically committed by CUBRID. They cannot be rolled back:

```go
// WARNING: This CREATE TABLE is permanent even if the transaction rolls back
tx := db.Begin()
tx.AutoMigrate(&NewTable{}) // Committed immediately by CUBRID
tx.Rollback()               // Does NOT undo the CREATE TABLE
```

### Identifier Quoting

The dialector uses double-quotes (`"`) for identifier quoting:

```go
// Generates: SELECT "name", "email" FROM "users"
db.Select("name", "email").Find(&users)
```

### AUTO_INCREMENT Handling

The dialector handles AUTO_INCREMENT specially in DDL:

1. `DataTypeOf()` appends `AUTO_INCREMENT` directly to the type: `INTEGER AUTO_INCREMENT`
2. `DefaultValueOf()` returns empty expression for auto-increment fields (prevents `DEFAULT` clause)
3. `FullDataTypeOf()` strips any trailing ` DEFAULT ` that GORM's base migrator may add

This ensures clean DDL like:
```sql
CREATE TABLE users (
    "id" INTEGER AUTO_INCREMENT,
    ...
)
```

---

## Complete Example

A full working example demonstrating models, migrations, CRUD, transactions, and queries:

```go
package main

import (
    "fmt"
    "log"
    "time"

    "gorm.io/gorm"
    cubrid "github.com/cubrid-lab/cubrid-go/dialector"
)

// ─── Models ─────────────────────────────────────────

type Author struct {
    ID        int       `gorm:"primaryKey;autoIncrement"`
    Name      string    `gorm:"size:100;not null"`
    Email     string    `gorm:"size:255;uniqueIndex"`
    CreatedAt time.Time `gorm:"autoCreateTime"`
}

type Book struct {
    ID          int     `gorm:"primaryKey;autoIncrement"`
    Title       string  `gorm:"size:200;not null"`
    AuthorID    int     `gorm:"not null;index"`
    Price       float64 `gorm:"type:DOUBLE;default:0"`
    PublishYear int     `gorm:"type:INTEGER"`
    InStock     bool    `gorm:"default:true"`
}

func main() {
    // Connect
    dsn := "cubrid://dba:@localhost:33000/demodb"
    db, err := gorm.Open(cubrid.Open(dsn), &gorm.Config{})
    if err != nil {
        log.Fatal("connection failed:", err)
    }

    // Migrate
    if err := db.AutoMigrate(&Author{}, &Book{}); err != nil {
        log.Fatal("migration failed:", err)
    }
    fmt.Println("Tables created successfully")

    // Create author + books in a transaction
    err = db.Transaction(func(tx *gorm.DB) error {
        author := Author{Name: "Brian Kernighan", Email: "bwk@example.com"}
        if err := tx.Create(&author).Error; err != nil {
            return err
        }

        books := []Book{
            {Title: "The C Programming Language", AuthorID: author.ID, Price: 49.99, PublishYear: 1978},
            {Title: "The Practice of Programming", AuthorID: author.ID, Price: 39.99, PublishYear: 1999},
            {Title: "The Go Programming Language", AuthorID: author.ID, Price: 44.99, PublishYear: 2015},
        }
        if err := tx.Create(&books).Error; err != nil {
            return err
        }

        fmt.Printf("Created author (ID=%d) with %d books\n", author.ID, len(books))
        return nil
    })
    if err != nil {
        log.Fatal("transaction failed:", err)
    }

    // Query: find books by author
    var books []Book
    db.Where("author_id = ?", 1).Order("publish_year ASC").Find(&books)
    for _, b := range books {
        fmt.Printf("  %d: %s ($%.2f)\n", b.PublishYear, b.Title, b.Price)
    }

    // Aggregate: average price
    var avgPrice float64
    db.Raw("SELECT AVG(price) FROM book").Scan(&avgPrice)
    fmt.Printf("Average book price: $%.2f\n", avgPrice)

    // Update price
    db.Model(&Book{}).Where("publish_year < ?", 2000).Update("price", 19.99)

    // Count
    var count int64
    db.Model(&Book{}).Where("in_stock = ?", 1).Count(&count)
    fmt.Printf("Books in stock: %d\n", count)

    // Cleanup
    db.Exec("DROP TABLE IF EXISTS book")
    db.Exec("DROP TABLE IF EXISTS author")
}
```

**Expected output**:
```
Tables created successfully
Created author (ID=1) with 3 books
  1978: The C Programming Language ($49.99)
  1999: The Practice of Programming ($39.99)
  2015: The Go Programming Language ($44.99)
Average book price: $44.99
Books in stock: 3
```
