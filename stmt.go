package cubrid

import (
	"context"
	"database/sql/driver"
	"errors"
)

var _ driver.StmtExecContext = (*stmt)(nil)
var _ driver.StmtQueryContext = (*stmt)(nil)

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
	// generation is the conn's physical-session generation at PREPARE. The
	// handle is valid only on that session; see conn.sendAndRecvHandle.
	generation uint64
	// openRows counts the rows from Query that may still FETCH with
	// queryHandle. While it is positive, Close defers CLOSE_REQ to the last
	// of those rows to close, and Exec and Query do not EXECUTE queryHandle
	// again, because the CAS would replace the cursor those rows fetch from
	// with the new result; they use PREPARE_AND_EXECUTE, which gives the new
	// execution its own handle (#86). closed, queryHandle and openRows are
	// guarded by conn.mu.
	openRows int
}

// Close releases the server-side prepared-statement handle (FC=6). A handle
// from an earlier physical session is dropped without sending CLOSE_REQ.
//
// database/sql closes a statement prepared on a Tx or a *sql.Conn at once,
// even while rows from it are open, and those rows fetch with the
// statement's handle. Releasing it then would let the CAS hand the same
// number to the next PREPARE, and the rows would fetch that statement's
// result. So while such rows are open, Close only marks the statement
// closed, and the last of them to close releases the handle, as database/sql
// does itself for a statement prepared on a *sql.DB.
func (s *stmt) Close() error {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.openRows == 0 {
		s.releaseHandleLocked()
	}
	return nil
}

// releaseHandleLocked sends CLOSE_REQ for the statement's handle, or drops a
// handle from an earlier session. The caller holds conn.mu.
func (s *stmt) releaseHandleLocked() {
	if s.queryHandle > 0 {
		s.conn.closeQueryHandle(s.queryHandle, s.generation)
		s.queryHandle = 0
	}
}

// sqlForExecLocked returns the statement's SQL with args interpolated, or
// the SQL as prepared when there are none. The caller holds conn.mu.
func (s *stmt) sqlForExecLocked(args []driver.Value) (string, error) {
	if len(args) == 0 {
		return s.query, nil
	}
	if err := s.conn.ensureStringEscapeModeLocked(); err != nil {
		return "", err
	}
	return InterpolateArgsWithMode(s.query, args, s.conn.stringEscapeMode)
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
	return s.execLocked(context.Background(), args)
}

// ExecContext implements driver.StmtExecContext. It is Exec bounded by ctx:
// canceling ctx, or reaching its deadline, while a reply is outstanding
// returns ctx's error, and the interrupted connection is retired (IsValid
// reports false), as for conn.ExecContext.
func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()

	if s.closed || s.conn.closed {
		return nil, driver.ErrBadConn
	}
	c := s.conn
	if err := c.applyContextDeadline(ctx); err != nil {
		return nil, err
	}
	defer c.endRequest()
	stopWatch := c.watchContextCancel(ctx)
	defer stopWatch()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	values, err := namedValuesToValues(args)
	if err != nil {
		return nil, err
	}
	return s.execLocked(ctx, values)
}

// execLocked runs Exec. The caller holds conn.mu. If the main request fails
// while ctx is done, it returns ctx's error; once the reply has arrived, the
// request completed and its result is returned even if ctx ended meanwhile,
// and the FC=41 handle is still closed.
func (s *stmt) execLocked(ctx context.Context, args []driver.Value) (driver.Result, error) {
	var res *ExecuteResult
	var paeRes *PrepareAndExecuteResult

	if len(args) > 0 || s.openRows > 0 {
		// Client-side interpolation: embed args into SQL, use FC=41. Without
		// args, FC=41 is used while rows still fetch with the prepared
		// handle, because executing that handle again would replace their
		// server cursor (see openRows).
		interpolatedSQL, err := s.sqlForExecLocked(args)
		if err != nil {
			return nil, err
		}
		paeRes, err = s.conn.execSQL(interpolatedSQL)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, err
		}
		// FC=41 allocates a server-side query handle; close it immediately
		// to avoid exhausting the CAS query-entry pool (default limit: 100).
		if paeRes.QueryHandle > 0 {
			s.conn.closeQueryHandle(paeRes.QueryHandle, s.conn.generation)
		}
	} else {
		// No args: use the prepared handle via FC=3.
		req := WriteExecute(s.queryHandle, s.stmtType, nil, s.conn.autoCommit, s.conn.casInfo)
		resp, err := s.conn.sendAndRecvHandle(s.generation, req)
		if errors.Is(err, errStaleHandle) {
			// The handle was prepared on an earlier physical session and
			// nothing was sent; database/sql re-prepares on a fresh conn.
			return nil, driver.ErrBadConn
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, err
		}
		res, err = ParseExecute(resp, s.columns, s.stmtType, s.conn.protoVer)
		if err != nil {
			return nil, s.conn.replyErr(err)
		}
	}

	// Retrieve last insert ID for INSERT statements.
	var lastID int64
	var lastIDErr error
	if s.stmtType == StmtInsert {
		lastID, lastIDErr = s.conn.fetchLastInsertID()
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

	return &result{lastInsertID: lastID, lastInsertIDErr: lastIDErr, rowsAffected: affected}, nil
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
	return s.queryLocked(context.Background(), args)
}

