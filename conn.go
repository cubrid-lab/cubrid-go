package cubrid

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const defaultFetchSize = 100

// conn is a single TCP connection to the CUBRID CAS broker.
// It implements driver.Conn, driver.Pinger, driver.Validator,
// driver.ConnBeginTx, driver.ConnPrepareContext, driver.QueryerContext and
// driver.ExecerContext.
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
	// stringEscapeMode is the negotiated no_backslash_escapes mode for this
	// physical connection. It starts unknown and is resolved on first
	// parameterized execution via ensureStringEscapeModeLocked. It is reset to
	// unknown on every (re)connect. All access is guarded by mu.
	stringEscapeMode StringEscapeMode
	// reqCtx and reqDeadline hold the context and deadline of the running
	// context-aware call (QueryContext, ExecContext, PrepareContext,
	// stmt.ExecContext/QueryContext, BeginTx, Ping) (set by applyContextDeadline,
	// cleared by endRequest) so checkReconnect can honor them.
	reqCtx      context.Context
	reqDeadline time.Time
	// generation identifies the physical CAS session. connect() increments
	// it before opening a session, so a server handle (prepared statement or
	// result set) recorded under an earlier value belongs to a session that
	// no longer exists. The new session numbers its handles from scratch and
	// may already use the same number for another statement, so such a
	// handle must never be sent (see sendAndRecvHandle).
	generation uint64
	// sockMu lets the cancel watcher read socket while the request goroutine,
	// which holds mu for the whole request, replaces it during a reconnect.
	// socket is written only with both mu and sockMu held (see setSocket), or
	// before the conn is published (Driver.Open).
	sockMu sync.Mutex
	// canceled records that the running request's context was canceled. The
	// watcher sets it before expiring the socket deadline, and every deadline
	// that the request path sets afterwards keeps the socket expired, so a
	// later deadline (such as the CHECK_CAS probe's) cannot undo a
	// cancellation. stopWatch clears it.
	canceled atomic.Bool
}

// errStaleHandle reports that a server handle was created on an earlier
// physical session. It is returned before anything is sent.
var errStaleHandle = errors.New("cubrid: server handle belongs to an earlier physical session")

var _ driver.QueryerContext = (*conn)(nil)
var _ driver.ExecerContext = (*conn)(nil)
var _ driver.ConnPrepareContext = (*conn)(nil)
var _ driver.ConnBeginTx = (*conn)(nil)
var _ driver.Pinger = (*conn)(nil)
var _ driver.Validator = (*conn)(nil)

