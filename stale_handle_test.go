package cubrid

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCAS is a broker on a TCP listener. Every accepted connection is a new
// CAS session that numbers its prepared handles from 1, as a CUBRID broker
// does after a restart, so a handle number from an earlier session names a
// different statement on a later one. Each session logs the requests that
// name a handle as "s<session> <REQUEST> <sql of the handle>".
type fakeCAS struct {
	t    *testing.T
	l    net.Listener
	mu   sync.Mutex
	open []net.Conn
	log  []string
	// rowsTotal is the total tuple count a PREPARE_AND_EXECUTE reply reports
	// alongside its single inline row.
	rowsTotal int32
}

func newFakeCAS(t *testing.T) *fakeCAS {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeCAS{t: t, l: l, rowsTotal: 1}
	t.Cleanup(func() {
		_ = l.Close()
		f.restart()
	})
	go func() {
		for session := 1; ; session++ {
			nc, err := l.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.open = append(f.open, nc)
			f.mu.Unlock()
			go f.serve(session, nc)
		}
	}()
	return f
}

func (f *fakeCAS) port() int { return f.l.Addr().(*net.TCPAddr).Port }

// restart closes every open session, as `cubrid broker restart` does.
func (f *fakeCAS) restart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, nc := range f.open {
		_ = nc.Close()
	}
	f.open = nil
}

func (f *fakeCAS) record(entry string) {
	f.mu.Lock()
	f.log = append(f.log, entry)
	f.mu.Unlock()
}

func (f *fakeCAS) entries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

