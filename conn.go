package cubrid

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const defaultFetchSize = 100

// conn is a single TCP connection to the CUBRID CAS broker.
// It implements driver.Conn, driver.Pinger, and driver.ConnBeginTx.
type conn struct {
	mu         sync.Mutex
	socket     net.Conn
	host       string
	port       int
	database   string
	user       string
	password   string
	timeout    time.Duration
	casInfo    [SizeCASInfo]byte
	protoVer   int
	autoCommit bool
	closed     bool
}

var _ driver.QueryerContext = (*conn)(nil)
var _ driver.ExecerContext = (*conn)(nil)
var _ driver.ConnBeginTx = (*conn)(nil)

// connect performs the two-step CUBRID broker handshake and opens the database.
func (c *conn) connect() error {
	brokerAddr := fmt.Sprintf("%s:%d", c.host, c.port)
	brokerConn, err := net.DialTimeout("tcp", brokerAddr, c.timeout) // #nosec G102 -- DB driver must dial user-provided broker address
	if err != nil {
		return &OperationalError{CubridError{Code: -1,
			Message: fmt.Sprintf("dial broker %s: %v", brokerAddr, err)}}
	}

	if c.timeout > 0 {
		brokerConn.SetDeadline(time.Now().Add(c.timeout))
	}

	// Step 1: send ClientInfoExchange (10 bytes, no framing header).
	if _, err = brokerConn.Write(WriteClientInfoExchange()); err != nil {
		brokerConn.Close()
		return err
	}

	// Step 2: receive the redirected CAS port (4 bytes).
	portBuf := make([]byte, 4)
	if _, err = io.ReadFull(brokerConn, portBuf); err != nil {
		brokerConn.Close()
		return err
	}
	newPort := int(int32(binary.BigEndian.Uint32(portBuf)))
	if newPort < 0 {
		brokerConn.Close()
		return &OperationalError{CubridError{Code: newPort,
			Message: "broker rejected connection"}}
	}

	// Step 3: if port > 0, connect to the new CAS port; if 0, reuse the broker socket.
	if newPort > 0 {
		brokerConn.Close()
		casAddr := fmt.Sprintf("%s:%d", c.host, newPort)
		c.socket, err = net.DialTimeout("tcp", casAddr, c.timeout) // #nosec G102 -- DB driver must dial broker-redirected CAS address
		if err != nil {
			return &OperationalError{CubridError{Code: -1,
				Message: fmt.Sprintf("dial CAS %s: %v", casAddr, err)}}
		}
	} else {
		// Port 0 means the CAS is on the same connection.
		c.socket = brokerConn
	}

	if c.timeout > 0 {
		c.socket.SetDeadline(time.Now().Add(c.timeout))
	}

	// Step 4: send OpenDatabase (628 bytes, no framing header).
	if _, err = c.socket.Write(WriteOpenDatabase(c.database, c.user, c.password)); err != nil {
		return err
	}

	// Step 5: receive the framed OpenDatabase response.
	data, err := c.recv()
	if err != nil {
		return err
	}
	res, err := ParseOpenDatabase(data)
	if err != nil {
		return err
	}
	c.casInfo = res.CASInfo
	c.protoVer = res.ProtocolVersion

	c.socket.SetDeadline(time.Time{})
	return nil
}

// ─── Low-level I/O ────────────────────────────────────────────────────────────

// send writes all bytes to the socket.
func (c *conn) send(data []byte) error {
	_, err := c.socket.Write(data)
	return err
}

// recv reads a length-framed response: 4-byte DATA_LENGTH, then CAS_INFO + body.
func (c *conn) recv() ([]byte, error) {
	lenBuf := make([]byte, SizeDataLength)
	if _, err := io.ReadFull(c.socket, lenBuf); err != nil {
		return nil, err
	}
	dataLen := int(binary.BigEndian.Uint32(lenBuf))

	// CUBRID wire: [DATA_LENGTH][CAS_INFO (4 bytes)][body (DATA_LENGTH bytes)]
	// Read CAS_INFO + body together.
	totalLen := dataLen + SizeCASInfo
	data := make([]byte, totalLen)
	if _, err := io.ReadFull(c.socket, data); err != nil {
		return nil, err
	}
	return data, nil
}