// connect performs the two-step CUBRID broker handshake and opens the database.
func (c *conn) connect() error {
	// Invalidate the handles of the previous session before any new socket
	// can be used, even if this handshake fails part way.
	c.generation++
	// A reconnect runs inside a request: dial with the request's context, so
	// canceling the request or reaching its deadline aborts the dial. Driver.Open
	// has no request context.
	ctx := c.reqCtx
	if ctx == nil {
		ctx = context.Background()
	}
	dialer := net.Dialer{Timeout: c.timeout}
	brokerAddr := fmt.Sprintf("%s:%d", c.host, c.port)
	brokerConn, err := dialer.DialContext(ctx, "tcp", brokerAddr)
	if err != nil {
		return &OperationalError{CubridError{Code: -1,
			Message: fmt.Sprintf("dial broker %s: %v", brokerAddr, err)}}
	}
	// Install the broker socket at once, so the cancel watcher can interrupt
	// the broker handshake below, which otherwise has no bound with timeout=0.
	c.setSocket(brokerConn)

	if c.timeout > 0 {
		_ = c.setSocketDeadline(time.Now().Add(c.timeout))
	}

	// Step 1: send ClientInfoExchange (10 bytes, no framing header).
	if _, err = brokerConn.Write(WriteClientInfoExchange()); err != nil {
		_ = brokerConn.Close()
		return err
	}

	// Step 2: receive the redirected CAS port (4 bytes).
	portBuf := make([]byte, 4)
	if _, err = io.ReadFull(brokerConn, portBuf); err != nil {
		_ = brokerConn.Close()
		return err
	}
	newPort := int(int32(binary.BigEndian.Uint32(portBuf))) // #nosec G115 -- the broker reply is a signed INT (two's complement); negative values are broker error codes handled below
	if newPort < 0 {
		_ = brokerConn.Close()
		return &OperationalError{CubridError{Code: newPort,
			Message: "broker rejected connection"}}
	}

	// Step 3: if port > 0, connect to the new CAS port; if 0, reuse the broker socket.
	if newPort > 0 {
		_ = brokerConn.Close()
		casAddr := fmt.Sprintf("%s:%d", c.host, newPort)
		var casConn net.Conn
		casConn, err = dialer.DialContext(ctx, "tcp", casAddr)
		c.setSocket(casConn)
		if err != nil {
			return &OperationalError{CubridError{Code: -1,
				Message: fmt.Sprintf("dial CAS %s: %v", casAddr, err)}}
		}
	}
	// Port 0 means the CAS is on the same connection: keep the broker socket.

	if c.timeout > 0 {
		_ = c.setSocketDeadline(time.Now().Add(c.timeout))
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
	// A fresh physical connection may target a server with a different
	// no_backslash_escapes setting, so force re-negotiation on next use.
	c.stringEscapeMode = stringEscapeModeUnknown

	_ = c.setSocketDeadline(time.Time{})
	return nil
}

// setSocket installs s as the connection's socket. If the running request
// was already canceled, s starts with an expired deadline, because the cancel
// watcher may have run while there was no socket to expire.
func (c *conn) setSocket(s net.Conn) {
	c.sockMu.Lock()
	c.socket = s
	c.sockMu.Unlock()
	if s != nil && c.canceled.Load() {
		_ = s.SetDeadline(time.Now())
	}
}

// setSocketDeadline sets the socket deadline for the running request. If the
// request was canceled, it leaves the socket expired: the flag is checked
// after the deadline is set, so a cancellation that lands in between is
// still applied, either here or by the watcher, which sets the flag before
// it expires the socket.
func (c *conn) setSocketDeadline(t time.Time) error {
	if c.socket == nil {
		return nil
	}
	err := c.socket.SetDeadline(t)
	if c.canceled.Load() {
		_ = c.socket.SetDeadline(time.Now())
	}
	return err
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
	return c.roundTrip(data)
}

// sendAndRecvHandle is sendAndRecv for a request that names a server handle
// (EXECUTE, FETCH, CLOSE_REQ_HANDLE) created under generation gen. The
// generation is compared after checkReconnect, so a reconnect triggered by
// this very request is caught, and before anything is sent: on a mismatch it
// returns errStaleHandle and the request never reaches the new session.
func (c *conn) sendAndRecvHandle(gen uint64, data []byte) ([]byte, error) {
	if err := c.checkReconnect(); err != nil {
		return nil, err
	}
	if gen != c.generation {
		return nil, errStaleHandle
	}
	return c.roundTrip(data)
}

// retireTransport marks an interrupted physical CAS session unusable. The
// caller holds c.mu in all production request paths, or calls it before the
// conn is published (Driver.Open). A new database/sql
// connection may be opened for a later operation, but this session must never
// be used to replay an ambiguous request.
func (c *conn) retireTransport() {
	c.closed = true
	if c.socket != nil {
		_ = c.socket.Close()
	}
}

// roundTrip sends a single CAS request and reads its framed response.
//
// database/sql is permitted to transparently retry driver.ErrBadConn. That
// sentinel is safe only when the Write accepted zero bytes of the request.
// After any request bytes were accepted, the CAS might have executed the SQL,
// even if the reply was lost; returning ErrBadConn could duplicate a write.
func (c *conn) roundTrip(data []byte) ([]byte, error) {
	if err := checkRequestLength(int64(len(data))); err != nil {
		return nil, err
	}
	written, writeErr := c.socket.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		c.retireTransport()
		if written == 0 {
			return nil, driver.ErrBadConn
		}
		return nil, &OperationalError{CubridError{Code: -1,
			Message: fmt.Sprintf("CAS request write interrupted after %d/%d bytes; execution outcome unknown and unsafe to retry: %v",
				written, len(data), writeErr)}}
	}
	resp, err := c.recv()
	if err != nil {
		c.retireTransport()
		return nil, &OperationalError{CubridError{Code: -1,
			Message: fmt.Sprintf("CAS reply unavailable after request send; execution outcome unknown and unsafe to retry: %v", err)}}
	}
	// Update CAS_INFO from the response (first 4 bytes).
	if len(resp) >= SizeCASInfo {
		copy(c.casInfo[:], resp[:SizeCASInfo])
	}
	return resp, nil
}