// waitFor waits until the log contains entry.
func (f *fakeCAS) waitFor(entry string) {
	f.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range f.entries() {
			if e == entry {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.t.Fatalf("fake CAS never logged %q; log: %q", entry, f.entries())
}

func (f *fakeCAS) serve(session int, nc net.Conn) {
	defer func() { _ = nc.Close() }()
	if _, err := io.ReadFull(nc, make([]byte, 10)); err != nil { // ClientInfoExchange
		return
	}
	if _, err := nc.Write([]byte{0, 0, 0, 0}); err != nil { // CAS on this socket
		return
	}
	if _, err := io.ReadFull(nc, make([]byte, len(WriteOpenDatabase("", "", "")))); err != nil {
		return
	}
	open := newPacketWriter()
	open.writeInt(0)
	broker := make([]byte, SizeBrokerInfo)
	broker[4] = ProtoVersion
	open.writeRawBytes(broker)
	open.writeInt(int32(session))
	if _, err := nc.Write(outTranPacket(open.toBytes())); err != nil {
		return
	}

	var handles []string // handles[h-1] is the SQL prepared as handle h
	sqlOf := func(body []byte) string {
		h := int(int32(binary.BigEndian.Uint32(body[4:8]))) // addInt(handle)
		if h < 1 || h > len(handles) {
			return fmt.Sprintf("unknown handle %d", h)
		}
		return handles[h-1]
	}
	name := fmt.Sprintf("s%d", session)
	for {
		fc, body, err := readRequest(nc)
		if err != nil {
			return
		}
		var reply []byte
		switch fc {
		case FuncCheckCas:
			reply = emptyCheckCasResponse()
		case FuncPrepare:
			n := int(binary.BigEndian.Uint32(body[:4]))
			query := string(body[4 : 4+n-1])
			handles = append(handles, query)
			f.record(fmt.Sprintf("%s PREPARE %s", name, query))
			w := newPacketWriter()
			w.writeInt(int32(len(handles))) // query handle
			w.writeInt(0)                   // result cache lifetime
			w.writeByte(StmtUpdate)
			w.writeInt(0) // bind count
			w.writeByte(0)
			w.writeInt(0) // column count
			reply = outTranPacket(w.toBytes())
		case FuncExecute:
			f.record(fmt.Sprintf("%s EXECUTE %s", name, sqlOf(body)))
			w := newPacketWriter()
			w.writeInt(1) // total tuple count
			w.writeByte(0)
			w.writeInt(1) // result-info count
			w.writeByte(StmtUpdate)
			w.writeInt(1) // affected rows
			w.writeRawBytes(make([]byte, SizeOID))
			w.writeInt(0)
			w.writeInt(0)
			w.writeByte(0) // includes_column_info
			w.writeInt(0)  // shard_id
			reply = outTranPacket(w.toBytes())
		case FuncPrepareAndExecute:
			n := int(binary.BigEndian.Uint32(body[8:12])) // after addInt(arg count)
			query := string(body[12 : 12+n-1])
			handles = append(handles, query)
			f.record(fmt.Sprintf("%s PREPARE_AND_EXECUTE %s", name, query))
			reply = outTranPacket(oneRowSelectBody(int32(len(handles)), f.rowsTotal))
		case FuncFetch:
			f.record(fmt.Sprintf("%s FETCH %s", name, sqlOf(body)))
			return
		case FuncCloseReqHandle:
			f.record(fmt.Sprintf("%s CLOSE_REQ %s", name, sqlOf(body)))
			reply = simpleResponsePacket(0)
		case FuncConClose:
			f.record(name + " CON_CLOSE")
			return
		default:
			f.record(fmt.Sprintf("%s FC%d", name, fc))
			reply = simpleResponsePacket(0)
		}
		if _, err := nc.Write(reply); err != nil {
			return
		}
	}
}

// readRequest reads one framed request and returns its function code and the
// bytes after it.
func readRequest(nc net.Conn) (byte, []byte, error) {
	lenBuf := make([]byte, SizeDataLength)
	if _, err := io.ReadFull(nc, lenBuf); err != nil {
		return 0, nil, err
	}
	payload := make([]byte, int(binary.BigEndian.Uint32(lenBuf))+SizeCASInfo)
	if _, err := io.ReadFull(nc, payload); err != nil {
		return 0, nil, err
	}
	if len(payload) <= SizeCASInfo {
		return 0, nil, errors.New("request has no function code")
	}
	return payload[SizeCASInfo], payload[SizeCASInfo+1:], nil
}

// outTranPacket frames body with an OUT_TRAN CAS_INFO, as the CAS replies
// after an autocommitted request, so the next request probes with CHECK_CAS.
func outTranPacket(body []byte) []byte {
	var casInfo [SizeCASInfo]byte
	return append(buildProtocolHeader(len(body), casInfo), body...)
}

// oneRowSelectBody is a PREPARE_AND_EXECUTE reply body for a one-INTEGER-column
// SELECT that reports total rows and carries only the first one inline.
func oneRowSelectBody(handle, total int32) []byte {
	w := newPacketWriter()
	w.writeInt(handle)
	w.writeInt(0) // result cache lifetime
	w.writeByte(StmtSelect)
	w.writeInt(0) // bind count
	w.writeByte(0)
	w.writeInt(1) // column count
	w.writeByte(TypeInt)
	w.writeShort(0)
	w.writeInt(0)
	w.writeNullTermString("v")
	w.writeNullTermString("")
	w.writeNullTermString("")
	w.writeByte(1)
	w.writeNullTermString("")
	for i := 0; i < 7; i++ {
		w.writeByte(0)
	}
	w.writeInt(total) // total tuple count
	w.writeByte(0)
	w.writeInt(0) // result-info count
	w.writeByte(0)
	w.writeInt(0) // shard_id
	w.writeInt(0) // fetch_code
	w.writeInt(1) // tuple count in this packet
	w.writeInt(0) // row index
	w.writeRawBytes(make([]byte, SizeOID))
	w.writeInt(4)
	w.writeInt(7)
	return w.toBytes()
}

func (f *fakeCAS) dsn() string {
	return fmt.Sprintf("cubrid://dba:@127.0.0.1:%d/testdb?timeout=2s", f.port())
}

// sessionLog returns the log entries of one session.
func sessionLog(entries []string, session string) []string {
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e, session+" ") {
			out = append(out, e)
		}
	}
	return out
}

