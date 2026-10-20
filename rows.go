package cubrid

import (
	"database/sql/driver"
	"errors"
	"io"
)

// rows implements driver.Rows and iterates over a CUBRID result set.
//
// closeHandle controls whether Close() sends FC=6 (CloseReqHandle).
// When rows are returned by stmt.Query(), closeHandle is false because
// the handle belongs to the stmt and will be closed by stmt.Close().
// When rows are the sole owner of a handle (e.g. from a one-shot execSQL),
// closeHandle should be true.
type rows struct {
	conn         *conn
	queryHandle  int
	columns      []ColumnMetaData
	stmtType     int
	totalCount   int
	fetchedCount int
	bufferedRows [][]interface{}
	bufferOffset int
	exhausted    bool
	fetchSize    int
	closed       bool
	closeHandle  bool // whether Close() should send FC=6
	// generation is the physical-session generation of queryHandle; the
	// server cursor exists only on that session.
	generation uint64
}

// Columns returns the column names.
func (r *rows) Columns() []string {
	names := make([]string, len(r.columns))
	for i, col := range r.columns {
		names[i] = col.Name
	}
	return names
}

// Close marks the rows as exhausted.
// FC=6 (CloseReqHandle) is only sent when closeHandle is true — that is,
// when this rows value is the sole owner of the server-side handle.
// Normally the handle is owned by the parent stmt and closed by stmt.Close().
func (r *rows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.closeHandle && r.queryHandle > 0 {
		r.conn.closeQueryHandle(r.queryHandle, r.generation)
		r.queryHandle = 0
	}
	return nil
}

// Next advances to the next row, fetching more from the server when needed.
func (r *rows) Next(dest []driver.Value) error {
	if r.closed {
		return io.EOF
	}

	// Serve from the local buffer first.
	if r.bufferOffset < len(r.bufferedRows) {
		return r.fillDest(dest, r.bufferedRows[r.bufferOffset])
	}

	// Buffer exhausted; fetch more rows from the server if available.
	if r.exhausted {
		return io.EOF
	}

	fetchReq := WriteFetch(r.queryHandle, r.fetchedCount, r.fetchSize, r.conn.casInfo)
	resp, err := r.conn.sendAndRecvHandle(r.generation, fetchReq)
	if errors.Is(err, errStaleHandle) {
		// The connection reconnected since this result set was opened, so
		// its server cursor is gone. Nothing was sent; the rows already
		// returned cannot be retried, so report it instead of ErrBadConn.
		return &OperationalError{CubridError{Code: -1, Message: "result set was " +
			"opened on an earlier physical session; its server cursor no longer exists"}}
	}
	if err != nil {
		return err
	}
	res, err := ParseFetch(resp, r.columns, r.stmtType)
	if err != nil {
		return err
	}

	if res.TupleCount == 0 {
		r.exhausted = true
		return io.EOF
	}

	r.bufferedRows = res.Rows
	r.bufferOffset = 0
	r.fetchedCount += res.TupleCount

	if r.fetchedCount >= r.totalCount {
		r.exhausted = true
	}

	return r.fillDest(dest, r.bufferedRows[r.bufferOffset])
}

// fillDest copies one server row into the dest slice and advances the offset.
func (r *rows) fillDest(dest []driver.Value, row []interface{}) error {
	r.bufferOffset++
	for i, v := range row {
		if i >= len(dest) {
			break
		}
		if v == nil {
			dest[i] = nil
			continue
		}
		// driver.Value accepts: nil, int64, float64, bool, []byte, string, time.Time
		switch val := v.(type) {
		case int64:
			dest[i] = val
		case float64:
			dest[i] = val
		case bool:
			dest[i] = val
		case string:
			dest[i] = val
		case []byte:
			dest[i] = val
		default:
			// Use fmt to produce a string representation for unsupported types.
			dest[i] = v
		}
	}
	return nil
}

// ColumnTypeDatabaseTypeName returns the CUBRID type name for column i.
func (r *rows) ColumnTypeDatabaseTypeName(i int) string {
	if i < 0 || i >= len(r.columns) {
		return ""
	}
	switch r.columns[i].ColumnType {
	case TypeChar:
		return "CHAR"
	case TypeString:
		return "VARCHAR"
	case TypeNChar:
		return "NCHAR"
	case TypeVarNChar:
		return "NVARCHAR"
	case TypeShort:
		return "SMALLINT"
	case TypeInt:
		return "INTEGER"
	case TypeBigInt:
		return "BIGINT"
	case TypeFloat:
		return "FLOAT"
	case TypeDouble:
		return "DOUBLE"
	case TypeMonetary:
		return "MONETARY"
	case TypeNumeric:
		return "NUMERIC"
	case TypeDate:
		return "DATE"
	case TypeTime:
		return "TIME"
	case TypeTimestamp:
		return "TIMESTAMP"
	case TypeDatetime:
		return "DATETIME"
	case TypeBit:
		return "BIT"
	case TypeVarBit:
		return "BIT VARYING"
	case TypeBlob:
		return "BLOB"
	case TypeClob:
		return "CLOB"
	case TypeEnum:
		return "ENUM"
	case TypeNull:
		return "NULL"
	default:
		return "UNKNOWN"
	}
}
