package cubrid

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type deadlineTrackingConn struct {
	net.Conn

	mu        sync.Mutex
	deadlines []time.Time
	writes    int
}

func (c *deadlineTrackingConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadlines = append(c.deadlines, t)
	c.mu.Unlock()
	return c.Conn.SetDeadline(t)
}

func (c *deadlineTrackingConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	c.writes++
	c.mu.Unlock()
	return c.Conn.Write(b)
}

func (c *deadlineTrackingConn) deadlineCalls() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Time, len(c.deadlines))
	copy(out, c.deadlines)
	return out
}

func (c *deadlineTrackingConn) writeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes
}

func consumeOneRequest(t *testing.T, conn net.Conn) {
	t.Helper()

	lenBuf := make([]byte, SizeDataLength)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		t.Fatalf("read request length: %v", err)
	}
	payloadLen := int(binary.BigEndian.Uint32(lenBuf)) + SizeCASInfo
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(conn, payload); err != nil {
		t.Fatalf("read request payload: %v", err)
	}
}

func prepareAndExecuteResponsePacket(stmtType byte, totalCount, resultCount, tupleCount int32) []byte {
	w := newPacketWriter()
	w.writeInt(1)
	w.writeInt(0)
	w.writeByte(stmtType)
	w.writeInt(0)
	w.writeByte(0)
	w.writeInt(0)
	w.writeInt(totalCount)
	w.writeByte(0)
	w.writeInt(1)
	w.writeByte(stmtType)
	w.writeInt(resultCount)
	w.writeRawBytes(make([]byte, SizeOID))
	w.writeInt(0)
	w.writeInt(0)
	w.writeByte(0)
	w.writeInt(0)
	if stmtType == StmtSelect {
		w.writeInt(0)
		w.writeInt(tupleCount)
	}
	body := w.toBytes()

	var casInfo [SizeCASInfo]byte
	casInfo[0] = 1 // ACTIVE – prevent checkReconnect from triggering
	header := buildProtocolHeader(len(body), casInfo)
	packet := make([]byte, 0, len(header)+len(body))
	packet = append(packet, header...)
	packet = append(packet, body...)
	return packet
}

func simpleResponsePacket(code int32) []byte {
	w := newPacketWriter()
	w.writeInt(code)
	body := w.toBytes()

	var casInfo [SizeCASInfo]byte
	casInfo[0] = 1 // ACTIVE – prevent checkReconnect from triggering
	header := buildProtocolHeader(len(body), casInfo)
	packet := make([]byte, 0, len(header)+len(body))
	packet = append(packet, header...)
	packet = append(packet, body...)
	return packet
}

// probeResponsePacket builds a PREPARE_AND_EXECUTE response for the
// no_backslash_escapes probe (`SELECT CHAR_LENGTH('\\')`): a single-column,
// single-row SELECT result whose only cell is the given integer length.
// A charLength of 2 signals literal-backslash mode; 1 signals backslash-escape.
func probeResponsePacket(charLength int32) []byte {
	w := newPacketWriter()
	w.writeInt(1)           // response code == query handle (>0)
	w.writeInt(0)           // result cache lifetime
	w.writeByte(StmtSelect) // statement type
	w.writeInt(0)           // bind count
	w.writeByte(0)          // is_updatable
	w.writeInt(1)           // column count
	// single INTEGER column
	w.writeByte(TypeInt)             // legacy column type (no extended-type bit)
	w.writeShort(0)                  // scale
	w.writeInt(0)                    // precision
	w.writeNullTermString("charlen") // name
	w.writeNullTermString("")        // real name
	w.writeNullTermString("")        // table name
	w.writeByte(1)                   // is_nullable
	w.writeNullTermString("")        // default value
	w.writeByte(0)                   // is_auto_increment
	w.writeByte(0)                   // is_unique_key
	w.writeByte(0)                   // is_primary_key
	w.writeByte(0)                   // is_reverse_index
	w.writeByte(0)                   // is_reverse_unique
	w.writeByte(0)                   // is_foreign_key
	w.writeByte(0)                   // is_shared
	w.writeInt(1)                    // total tuple count
	w.writeByte(0)                   // cache_reusable
	w.writeInt(0)                    // result-info count
	w.writeByte(0)                   // includes_column_info (proto > 1)
	w.writeInt(0)                    // shard_id (proto > 4)
	w.writeInt(0)                    // fetch_code
	w.writeInt(1)                    // tuple count in this packet
	// row 0
	w.writeInt(0)                          // row index
	w.writeRawBytes(make([]byte, SizeOID)) // OID
	w.writeInt(4)                          // column value size
	w.writeInt(charLength)                 // CHAR_LENGTH result
	body := w.toBytes()

	var casInfo [SizeCASInfo]byte
	casInfo[0] = 1 // ACTIVE – prevent checkReconnect from triggering
	header := buildProtocolHeader(len(body), casInfo)
	packet := make([]byte, 0, len(header)+len(body))
	packet = append(packet, header...)
	packet = append(packet, body...)
	return packet
}

func TestQueryContextWorksAndAppliesDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	tracked := &deadlineTrackingConn{Conn: client}
	c := &conn{socket: tracked, autoCommit: true, protoVer: ProtoVersion}
	c.casInfo[0] = 1 // ACTIVE – prevent checkReconnect from triggering

	go func() {
		consumeOneRequest(t, server)
		_, _ = server.Write(prepareAndExecuteResponsePacket(StmtSelect, 0, 0, 0))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	rows, err := c.QueryContext(ctx, "SELECT 1", nil)
	if err != nil {
		t.Fatalf("QueryContext error: %v", err)
	}

	if err := rows.Next(make([]driver.Value, 0)); !errors.Is(err, io.EOF) {
		t.Fatalf("rows.Next expected EOF, got %v", err)
	}

	deadlineCalls := tracked.deadlineCalls()
	if len(deadlineCalls) == 0 {
		t.Fatalf("expected SetDeadline calls")
	}
	if deadlineCalls[len(deadlineCalls)-1] != (time.Time{}) {
		t.Fatalf("expected deadline reset to zero time")
	}
}

func TestExecContextWorksWithDML(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	tracked := &deadlineTrackingConn{Conn: client}
	c := &conn{socket: tracked, autoCommit: true, protoVer: ProtoVersion}
	c.casInfo[0] = 1 // ACTIVE – prevent checkReconnect from triggering

	go func() {
		// 1. no_backslash_escapes probe (SELECT CHAR_LENGTH('\\')) -> 2 (literal).
		consumeOneRequest(t, server)
		_, _ = server.Write(probeResponsePacket(2))
		// 2. probe closes its server-side query handle.
		consumeOneRequest(t, server)
		_, _ = server.Write(simpleResponsePacket(0))
		// 3. the actual UPDATE.
		consumeOneRequest(t, server)
		_, _ = server.Write(prepareAndExecuteResponsePacket(StmtUpdate, 2, 2, 0))
		consumeOneRequest(t, server)
		_, _ = server.Write(simpleResponsePacket(0))
	}()

	res, err := c.ExecContext(context.Background(), "UPDATE t SET v = ?", []driver.NamedValue{{Ordinal: 1, Value: int64(7)}})
	if err != nil {
		t.Fatalf("ExecContext error: %v", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("RowsAffected error: %v", err)
	}
	if affected != 2 {
		t.Fatalf("expected 2 affected rows, got %d", affected)
	}
}

func TestBeginTxAcceptsOptions(t *testing.T) {
	c := &conn{autoCommit: true}

	tx, err := c.BeginTx(context.Background(), driver.TxOptions{
		Isolation: driver.IsolationLevel(6),
		ReadOnly:  true,
	})
	if err != nil {
		t.Fatalf("BeginTx error: %v", err)
	}
	if tx == nil {
		t.Fatalf("expected tx")
	}
	if c.autoCommit {
		t.Fatalf("expected autoCommit=false after BeginTx")
	}
}

func TestExecContextExpiredContextReturnsImmediately(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	tracked := &deadlineTrackingConn{Conn: client}
	c := &conn{socket: tracked, autoCommit: true, protoVer: ProtoVersion}
	c.casInfo[0] = 1 // ACTIVE – prevent checkReconnect from triggering

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := c.ExecContext(ctx, "UPDATE t SET v=1", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	if tracked.writeCount() != 0 {
		t.Fatalf("expected no network writes for expired context")
	}
}

func TestQueryContextCancellationInterruptsInflightOperation(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	tracked := &deadlineTrackingConn{Conn: client}
	c := &conn{socket: tracked, autoCommit: true, protoVer: ProtoVersion}
	c.casInfo[0] = 1 // ACTIVE – prevent checkReconnect from triggering

	requestRead := make(chan struct{})
	go func() {
		consumeOneRequest(t, server)
		close(requestRead)
		select {
		case <-time.After(2 * time.Second):
		case <-time.After(10 * time.Millisecond):
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		rows, err := c.QueryContext(ctx, "SELECT 1", nil)
		if err == nil {
			_ = rows.Close()
		}
		errCh <- err
	}()

	select {
	case <-requestRead:
	case <-time.After(time.Second):
		t.Fatal("request was not sent")
	}

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("QueryContext did not return after cancellation")
	}
}

// TestPreparedPathsHonorContextCancellation checks the context-aware prepare
// and prepared-statement paths (#87): canceling the context while the CAS
// holds the reply returns context.Canceled within a second, and the
// interrupted connection is retired. database/sql then closes it instead of
// returning it to the pool, so the following ping, which database/sql does
// not retry, runs on a new session.
func TestPreparedPathsHonorContextCancellation(t *testing.T) {
	for _, tc := range []struct {
		name string
		// prepare is the statement prepared before the hold, or "" for none.
		prepare string
		hold    byte
		run     func(ctx context.Context, db *sql.DB, st *sql.Stmt) error
	}{
		{name: "PrepareContext", hold: FuncPrepare,
			run: func(ctx context.Context, db *sql.DB, _ *sql.Stmt) error {
				st, err := db.PrepareContext(ctx, "SELECT v FROM t")
				if err == nil {
					_ = st.Close()
				}
				return err
			}},
		{name: "Stmt.ExecContext", prepare: "UPDATE t SET v = 1", hold: FuncExecute,
			run: func(ctx context.Context, _ *sql.DB, st *sql.Stmt) error {
				_, err := st.ExecContext(ctx)
				return err
			}},
		{name: "Stmt.QueryContext", prepare: "SELECT v FROM t", hold: FuncExecute,
			run: func(ctx context.Context, _ *sql.DB, st *sql.Stmt) error {
				rows, err := st.QueryContext(ctx)
				if err == nil {
					_ = rows.Close()
				}
				return err
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCAS(t)
			db, err := sql.Open("cubrid", f.dsn())
			if err != nil {
				t.Fatalf("sql.Open: %v", err)
			}
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			if err := db.Ping(); err != nil {
				t.Fatalf("first Ping: %v", err)
			}
			var st *sql.Stmt
			if tc.prepare != "" {
				if st, err = db.Prepare(tc.prepare); err != nil {
					t.Fatalf("Prepare: %v", err)
				}
				defer func() { _ = st.Close() }()
			}
			arrived := holdFirst(t, f, tc.hold)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- tc.run(ctx, db, st) }()
			select {
			case <-arrived:
			case <-time.After(3 * time.Second):
				t.Fatalf("the request never reached the CAS")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("%s = %v, want context.Canceled", tc.name, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s did not return within 1s of cancellation", tc.name)
			}

			if err := db.Ping(); err != nil {
				t.Fatalf("Ping after the canceled request: %v", err)
			}
			pingSessionLog(t, f, "s2", "s2 GET_DB_VERSION")
		})
	}
}

// TestPreparedPathsRetireConnOnCancel drives the driver directly: after a
// canceled in-flight PrepareContext, stmt.ExecContext or stmt.QueryContext,
// IsValid reports false and the next request returns driver.ErrBadConn
// without sending anything.
func TestPreparedPathsRetireConnOnCancel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare string
		hold    byte
		run     func(ctx context.Context, c *conn, s *stmt) error
	}{
		{name: "PrepareContext", hold: FuncPrepare,
			run: func(ctx context.Context, c *conn, _ *stmt) error {
				_, err := c.PrepareContext(ctx, "SELECT v FROM t")
				return err
			}},
		{name: "stmt.ExecContext", prepare: "UPDATE t SET v = 1", hold: FuncExecute,
			run: func(ctx context.Context, _ *conn, s *stmt) error {
				_, err := s.ExecContext(ctx, nil)
				return err
			}},
		{name: "stmt.QueryContext", prepare: "SELECT v FROM t", hold: FuncExecute,
			run: func(ctx context.Context, _ *conn, s *stmt) error {
				_, err := s.QueryContext(ctx, nil)
				return err
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCAS(t)
			dc, err := (&Driver{}).Open(f.dsn())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			c := dc.(*conn)
			defer func() { _ = c.Close() }()
			var s *stmt
			if tc.prepare != "" {
				ds, err := c.Prepare(tc.prepare)
				if err != nil {
					t.Fatalf("Prepare: %v", err)
				}
				s = ds.(*stmt)
			}
			arrived := holdFirst(t, f, tc.hold)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- tc.run(ctx, c, s) }()
			select {
			case <-arrived:
			case <-time.After(3 * time.Second):
				t.Fatalf("the request never reached the CAS")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("%s = %v, want context.Canceled", tc.name, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s did not return within 1s of cancellation", tc.name)
			}

			if c.IsValid() {
				t.Fatalf("IsValid reports the interrupted connection as valid")
			}
			before := len(f.entries())
			if _, err := c.PrepareContext(context.Background(), "SELECT v FROM t"); !errors.Is(err, driver.ErrBadConn) {
				t.Fatalf("next request = %v, want driver.ErrBadConn", err)
			}
			if got := f.entries(); len(got) != before {
				t.Fatalf("the retired connection sent %q", got[before:])
			}
		})
	}
}

