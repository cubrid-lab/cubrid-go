// Package dialector provides a GORM dialector for CUBRID databases.
//
// Usage:
//
//	import (
//	    "gorm.io/gorm"
//	    cubrid "github.com/cubrid-labs/cubrid-go/dialector"
//	)
//
//	db, err := gorm.Open(cubrid.Open("cubrid://dba:@localhost:33000/demodb"), &gorm.Config{})
package dialector

import (
	"database/sql"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"

	// Register the cubrid driver with database/sql.
	_ "github.com/cubrid-labs/cubrid-go"
)

// Dialector is the GORM dialector for CUBRID.
type Dialector struct {
	DSN  string
	conn gorm.ConnPool
}

// Open creates a new CUBRID dialector from a DSN string.
func Open(dsn string) gorm.Dialector {
	return &Dialector{DSN: dsn}
}

// OpenDB creates a new CUBRID dialector from an existing *sql.DB.
func OpenDB(db *sql.DB) gorm.Dialector {
	return &Dialector{conn: db}
}

// Name returns the dialector name used in GORM.
func (d *Dialector) Name() string { return "cubrid" }

// Initialize configures the GORM DB instance for CUBRID.
func (d *Dialector) Initialize(db *gorm.DB) error {
	if d.conn != nil {
		db.ConnPool = d.conn
	} else {
		sqlDB, err := sql.Open("cubrid", d.DSN)
		if err != nil {
			return err
		}
		db.ConnPool = sqlDB
	}

	// Register callbacks shared with MySQL-like databases.
	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{
		LastInsertIDReversed: false,
	})

	for k, v := range d.ClauseBuilders() {
		db.ClauseBuilders[k] = v
	}

	return nil
}

// ClauseBuilders returns CUBRID-specific clause builders.
func (d *Dialector) ClauseBuilders() map[string]clause.ClauseBuilder {
	return map[string]clause.ClauseBuilder{
		"ON CONFLICT": func(c clause.Clause, builder clause.Builder) {
			// CUBRID does not support ON CONFLICT; silently ignore.
		},
		"INSERT": func(c clause.Clause, builder clause.Builder) {
			c.Build(builder)
		},
	}
}

// DefaultValueOf returns the CUBRID expression for a field's default value.
func (d *Dialector) DefaultValueOf(field *schema.Field) clause.Expression {
	if field.AutoIncrement {
		// AUTO_INCREMENT is already appended in DataTypeOf; no DEFAULT needed.
		return clause.Expr{SQL: ""}
	}
	return clause.Expr{SQL: "DEFAULT"}
}

// BindVarTo writes a `?` placeholder.
func (d *Dialector) BindVarTo(writer clause.Writer, stmt *gorm.Statement, v interface{}) {
	_ = writer.WriteByte('?')
}

// QuoteTo writes a CUBRID-quoted identifier using double-quotes.
func (d *Dialector) QuoteTo(writer clause.Writer, str string) {
	_ = writer.WriteByte('"')
	if strings.Contains(str, `"`) {
		// Escape embedded double-quotes.
		parts := strings.Split(str, `"`)
		for i, p := range parts {
			if i > 0 {
				_, _ = writer.WriteString(`""`)
			}
			_, _ = writer.WriteString(p)
		}
	} else {
		_, _ = writer.WriteString(str)
	}
	_ = writer.WriteByte('"')
}

// Explain returns the SQL with arguments interpolated (for logging/debugging).
func (d *Dialector) Explain(sql string, vars ...interface{}) string {
	return logger.ExplainSQL(sql, nil, `'`, vars...)
}

// DataTypeOf maps a GORM schema field to a CUBRID SQL data type.
func (d *Dialector) DataTypeOf(field *schema.Field) string {
	// AUTO_INCREMENT fields must use INTEGER or BIGINT.
	// We append AUTO_INCREMENT directly so FullDataTypeOf includes it in the DDL.
	if field.AutoIncrement {
		if field.Size > 32 {
			return "BIGINT AUTO_INCREMENT"
		}
		return "INTEGER AUTO_INCREMENT"
	}

	switch field.DataType {
	case schema.Bool:
		return "SMALLINT" // CUBRID has no native BOOLEAN in older versions
	case schema.Int:
		switch {
		case field.Size <= 16:
			return "SMALLINT"
		case field.Size <= 32:
			return "INTEGER"
		default:
			return "BIGINT"
		}
	case schema.Uint:
		switch {
		case field.Size <= 8:
			return "SMALLINT"
		case field.Size <= 16:
			return "INTEGER"
		case field.Size <= 32:
			return "BIGINT"
		default:
			return "BIGINT"
		}
	case schema.Float:
		if field.Size <= 32 {
			return "FLOAT"
		}
		return "DOUBLE"
	case schema.String:
		size := field.Size
		if size == 0 {
			size = 256
		}
		if size >= 1073741823 {
			return "STRING"
		}
		return fmt.Sprintf("VARCHAR(%d)", size)
	case schema.Time:
		if field.Precision > 0 {
			return fmt.Sprintf("DATETIME(%d)", field.Precision)
		}
		return "DATETIME"
	case schema.Bytes:
		return "BLOB"
	default:
		return d.dataTypeFromTag(field)
	}
}