// checkReconnect keeps the CAS session across transactions and reconnects
// only when the CAS no longer answers.
//
// CAS_INFO[0] is the transaction status: 0 is OUT_TRAN, 1 is IN_TRAN.
// OUT_TRAN (for example after an autocommitted statement) does not mean the
// CAS was released; the session, including LAST_INSERT_ID(), is still alive.
// Because the broker may still close the socket after an OUT_TRAN reply
// (KEEP_CONNECTION=AUTO, CAS restart), the official JDBC driver probes with
// CHECK_CAS before the next request and reconnects only when that probe
// fails. Reconnecting unconditionally would open a new session and lose
// session state such as the last generated AUTO_INCREMENT id.
func (c *conn) checkReconnect() error {
	if c.closed || c.socket == nil {
		return driver.ErrBadConn
	}
	if c.casInfo[0] != casInfoStatusOutTran {
		return nil
	}
	alive := c.checkCAS()
	if err := c.requestErr(); err != nil {
		// The request was canceled or timed out during the probe. Do not
		// reconnect or send it; a probe reply may still be in flight, so
		// this socket can no longer be used.
		_ = c.socket.Close()
		c.closed = true
		return err
	}
	if alive {
		return nil
	}
	_ = c.socket.Close()
	c.setSocket(nil)
	if err := c.connect(); err != nil {
		// A handshake that failed part way can leave a reply in flight on
		// the new socket (for example the OpenDatabase reply after a
		// cancellation). The next request would read it as its own answer,
		// so this connection must never be used again.
		c.retireTransport()
		return err
	}
	// connect() clears the socket deadline; re-apply the request's.
	if err := c.requestErr(); err != nil {
		return err
	}
	if !c.reqDeadline.IsZero() {
		return c.setSocketDeadline(c.reqDeadline)
	}
	return nil
}

// checkCAS sends CHECK_CAS (FC=32) on the current socket and reports whether
// the CAS answered successfully. The probe is bounded by the connection
// timeout and by the request deadline, whichever is earlier; the request
// deadline is restored afterward. Neither deadline undoes a cancellation of
// the request (see setSocketDeadline).
func (c *conn) checkCAS() bool {
	probeDeadline := c.reqDeadline
	if c.timeout > 0 {
		if d := time.Now().Add(c.timeout); probeDeadline.IsZero() || d.Before(probeDeadline) {
			probeDeadline = d
		}
	}
	_ = c.setSocketDeadline(probeDeadline)
	defer func() { _ = c.setSocketDeadline(c.reqDeadline) }()

	if err := c.send(WriteCheckCas(c.casInfo)); err != nil {
		return false
	}
	resp, err := c.recv()
	if err != nil || ParseCheckCas(resp) != nil {
		return false
	}
	copy(c.casInfo[:], resp[:SizeCASInfo])
	return true
}

// requestErr reports whether the running request's context is done or its
// deadline has passed.
func (c *conn) requestErr() error {
	if c.reqCtx != nil {
		if err := c.reqCtx.Err(); err != nil {
			return err
		}
	}
	if !c.reqDeadline.IsZero() && !time.Now().Before(c.reqDeadline) {
		return context.DeadlineExceeded
	}
	return nil
}

const casInfoStatusOutTran = 0

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

// watchContextCancel interrupts the running request's socket I/O when ctx is
// canceled. The caller holds c.mu for the whole request, so the watcher reads
// the socket under sockMu instead: a reconnect may replace the socket, or
// leave none while it dials. The returned stop function waits for the watcher
// to exit and clears the cancellation flag, so a late cancellation cannot
// leak into the next request on this connection; call it before endRequest,
// which then clears any deadline the watcher left behind.
func (c *conn) watchContextCancel(ctx context.Context) func() {
	if c.socket == nil {
		return func() {}
	}

	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-ctx.Done():
			c.canceled.Store(true)
			c.sockMu.Lock()
			s := c.socket
			c.sockMu.Unlock()
			if s != nil {
				_ = s.SetDeadline(time.Now())
			}
		case <-done:
		}
	}()

	return func() {
		close(done)
		<-exited
		c.canceled.Store(false)
	}
}

func (c *conn) applyContextDeadline(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if ok && c.socket != nil {
		if err := c.socket.SetDeadline(deadline); err != nil {
			return err
		}
	}
	c.reqCtx = ctx
	if ok {
		c.reqDeadline = deadline
	}
	return nil
}