// TestPreparedPathsAlreadyCanceledSendNothing: an already canceled context
// returns its error before anything is sent, and leaves the connection usable.
func TestPreparedPathsAlreadyCanceledSendNothing(t *testing.T) {
	f := newFakeCAS(t)
	dc, err := (&Driver{}).Open(f.dsn())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := dc.(*conn)
	defer func() { _ = c.Close() }()
	ds, err := c.Prepare("SELECT v FROM t")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	s := ds.(*stmt)
	before := len(f.entries())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.PrepareContext(ctx, "SELECT v FROM t"); !errors.Is(err, context.Canceled) {
		t.Fatalf("PrepareContext = %v, want context.Canceled", err)
	}
	if _, err := s.ExecContext(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("stmt.ExecContext = %v, want context.Canceled", err)
	}
	if _, err := s.QueryContext(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("stmt.QueryContext = %v, want context.Canceled", err)
	}
	if got := f.entries(); len(got) != before {
		t.Fatalf("canceled calls sent %q", got[before:])
	}
	if !c.IsValid() {
		t.Fatalf("IsValid = false after calls that sent nothing")
	}
}

// cancelOnFirst makes f cancel ctx when the first request with function code
// fc whose log entry contains substr reaches it. With drop, that request's
// session is closed without a reply; otherwise the reply is still sent.
func cancelOnFirst(f *fakeCAS, fc byte, substr string, cancel context.CancelFunc, drop bool) {
	var once sync.Once
	f.beforeReply = func(code byte, _ net.Conn) bool {
		if code != fc {
			return true
		}
		entries := f.entries()
		if len(entries) == 0 || !strings.Contains(entries[len(entries)-1], substr) {
			return true
		}
		hit := false
		once.Do(func() { hit = true })
		if !hit {
			return true
		}
		cancel()
		return !drop
	}
}