// QueryContext implements driver.StmtQueryContext. It is Query bounded by
// ctx: canceling ctx, or reaching its deadline, while the reply is
// outstanding returns ctx's error, and the interrupted connection is retired
// (IsValid reports false), as for conn.QueryContext. Fetching further rows
// from the returned rows does not watch ctx.
func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()

	if s.closed || s.conn.closed {
		return nil, driver.ErrBadConn
	}
	c := s.conn
	if err := c.applyContextDeadline(ctx); err != nil {
		return nil, err
	}
	defer c.endRequest()
	stopWatch := c.watchContextCancel(ctx)
	defer stopWatch()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	values, err := namedValuesToValues(args)
	if err != nil {
		return nil, err
	}
	return s.queryLocked(ctx, values)
}

// queryLocked runs Query. The caller holds conn.mu. If the request fails
// while ctx is done, it returns ctx's error; once the reply has arrived, the
// rows are returned even if ctx ended meanwhile.
func (s *stmt) queryLocked(ctx context.Context, args []driver.Value) (driver.Rows, error) {
	var totalCount, tupleCount int
	var fetchedRows [][]interface{}
	var columns []ColumnMetaData
	var qh int
	var gen uint64
	var ownsHandle bool

	if len(args) > 0 || s.openRows > 0 {
		// Client-side interpolation: embed args into SQL, use FC=41. Without
		// args, FC=41 gives these rows their own handle while earlier rows
		// still fetch with the prepared one (see openRows).
		interpolatedSQL, err := s.sqlForExecLocked(args)
		if err != nil {
			return nil, err
		}
		paeRes, err := s.conn.execSQL(interpolatedSQL)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, err
		}
		totalCount = paeRes.TotalTupleCount
		tupleCount = paeRes.TupleCount
		fetchedRows = paeRes.Rows
		columns = paeRes.Columns
		qh = paeRes.QueryHandle
		gen = s.conn.generation
		ownsHandle = true // FC=41 creates a new handle
	} else {
		// No args: use the prepared handle via FC=3.
		req := WriteExecute(s.queryHandle, s.stmtType, nil, s.conn.autoCommit, s.conn.casInfo)
		resp, err := s.conn.sendAndRecvHandle(s.generation, req)
		if errors.Is(err, errStaleHandle) {
			// The handle was prepared on an earlier physical session and
			// nothing was sent; database/sql re-prepares on a fresh conn.
			return nil, driver.ErrBadConn
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, err
		}
		res, err := ParseExecute(resp, s.columns, s.stmtType, s.conn.protoVer)
		if err != nil {
			return nil, s.conn.replyErr(err)
		}
		totalCount = res.TotalTupleCount
		tupleCount = res.TupleCount
		fetchedRows = res.Rows
		columns = s.columns
		qh = s.queryHandle
		gen = s.generation
		ownsHandle = false
	}

	r := &rows{
		conn:         s.conn,
		generation:   gen,
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
	if !ownsHandle && !r.exhausted {
		// The rows will FETCH with the statement's handle; keep it until
		// they no longer need it (see Close).
		r.stmt = s
		s.openRows++
	}

	return r, nil
}

// result implements driver.Result. lastInsertIDErr is set when the
// LAST_INSERT_ID() lookup after an INSERT failed; the INSERT itself ran, so
// Exec succeeds and only LastInsertId reports the error.
type result struct {
	lastInsertID    int64
	lastInsertIDErr error
	rowsAffected    int64
}

func (r *result) LastInsertId() (int64, error) { return r.lastInsertID, r.lastInsertIDErr }
func (r *result) RowsAffected() (int64, error) { return r.rowsAffected, nil }