// endRequest clears the request context and deadline set by
// applyContextDeadline.
func (c *conn) endRequest() {
	c.reqCtx = nil
	c.reqDeadline = time.Time{}
	if c.socket != nil {
		_ = c.socket.SetDeadline(time.Time{})
	}
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

// closeQueryHandle sends FC=6 to release a server-side handle created under
// generation gen. Errors are silently ignored (best-effort cleanup). A handle
// from an earlier physical session is dropped without sending anything: the
// old session's handles are already gone, and the same number on the current
// session may belong to another statement.
func (c *conn) closeQueryHandle(qh int, gen uint64) {
	req := WriteCloseReqHandle(qh, c.casInfo)
	resp, err := c.sendAndRecvHandle(gen, req)
	if err != nil {
		return // best-effort: ignore stale handles and network errors
	}
	_ = ParseSimpleResponse(resp)
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

// ensureStringEscapeModeLocked negotiates the server's no_backslash_escapes
// setting once per physical connection and caches it on the conn. The caller
// MUST hold c.mu (all execution entry points do). It mirrors the CUBRID
// JDBC/pycubrid strategy and the sibling cubrid-client driver.
//
// It probes with the raw, constant SQL `SELECT CHAR_LENGTH('\\')` (a literal
// two-backslash string). The result disambiguates the mode:
//   - result 2 -> the two backslashes are literal characters
//     (no_backslash_escapes=yes, the CUBRID default) -> literal mode.
//   - result 1 -> `\\` collapsed to one backslash -> backslash-escape mode.
//
// Any other result is treated as unknown and returned as an error rather than
// guessed, because an incorrect mode silently corrupts every string parameter.
func (c *conn) ensureStringEscapeModeLocked() error {
	if c.stringEscapeMode != stringEscapeModeUnknown {
		return nil
	}

	// NOTE: the probe SQL is a raw constant and MUST NOT be routed through the
	// string escaper (which is exactly what we are negotiating here).
	res, err := c.execSQL(`SELECT CHAR_LENGTH('\\')`)
	if err != nil {
		return err
	}
	gen := c.generation

	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		if res.QueryHandle > 0 {
			c.closeQueryHandle(res.QueryHandle, gen)
		}
		return &OperationalError{CubridError{Code: -1,
			Message: "cubrid: no_backslash_escapes probe returned no rows"}}
	}
	length, ok := probeInt(res.Rows[0][0])
	if !ok {
		if res.QueryHandle > 0 {
			c.closeQueryHandle(res.QueryHandle, gen)
		}
		return &OperationalError{CubridError{Code: -1,
			Message: fmt.Sprintf("cubrid: no_backslash_escapes probe returned non-numeric value %v (%T)", res.Rows[0][0], res.Rows[0][0])}}
	}

	// Release the probe's server-side handle BEFORE recording the negotiated
	// mode. closeQueryHandle may transparently reconnect (the autocommitted
	// probe can leave the CAS marked INACTIVE), and connect() deliberately
	// resets stringEscapeMode to unknown. Assigning the mode last guarantees a
	// reconnect during cleanup cannot wipe the value we just negotiated.
	if res.QueryHandle > 0 {
		c.closeQueryHandle(res.QueryHandle, gen)
	}

	switch length {
	case 2:
		c.stringEscapeMode = stringEscapeModeLiteralBackslash
	case 1:
		c.stringEscapeMode = stringEscapeModeBackslashEscapes
	default:
		return &OperationalError{CubridError{Code: -1,
			Message: fmt.Sprintf("cubrid: unable to determine no_backslash_escapes mode: CHAR_LENGTH probe returned %d (expected 1 or 2); refusing to guess", length)}}
	}
	return nil
}

// probeInt coerces a CUBRID result cell to an int64, accepting the numeric
// representations the driver may produce (int64 or a numeric string).
func probeInt(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// fetchLastInsertID retrieves the last auto-generated ID via SQL.
// It must run on the same CAS session as the INSERT; see checkReconnect.
// It returns an error only when the lookup request fails (for example when
// the request's context ends during it); the INSERT has then already run,
// so callers report the error through Result.LastInsertId, not Exec.
func (c *conn) fetchLastInsertID() (int64, error) {
	res, err := c.execSQL("SELECT LAST_INSERT_ID()")
	if err != nil {
		if ctxErr := c.requestErr(); ctxErr != nil {
			return 0, fmt.Errorf("cubrid: LAST_INSERT_ID lookup interrupted after the statement ran: %w (%v)", ctxErr, err)
		}
		return 0, fmt.Errorf("cubrid: LAST_INSERT_ID lookup failed after the statement ran: %w", err)
	}
	// FC=41 allocates a server-side query handle; close it immediately
	// to avoid exhausting the CAS query-entry pool (default limit: 100).
	if res.QueryHandle > 0 {
		defer c.closeQueryHandle(res.QueryHandle, c.generation)
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0, nil
	}
	switch v := res.Rows[0][0].(type) {
	case int64:
		return v, nil
	case string:
		var n int64
		for _, ch := range v {
			if ch >= '0' && ch <= '9' {
				n = n*10 + int64(ch-'0')
			} else {
				break
			}
		}
		return n, nil
	default:
		return 0, nil
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
	return c.prepareLocked(query)
}

// PrepareContext implements driver.ConnPrepareContext. It is Prepare bounded
// by ctx: canceling ctx, or reaching its deadline, while the PREPARE reply is
// outstanding returns ctx's error, and the interrupted connection is retired
// (IsValid reports false), as for QueryContext.
func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, driver.ErrBadConn
	}
	if err := c.applyContextDeadline(ctx); err != nil {
		return nil, err
	}
	defer c.endRequest()
	stopWatch := c.watchContextCancel(ctx)
	defer stopWatch()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s, err := c.prepareLocked(query)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	return s, nil
}

// prepareLocked sends PREPARE (FC=2) for query. The caller holds c.mu.
func (c *conn) prepareLocked(query string) (driver.Stmt, error) {
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
		generation:  c.generation,
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
		_, _ = c.socket.Write(req)
		_ = c.socket.Close()
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
	defer c.endRequest()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	_ = opts
	c.autoCommit = false
	return &tx{conn: c}, nil
}

// Ping implements driver.Pinger: it sends GET_DB_VERSION (FC=15) and checks
// the reply, so db.Ping and db.PingContext reach the server on a pooled
// connection too. Like any request, it first probes an out-of-transaction
// session with CHECK_CAS and reconnects if the CAS is gone. ctx bounds the
// whole exchange and canceling it interrupts it, as for QueryContext.
//
// It returns an error that matches driver.ErrBadConn (errors.Is) when the
// session cannot be used: the connection is closed, the request could not be
// sent, the reply was lost or a reconnect failed. The cause is wrapped in the
// message. database/sql then closes the connection instead of returning it to
// the pool, and db.PingContext returns the error (it does not retry the ping
// on another connection). GET_DB_VERSION changes nothing on the server, so
// ErrBadConn is safe even when the request was sent. If ctx ends first, Ping
// returns ctx's error instead; the connection is then retired, and IsValid
// keeps database/sql from reusing it.
func (c *conn) Ping(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return driver.ErrBadConn
	}
	if err := c.applyContextDeadline(ctx); err != nil {
		return err
	}
	defer c.endRequest()
	stopWatch := c.watchContextCancel(ctx)
	defer stopWatch()

	req := WriteGetDbVersion(true, c.casInfo)
	resp, err := c.sendAndRecv(req)
	if err != nil {
		// requestErr also catches a deadline that expired the socket just
		// before ctx itself reports it.
		if ctxErr := c.requestErr(); ctxErr != nil {
			return ctxErr
		}
		if errors.Is(err, driver.ErrBadConn) {
			return err
		}
		return fmt.Errorf("%w: cubrid ping: %v", driver.ErrBadConn, err)
	}
	_, err = ParseGetDbVersion(resp)
	return err
}

