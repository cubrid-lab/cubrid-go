package cubrid

import (
	"database/sql/driver"
)

// stmt is a server-side prepared statement obtained via conn.Prepare().
// It holds the query handle returned by FC=2 (PREPARE) and reuses it
// across multiple Exec / Query calls via FC=3 (EXECUTE).
type stmt struct {
	conn        *conn
	query       string // original SQL (kept for Explain / debugging)
	queryHandle int    // server-side handle from FC=2
	stmtType    int    // statement type (SELECT, INSERT, …)
	bindCount   int    // number of ? placeholders
	columns     []ColumnMetaData
	closed      bool
}

// Close releases the server-side prepared-statement handle (FC=6).
func (s *stmt) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.queryHandle > 0 {
		s.conn.mu.Lock()
		defer s.conn.mu.Unlock()
		s.conn.closeQueryHandle(s.queryHandle)
		s.queryHandle = 0
	}
	return nil
}

// NumInput returns the number of ? placeholders the server found during Prepare.
// database/sql uses this to validate argument counts before calling Exec/Query.
func (s *stmt) NumInput() int { return s.bindCount }

// Exec executes a non-SELECT (or any) statement with the given arguments.
// When args are present, parameters are interpolated into the SQL and the
// statement is re-executed via PrepareAndExecute (FC=41), matching pycubrid's
// client-side interpolation approach. CUBRID's CAS protocol does not reliably
// support server-side bind parameters for all drivers.
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()

	if s.closed || s.conn.closed {
		return nil, driver.ErrBadConn
	}

	var res *ExecuteResult
	var paeRes *PrepareAndExecuteResult

	if len(args) > 0 {
		// Client-side interpolation: embed args into SQL, use FC=41.
		if err := s.conn.ensureStringEscapeModeLocked(); err != nil {
			return nil, err
		}
		interpolatedSQL, err := InterpolateArgsWithMode(s.query, args, s.conn.stringEscapeMode)
		if err != nil {
			return nil, err
		}
		paeRes, err = s.conn.execSQL(interpolatedSQL)
		if err != nil {
			return nil, err
		}
		// FC=41 allocates a server-side query handle; close it immediately
		// to avoid exhausting the CAS query-entry pool (default limit: 100).
		if paeRes.QueryHandle > 0 {
			s.conn.closeQueryHandle(paeRes.QueryHandle)
		}
	} else {
		// No args: use the prepared handle via FC=3.
		req := WriteExecute(s.queryHandle, s.stmtType, nil, s.conn.autoCommit, s.conn.casInfo)
		resp, err := s.conn.sendAndRecv(req)
		if err != nil {
			return nil, err
		}
		res, err = ParseExecute(resp, s.columns, s.stmtType, s.conn.protoVer)
		if err != nil {
			return nil, err
		}
	}

	// Retrieve last insert ID for INSERT statements.
	var lastID int64
	if s.stmtType == StmtInsert {
		lastID = s.conn.fetchLastInsertID()
	}

	var affected int64
	if paeRes != nil {
		for _, info := range paeRes.ResultInfos {
			affected += int64(info.ResultCount)
		}
	} else if res != nil {
		for _, info := range res.ResultInfos {
			affected += int64(info.ResultCount)
		}
	}

	return &result{lastInsertID: lastID, rowsAffected: affected}, nil
}

// Query executes the statement and returns the result rows.
// When args are present, parameters are interpolated into the SQL and the
// statement is re-executed via PrepareAndExecute (FC=41), matching pycubrid's
// client-side interpolation approach.
func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()

	if s.closed || s.conn.closed {
		return nil, driver.ErrBadConn
	}

	var totalCount, tupleCount int
	var fetchedRows [][]interface{}
	var columns []ColumnMetaData
	var qh int
	var ownsHandle bool

	if len(args) > 0 {
		// Client-side interpolation: embed args into SQL, use FC=41.
		if err := s.conn.ensureStringEscapeModeLocked(); err != nil {
			return nil, err
		}
		interpolatedSQL, err := InterpolateArgsWithMode(s.query, args, s.conn.stringEscapeMode)
		if err != nil {
			return nil, err
		}
		paeRes, err := s.conn.execSQL(interpolatedSQL)
		if err != nil {
			return nil, err
		}
		totalCount = paeRes.TotalTupleCount
		tupleCount = paeRes.TupleCount
		fetchedRows = paeRes.Rows
		columns = paeRes.Columns
		qh = paeRes.QueryHandle
		ownsHandle = true // FC=41 creates a new handle
	} else {
		// No args: use the prepared handle via FC=3.
		req := WriteExecute(s.queryHandle, s.stmtType, nil, s.conn.autoCommit, s.conn.casInfo)
		resp, err := s.conn.sendAndRecv(req)
		if err != nil {
			return nil, err
		}
		res, err := ParseExecute(resp, s.columns, s.stmtType, s.conn.protoVer)
		if err != nil {
			return nil, err
		}
		totalCount = res.TotalTupleCount
		tupleCount = res.TupleCount
		fetchedRows = res.Rows
		columns = s.columns
		qh = s.queryHandle
		ownsHandle = false
	}

	r := &rows{
		conn:         s.conn,
		queryHandle:  qh,
		columns:      columns,
		stmtType:     s.stmtType,
		totalCount:   totalCount,
		fetchedCount: len(fetchedRows),
		bufferedRows: fetchedRows,
		bufferOffset: 0,
		exhausted:    tupleCount == 0 || len(fetchedRows) >= totalCount,
		fetchSize:    defaultFetchSize,
		closeHandle:  ownsHandle,
	}

	return r, nil
}

// result implements driver.Result.
type result struct {
	lastInsertID int64
	rowsAffected int64
}

func (r *result) LastInsertId() (int64, error) { return r.lastInsertID, nil }
func (r *result) RowsAffected() (int64, error) { return r.rowsAffected, nil }