// dataTypeFromTag reads the `type` struct tag or falls back to VARCHAR(256).
func (d *Dialector) dataTypeFromTag(field *schema.Field) string {
	if field.HasDefaultValue && field.DefaultValue != "" {
		return field.DefaultValue
	}
	return "VARCHAR(256)"
}

// Migrator returns a CUBRID-specific migrator.
func (d *Dialector) Migrator(db *gorm.DB) gorm.Migrator {
	return &Migrator{
		Migrator: migrator.Migrator{Config: migrator.Config{
			DB:                          db,
			Dialector:                   d,
			CreateIndexAfterCreateTable: true,
		}},
	}
}

// Migrator wraps the generic GORM migrator with CUBRID-specific overrides.
type Migrator struct {
	migrator.Migrator
}

// AutoMigrate creates or alters tables to match the provided model schemas.
func (m *Migrator) AutoMigrate(dst ...interface{}) error {
	return m.Migrator.AutoMigrate(dst...)
}

// CurrentDatabase returns the name of the current database.
func (m *Migrator) CurrentDatabase() (name string) {
	_ = m.DB.Raw("SELECT DATABASE()").Row().Scan(&name)
	return
}

// CreateTable creates a table for each provided model.
func (m *Migrator) CreateTable(dst ...interface{}) error {
	for _, d := range dst {
		if err := m.Migrator.CreateTable(d); err != nil {
			return err
		}
	}
	return nil
}

// HasTable returns true if the named table exists.
func (m *Migrator) HasTable(dst interface{}) bool {
	var count int64
	var tableName string
	if name, ok := dst.(string); ok {
		tableName = name
	} else {
		stmt := &gorm.Statement{DB: m.DB}
		if err := stmt.Parse(dst); err == nil {
			tableName = stmt.Table
		} else {
			tableName = fmt.Sprintf("%v", dst)
		}
	}
	m.DB.Raw(
		"SELECT COUNT(*) FROM db_class WHERE class_name = ? AND is_system_class = 'NO'",
		tableName,
	).Scan(&count)
	return count > 0
}

// HasColumn returns true if the named column exists on the table.
func (m *Migrator) HasColumn(dst interface{}, columnName string) bool {
	var count int64
	var tableName string
	if name, ok := dst.(string); ok {
		tableName = name
	} else {
		stmt := &gorm.Statement{DB: m.DB}
		if err := stmt.Parse(dst); err == nil {
			tableName = stmt.Table
		} else {
			tableName = fmt.Sprintf("%v", dst)
		}
	}
	m.DB.Raw(
		`SELECT COUNT(*) FROM db_attribute WHERE class_name = ? AND attr_name = ?`,
		tableName, columnName,
	).Scan(&count)
	return count > 0
}

// HasIndex returns true if the named index exists on the table.
func (m *Migrator) HasIndex(dst interface{}, indexName string) bool {
	var count int64
	var tableName string
	if name, ok := dst.(string); ok {
		tableName = name
	} else {
		stmt := &gorm.Statement{DB: m.DB}
		if err := stmt.Parse(dst); err == nil {
			tableName = stmt.Table
		} else {
			tableName = fmt.Sprintf("%v", dst)
		}
	}
	m.DB.Raw(
		`SELECT COUNT(*) FROM db_index WHERE class_name = ? AND index_name = ?`,
		tableName, indexName,
	).Scan(&count)
	return count > 0
}

// FullDataTypeOf returns the full column definition including constraints.
func (m *Migrator) FullDataTypeOf(field *schema.Field) clause.Expr {
	expr := m.Migrator.FullDataTypeOf(field)

	// CUBRID: replace AUTOINCREMENT keyword with AUTO_INCREMENT.
	sql := strings.ReplaceAll(expr.SQL, "AUTOINCREMENT", "AUTO_INCREMENT")
	// Remove empty DEFAULT clause that GORM base may add for auto-increment fields.
	sql = strings.TrimSuffix(sql, " DEFAULT ")
	expr.SQL = sql
	return expr
}