// TestStalePreparedStmtIsNotSentOnNewSession reproduces issue #40 at the
// driver level: after the broker restarts, the connection reconnects, the new
// session prepares DELETE as handle 1, and the statement prepared earlier as
// handle 1 is executed again. The driver must return driver.ErrBadConn without
// sending EXECUTE, and must not CLOSE_REQ the old handle on the new session.
func TestStalePreparedStmtIsNotSentOnNewSession(t *testing.T) {
	f := newFakeCAS(t)
	dc, err := (&Driver{}).Open(f.dsn())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := dc.(*conn)

	prepared, err := c.Prepare("UPDATE t SET v = 'OLD'")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	old := prepared.(*stmt)
	if _, err := old.Exec(nil); err != nil {
		t.Fatalf("first Exec: %v", err)
	}

	f.restart()
	del, err := c.Prepare("DELETE FROM t")
	if err != nil {
		t.Fatalf("Prepare after broker restart: %v", err)
	}

	if _, err := old.Exec(nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("Exec of a statement from the earlier session: got %v, want driver.ErrBadConn", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := del.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("conn Close: %v", err)
	}
	f.waitFor("s2 CON_CLOSE")

	got := sessionLog(f.entries(), "s2")
	want := []string{"s2 PREPARE DELETE FROM t", "s2 CLOSE_REQ DELETE FROM t", "s2 CON_CLOSE"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("new session received %q, want %q", got, want)
	}
}

// TestStaleResultSetIsNotFetchedOnNewSession verifies that a result set whose
// server cursor belongs to an earlier physical session neither FETCHes nor
// CLOSE_REQs that handle on the new session.
func TestStaleResultSetIsNotFetchedOnNewSession(t *testing.T) {
	f := newFakeCAS(t)
	f.rowsTotal = 2
	dc, err := (&Driver{}).Open(f.dsn())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := dc.(*conn)

	r, err := c.QueryContext(context.Background(), "SELECT v FROM t", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	dest := make([]driver.Value, 1)
	if err := r.Next(dest); err != nil {
		t.Fatalf("first Next: %v", err)
	}

	f.restart()
	err = r.Next(dest)
	var opErr *OperationalError
	if !errors.As(err, &opErr) {
		t.Fatalf("Next after reconnect: got %v, want an OperationalError", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("rows Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("conn Close: %v", err)
	}
	f.waitFor("s2 CON_CLOSE")

	if got := sessionLog(f.entries(), "s2"); len(got) != 1 {
		t.Fatalf("new session received %q, want only CON_CLOSE", got)
	}
}

// TestDBStmtReprepareAfterBrokerRestart verifies the database/sql behavior:
// a *sql.Stmt whose handle is from an earlier session is transparently
// prepared again on a fresh connection and runs its own statement, never the
// statement the new session prepared under the same handle number.
func TestDBStmtReprepareAfterBrokerRestart(t *testing.T) {
	f := newFakeCAS(t)
	db, err := sql.Open("cubrid", f.dsn())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	old, err := db.Prepare("UPDATE t SET v = 'OLD'")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() { _ = old.Close() }()
	if _, err := old.Exec(); err != nil {
		t.Fatalf("first Exec: %v", err)
	}

	f.restart()
	del, err := db.Prepare("DELETE FROM t")
	if err != nil {
		t.Fatalf("Prepare after broker restart: %v", err)
	}
	defer func() { _ = del.Close() }()

	res, err := old.Exec()
	if err != nil {
		t.Fatalf("Exec after broker restart: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("RowsAffected = %d, want 1", n)
	}

	var executed []string
	for _, e := range f.entries() {
		if strings.Contains(e, " EXECUTE ") {
			executed = append(executed, e)
		}
	}
	want := []string{"s1 EXECUTE UPDATE t SET v = 'OLD'", "s3 EXECUTE UPDATE t SET v = 'OLD'"}
	if strings.Join(executed, "\n") != strings.Join(want, "\n") {
		t.Fatalf("executed %q, want %q", executed, want)
	}
}