// sendAndRecv sends data and returns the framed response.
// Before sending, checks CAS_INFO status and reconnects if the broker
// has released the CAS process — matching the official CUBRID JDBC
// driver's UClientSideConnection.checkReconnect().
func (c *conn) sendAndRecv(data []byte) ([]byte, error) {
	if err := c.checkReconnect(); err != nil {
		return nil, err
	}
	if err := c.send(data); err != nil {
		return nil, driver.ErrBadConn
	}
	resp, err := c.recv()
	if err != nil {
		return nil, driver.ErrBadConn
	}
	// Update CAS_INFO from the response (first 4 bytes).
	if len(resp) >= SizeCASInfo {
		copy(c.casInfo[:], resp[:SizeCASInfo])
	}
	return resp, nil
}

// checkReconnect reconnects when the CAS has been released.
//
// The CUBRID broker sets CAS_INFO[0] to INACTIVE (0) when the CAS
// process is no longer reserved for this client (KEEP_CONNECTION=AUTO).
// The official JDBC driver checks this before every request and
// transparently reconnects.
func (c *conn) checkReconnect() error {
	if c.closed || c.socket == nil {
		return driver.ErrBadConn
	}
	if c.casInfo[0] == casInfoStatusInactive {
		_ = c.socket.Close()
		c.socket = nil
		return c.connect()
	}
	return nil
}

const casInfoStatusInactive = 0

func namedValuesToValues(args []driver.NamedValue) ([]driver.Value, error) {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		if arg.Name != "" {
			return nil, fmt.Errorf("cubrid: named parameters are not supported: %s", arg.Name)
		}
		values[i] = arg.Value
	}
	return values, nil
}

func (c *conn) watchContextCancel(ctx context.Context) func() {
	if c.socket == nil {
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.socket.SetDeadline(time.Now())
		case <-done:
		}
	}()

	return func() { close(done) }
}

func (c *conn) applyContextDeadline(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.socket == nil {
		return nil
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	return c.socket.SetDeadline(deadline)
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

// closeQueryHandle sends FC=6 to release a server-side handle.
// Errors are silently ignored (best-effort cleanup).
func (c *conn) closeQueryHandle(qh int) {
	req := WriteCloseReqHandle(qh, c.casInfo)
	resp, err := c.sendAndRecv(req)
	if err != nil {
		return // best-effort: ignore network errors during cleanup
	}
	_ = ParseSimpleResponse(resp) // #nosec G104 -- best-effort cleanup; result is intentionally discarded
}

// execSQL runs a complete SQL string via PrepareAndExecute (FC=41).
// Used internally for one-shot queries (e.g. SELECT LAST_INSERT_ID()).
func (c *conn) execSQL(sql string) (*PrepareAndExecuteResult, error) {
	req := WritePrepareAndExecute(sql, c.autoCommit, c.casInfo)
	resp, err := c.sendAndRecv(req)
	if err != nil {
		return nil, err
	}
	return ParsePrepareAndExecute(resp, c.protoVer)
}

// fetchLastInsertID retrieves the last auto-generated ID via SQL.
// FC=40 (GET_LAST_INSERT_ID) is unreliable; using SELECT LAST_INSERT_ID() instead.
func (c *conn) fetchLastInsertID() int64 {
	res, err := c.execSQL("SELECT LAST_INSERT_ID()")
	if err != nil {
		return 0
	}
	// FC=41 allocates a server-side query handle; close it immediately
	// to avoid exhausting the CAS query-entry pool (default limit: 100).
	if res.QueryHandle > 0 {
		defer c.closeQueryHandle(res.QueryHandle)
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0
	}
	switch v := res.Rows[0][0].(type) {
	case int64:
		return v
	case string:
		var n int64
		for _, ch := range v {
			if ch >= '0' && ch <= '9' {
				n = n*10 + int64(ch-'0')
			} else {
				break
			}
		}
		return n
	default:
		return 0
	}
}

// ─── driver.Conn ──────────────────────────────────────────────────────────────

// Prepare sends the SQL to the server (FC=2) and returns a reusable Stmt.
// The server validates the SQL and returns a query handle, statement type,
// bind-parameter count, and column metadata.
func (c *conn) Prepare(query string) (driver.Stmt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, driver.ErrBadConn
	}

	req := WritePrepare(query, c.autoCommit, c.casInfo)
	resp, err := c.sendAndRecv(req)
	if err != nil {
		return nil, err
	}
	res, err := ParsePrepare(resp)
	if err != nil {
		return nil, err
	}

	return &stmt{
		conn:        c,
		query:       query,
		queryHandle: res.QueryHandle,
		stmtType:    res.StatementType,
		bindCount:   res.BindCount,
		columns:     res.Columns,
	}, nil
}

// Close gracefully disconnects from the CAS broker.
func (c *conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true
	if c.socket != nil {
		req := WriteConClose(c.casInfo)
		c.socket.Write(req) // #nosec G104 -- best-effort write during connection close; errors are intentionally ignored
		c.socket.Close()
	}
	return nil
}

// Begin starts a transaction (disables auto-commit for this connection).
func (c *conn) Begin() (driver.Tx, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, driver.ErrBadConn
	}
	c.autoCommit = false
	return &tx{conn: c}, nil
}