// expiryAfterWriteConn holds back any deadline that expires the socket until
// the driver starts a Write after it was requested (or 200ms pass), so a
// cancel watcher that fires while a reply is in transit cannot interrupt
// reading that reply.
type expiryAfterWriteConn struct {
	net.Conn
	writes atomic.Int64
}

func (e *expiryAfterWriteConn) SetDeadline(t time.Time) error {
	if !t.IsZero() && !t.After(time.Now()) {
		n := e.writes.Load()
		for end := time.Now().Add(200 * time.Millisecond); e.writes.Load() == n && time.Now().Before(end); {
			time.Sleep(time.Millisecond)
		}
	}
	return e.Conn.SetDeadline(t)
}

func (e *expiryAfterWriteConn) Write(b []byte) (int, error) {
	e.writes.Add(1)
	return e.Conn.Write(b)
}

// TestReplyThatArrivedWinsOverLateCancel: when ctx ends while the FC=41
// reply is already on its way, the request completed, so Exec returns its
// result instead of ctx's error, and the reply's server handle is not left
// on a pooled session: either CLOSE_REQ reaches the CAS or, when the
// cancellation interrupts that cleanup, the session is retired and the
// handle goes with it (#87).
func TestReplyThatArrivedWinsOverLateCancel(t *testing.T) {
	for _, tc := range []struct {
		name string
		// run calls wrap just before the request under test.
		run func(ctx context.Context, c *conn, wrap func()) (driver.Result, error)
		// closed is the CLOSE_REQ log entry for the FC=41 handle.
		closed string
	}{
		{name: "conn.ExecContext", closed: "s1 CLOSE_REQ UPDATE t SET v = 1",
			run: func(ctx context.Context, c *conn, wrap func()) (driver.Result, error) {
				wrap()
				return c.ExecContext(ctx, "UPDATE t SET v = 1", nil)
			}},
		{name: "stmt.ExecContext", closed: "s1 CLOSE_REQ SELECT v FROM t",
			run: func(ctx context.Context, c *conn, wrap func()) (driver.Result, error) {
				// Rows that still fetch with the prepared handle make Exec
				// use FC=41 (see stmt.openRows).
				ds, err := c.Prepare("SELECT v FROM t")
				if err != nil {
					return nil, err
				}
				s := ds.(*stmt)
				if _, err := s.Query(nil); err != nil {
					return nil, err
				}
				wrap()
				return s.ExecContext(ctx, nil)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCAS(t)
			f.rowsTotal = 2 // the stmt case's rows stay open
			dc, err := (&Driver{}).Open(f.dsn())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			c := dc.(*conn)
			defer func() { _ = c.Close() }()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.beforeReply = func(code byte, _ net.Conn) bool {
				if code == FuncPrepareAndExecute {
					cancel() // the reply is written right after this
				}
				return true
			}
			wrap := func() { c.setSocket(&expiryAfterWriteConn{Conn: c.socket}) }

			res, err := tc.run(ctx, c, wrap)
			if err != nil {
				t.Fatalf("%s = %v, want its result: the reply had arrived", tc.name, err)
			}
			if _, err := res.RowsAffected(); err != nil {
				t.Fatalf("RowsAffected: %v", err)
			}
			if ctx.Err() == nil {
				t.Fatalf("the context was never canceled")
			}
			released := false
			for _, e := range f.entries() {
				released = released || e == tc.closed
			}
			if !released && c.IsValid() {
				t.Fatalf("the FC=41 handle was neither released nor its session retired; log: %q", f.entries())
			}
		})
	}
}