// IsValid implements driver.Validator. database/sql calls it before it
// returns a connection to the pool or reuses one: a connection that a
// request retired (a canceled or timed-out request whose reply may still be
// in flight, a lost reply, a failed reconnect) is closed instead of being
// handed to the next caller.
func (c *conn) IsValid() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed
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

	interpolatedSQL := query
	if len(values) > 0 {
		if err = c.ensureStringEscapeModeLocked(); err != nil {
			return nil, err
		}
		interpolatedSQL, err = InterpolateArgsWithMode(query, values, c.stringEscapeMode)
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
	// The reply has arrived, so the request completed: return its result
	// even if ctx ended meanwhile. Only a request whose reply was still
	// outstanding returns ctx's error (above).
	gen := c.generation

	r := &rows{
		conn:         c,
		generation:   gen,
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

	interpolatedSQL := query
	if len(values) > 0 {
		if err = c.ensureStringEscapeModeLocked(); err != nil {
			return nil, err
		}
		interpolatedSQL, err = InterpolateArgsWithMode(query, values, c.stringEscapeMode)
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
	// The reply has arrived, so the request completed: return its result
	// even if ctx ended meanwhile. Only a request whose reply was still
	// outstanding returns ctx's error (above).
	gen := c.generation

	var affected int64
	for _, info := range res.ResultInfos {
		affected += int64(info.ResultCount)
	}

	var lastID int64
	var lastIDErr error
	if res.StatementType == StmtInsert {
		lastID, lastIDErr = c.fetchLastInsertID()
	}

	if res.QueryHandle > 0 {
		c.closeQueryHandle(res.QueryHandle, gen)
	}

	return &result{lastInsertID: lastID, lastInsertIDErr: lastIDErr, rowsAffected: affected}, nil
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