func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, driver.ErrBadConn
	}
	if err := c.applyContextDeadline(ctx); err != nil {
		return nil, err
	}
	if c.socket != nil {
		defer c.socket.SetDeadline(time.Time{})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	_ = opts
	c.autoCommit = false
	return &tx{conn: c}, nil
}

// Ping verifies the connection is still alive.
func (c *conn) Ping() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return driver.ErrBadConn
	}
	req := WriteGetDbVersion(true, c.casInfo)
	resp, err := c.sendAndRecv(req)
	if err != nil {
		return driver.ErrBadConn
	}
	_, err = ParseGetDbVersion(resp)
	return err
}

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, driver.ErrBadConn
	}
	if err := c.applyContextDeadline(ctx); err != nil {
		return nil, err
	}
	stopWatch := c.watchContextCancel(ctx)
	defer stopWatch()
	if c.socket != nil {
		defer c.socket.SetDeadline(time.Time{})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	values, err := namedValuesToValues(args)
	if err != nil {
		return nil, err
	}

	interpolatedSQL := query
	if len(values) > 0 {
		interpolatedSQL, err = InterpolateArgs(query, values)
		if err != nil {
			return nil, err
		}
	}

	res, err := c.execSQL(interpolatedSQL)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r := &rows{
		conn:         c,
		queryHandle:  res.QueryHandle,
		columns:      res.Columns,
		stmtType:     res.StatementType,
		totalCount:   res.TotalTupleCount,
		fetchedCount: len(res.Rows),
		bufferedRows: res.Rows,
		bufferOffset: 0,
		exhausted:    res.TupleCount == 0 || len(res.Rows) >= res.TotalTupleCount,
		fetchSize:    defaultFetchSize,
		closeHandle:  res.QueryHandle > 0,
	}

	return r, nil
}

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, driver.ErrBadConn
	}
	if err := c.applyContextDeadline(ctx); err != nil {
		return nil, err
	}
	stopWatch := c.watchContextCancel(ctx)
	defer stopWatch()
	if c.socket != nil {
		defer c.socket.SetDeadline(time.Time{})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	values, err := namedValuesToValues(args)
	if err != nil {
		return nil, err
	}

	interpolatedSQL := query
	if len(values) > 0 {
		interpolatedSQL, err = InterpolateArgs(query, values)
		if err != nil {
			return nil, err
		}
	}

	res, err := c.execSQL(interpolatedSQL)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var affected int64
	for _, info := range res.ResultInfos {
		affected += int64(info.ResultCount)
	}

	var lastID int64
	if res.StatementType == StmtInsert {
		lastID = c.fetchLastInsertID()
	}

	if res.QueryHandle > 0 {
		c.closeQueryHandle(res.QueryHandle)
	}

	return &result{lastInsertID: lastID, rowsAffected: affected}, nil
}

// ServerVersion returns the CUBRID engine version string.
func (c *conn) ServerVersion() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	req := WriteGetDbVersion(true, c.casInfo)
	resp, err := c.sendAndRecv(req)
	if err != nil {
		return "", err
	}
	return ParseGetDbVersion(resp)
}