// TestLastInsertIDLookupCanceled: canceling ctx during the LAST_INSERT_ID()
// lookup after an INSERT keeps Exec successful, because the INSERT ran;
// RowsAffected is correct and LastInsertId returns the cancellation (#87).
func TestLastInsertIDLookupCanceled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		affected int64
		run      func(ctx context.Context, c *conn) (driver.Result, error)
	}{
		{name: "conn.ExecContext", affected: 1,
			run: func(ctx context.Context, c *conn) (driver.Result, error) {
				return c.ExecContext(ctx, "INSERT INTO t VALUES (1)", nil)
			}},
		{name: "stmt.ExecContext", affected: 1,
			run: func(ctx context.Context, c *conn) (driver.Result, error) {
				ds, err := c.Prepare("INSERT INTO t VALUES (1)")
				if err != nil {
					return nil, err
				}
				s := ds.(*stmt)
				s.stmtType = StmtInsert // the fake CAS prepares every non-SELECT as an UPDATE
				return s.ExecContext(ctx, nil)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCAS(t)
			dc, err := (&Driver{}).Open(f.dsn())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			c := dc.(*conn)
			defer func() { _ = c.Close() }()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cancelOnFirst(f, FuncPrepareAndExecute, "LAST_INSERT_ID", cancel, true)

			done := make(chan struct{})
			var res driver.Result
			go func() {
				defer close(done)
				res, err = tc.run(ctx, c)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatalf("%s did not return within 1s of cancellation", tc.name)
			}
			if err != nil {
				t.Fatalf("%s = %v, want success: the INSERT ran", tc.name, err)
			}
			if n, err := res.RowsAffected(); err != nil || n != tc.affected {
				t.Fatalf("RowsAffected = %d, %v; want %d, nil", n, err, tc.affected)
			}
			if id, err := res.LastInsertId(); !errors.Is(err, context.Canceled) {
				t.Fatalf("LastInsertId = %d, %v; want context.Canceled", id, err)
			}
			if c.IsValid() {
				t.Fatalf("IsValid reports the interrupted connection as valid")
			}
		})
	}
}
