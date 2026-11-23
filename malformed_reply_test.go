package cubrid

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for issue #102: every size and count in a CAS reply is checked
// against the bytes the reply holds before it is used, and a reply that
// fails the check returns an error and retires the connection instead of
// panicking or allocating what the header claims.

const frameHeader = SizeDataLength + SizeCASInfo

// withBodyInt returns a copy of the framed reply with the INT at body
// offset off (after DATA_LENGTH and CAS_INFO) set to v.
func withBodyInt(reply []byte, off int, v int32) []byte {
	out := append([]byte(nil), reply...)
	binary.BigEndian.PutUint32(out[frameHeader+off:], uint32(v))
	return out
}

// truncatedBody returns the framed reply cut to its first n body bytes, with
// DATA_LENGTH matching, so the frame itself is well formed.
func truncatedBody(reply []byte, n int) []byte {
	out := append([]byte(nil), reply[:frameHeader+n]...)
	binary.BigEndian.PutUint32(out, uint32(n))
	return out
}

// withStringColumn returns a copy of a framed PREPARE or PREPARE_AND_EXECUTE
// reply built from selectPrepareBody whose column is a VARCHAR instead of an
// INTEGER, so a row value's size field decides how many bytes are read.
func withStringColumn(reply []byte) []byte {
	out := append([]byte(nil), reply...)
	out[frameHeader+18] = TypeString
	return out
}

// oversizedFrame keeps the reply's CAS_INFO but declares a DATA_LENGTH of
// almost 4 GiB and sends no body.
func oversizedFrame(reply []byte) []byte {
	out := append([]byte(nil), reply[:frameHeader]...)
	binary.BigEndian.PutUint32(out, 0xFFFFFFF0)
	return out
}

func assertMalformed(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want a malformed-reply error")
	}
	if !strings.Contains(err.Error(), "malformed CAS reply") {
		t.Fatalf("error %q does not report a malformed CAS reply", err)
	}
}

func TestParsersRejectMalformedReplies(t *testing.T) {
	cols := []ColumnMetaData{{ColumnType: TypeString, Name: "v"}}
	prep := outTranPacket(selectPrepareBody(1))
	pae := outTranPacket(oneRowSelectBody(1, 1))
	p := len(selectPrepareBody(1)) // EXECUTE part of a PREPARE_AND_EXECUTE reply starts here
	exec := outTranPacket(selectExecuteBody(1))
	fetch := outTranPacket(oneRowFetchBody())
	upd := outTranPacket(oneRowAffectedBody(StmtUpdate))
	lastID := func(code int32) []byte {
		w := newPacketWriter()
		w.writeInt(code)
		w.writeRawBytes([]byte("42\x00"))
		return outTranPacket(w.toBytes())
	}
	errReply := func(msgLen int) []byte {
		w := newPacketWriter()
		w.writeInt(-1)
		w.writeInt(-493)
		w.writeRawBytes(make([]byte, msgLen))
		return outTranPacket(w.toBytes())
	}
	data := func(reply []byte) []byte { return reply[SizeDataLength:] }
	prepare := func(reply []byte) error { _, err := ParsePrepare(data(reply)); return err }
	prepAndExec := func(reply []byte) error { _, err := ParsePrepareAndExecute(data(reply), ProtoVersion); return err }
	execute := func(reply []byte) error {
		_, err := ParseExecute(data(reply), cols, StmtSelect, ProtoVersion)
		return err
	}
	executeUpdate := func(reply []byte) error {
		_, err := ParseExecute(data(reply), nil, StmtUpdate, ProtoVersion)
		return err
	}
	fetchRows := func(reply []byte) error { _, err := ParseFetch(data(reply), cols, StmtSelect); return err }

	for _, tc := range []struct {
		name  string
		parse func([]byte) error
		reply []byte
	}{
		{"PREPARE negative column count", prepare, withBodyInt(prep, 14, -1)},
		{"PREPARE column count beyond reply", prepare, withBodyInt(prep, 14, math.MaxInt32)},
		{"PREPARE name length beyond reply", prepare, withBodyInt(prep, 25, 1<<30)},
		{"PREPARE truncated", prepare, truncatedBody(prep, 20)},
		{"PREPARE_AND_EXECUTE negative column count", prepAndExec, withBodyInt(pae, 14, -1)},
		{"PREPARE_AND_EXECUTE negative result count", prepAndExec, withBodyInt(pae, p+5, -1)},
		{"PREPARE_AND_EXECUTE result count beyond reply", prepAndExec, withBodyInt(pae, p+5, math.MaxInt32)},
		{"PREPARE_AND_EXECUTE negative tuple count", prepAndExec, withBodyInt(pae, p+18, -1)},
		{"PREPARE_AND_EXECUTE tuple count beyond reply", prepAndExec, withBodyInt(pae, p+18, math.MaxInt32)},
		{"PREPARE_AND_EXECUTE value size beyond reply", prepAndExec, withBodyInt(withStringColumn(pae), p+34, 1<<30)},
		{"PREPARE_AND_EXECUTE truncated value", prepAndExec, truncatedBody(pae, p+36)},
		{"EXECUTE negative result count", executeUpdate, withBodyInt(upd, 5, -1)},
		{"EXECUTE result count beyond reply", executeUpdate, withBodyInt(upd, 5, math.MaxInt32)},
		{"EXECUTE negative tuple count", execute, withBodyInt(exec, 18, -1)},
		{"EXECUTE tuple count beyond reply", execute, withBodyInt(exec, 18, math.MaxInt32)},
		{"EXECUTE value size beyond reply", execute, withBodyInt(exec, 34, 1<<30)},
		{"FETCH negative tuple count", fetchRows, withBodyInt(fetch, 4, -1)},
		{"FETCH tuple count beyond reply", fetchRows, withBodyInt(fetch, 4, math.MaxInt32)},
		{"FETCH value size beyond reply", fetchRows, withBodyInt(fetch, 20, 1<<30)},
		{"FETCH empty body", fetchRows, truncatedBody(fetch, 0)},
		{"simple response empty body", func(r []byte) error { return ParseSimpleResponse(data(r)) }, truncatedBody(simpleResponsePacket(0), 2)},
		{"error reply without error code", func(r []byte) error { return ParseSimpleResponse(data(r)) }, truncatedBody(errReply(0), 6)},
		{"GET_DB_VERSION empty body", func(r []byte) error { _, err := ParseGetDbVersion(data(r)); return err }, truncatedBody(simpleResponsePacket(0), 0)},
		{"GET_LAST_INSERT_ID length beyond reply", func(r []byte) error { _, err := ParseGetLastInsertId(data(r)); return err }, lastID(1 << 30)},
		{"OpenDatabase truncated broker info", func(r []byte) error { _, err := ParseOpenDatabase(data(r)); return err }, truncatedBody(simpleResponsePacket(0), 4)},
		{"OpenDatabase empty body", func(r []byte) error { _, err := ParseOpenDatabase(data(r)); return err }, truncatedBody(simpleResponsePacket(0), 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.parse(tc.reply)
			if !errors.Is(err, errMalformedReply) {
				t.Fatalf("got %v, want an error wrapping errMalformedReply", err)
			}
			var oe *OperationalError
			if !errors.As(err, &oe) {
				t.Fatalf("got %T, want *OperationalError", err)
			}
		})
	}

	// A well-formed error reply is still the server's error, not a malformed one.
	err := ParseSimpleResponse(data(errReply(8)))
	if err == nil || errors.Is(err, errMalformedReply) {
		t.Fatalf("well-formed error reply: got %v, want the server error", err)
	}
	// GET_LAST_INSERT_ID whose length fits is unchanged.
	if id, err := ParseGetLastInsertId(data(lastID(3))); err != nil || id != "42" {
		t.Fatalf("GET_LAST_INSERT_ID = %q, %v; want \"42\", nil", id, err)
	}
}

// replyPipe returns a conn whose socket is one end of a pipe; the returned
// function writes b to the other end and then closes it.
func replyPipe(t *testing.T) (*conn, func(b []byte)) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return &conn{socket: client}, func(b []byte) {
		go func() {
			_, _ = server.Write(b)
			_ = server.Close()
		}()
	}
}

func TestRecvRejectsDataLengthAboveLimit(t *testing.T) {
	for _, dataLen := range []uint32{math.MaxInt32 + 1, math.MaxUint32} {
		c, send := replyPipe(t)
		hdr := make([]byte, frameHeader)
		binary.BigEndian.PutUint32(hdr, dataLen)
		send(hdr)
		_, err := c.recv()
		if !errors.Is(err, errMalformedReply) {
			t.Fatalf("DATA_LENGTH %d: got %v, want errMalformedReply", dataLen, err)
		}
	}

	old := maxResponseLength
	maxResponseLength = 16
	defer func() { maxResponseLength = old }()
	for _, tc := range []struct {
		dataLen int
		ok      bool
	}{{16, true}, {17, false}} {
		c, send := replyPipe(t)
		frame := make([]byte, frameHeader+tc.dataLen)
		binary.BigEndian.PutUint32(frame, uint32(tc.dataLen))
		send(frame)
		data, err := c.recv()
		if tc.ok && (err != nil || len(data) != SizeCASInfo+tc.dataLen) {
			t.Fatalf("DATA_LENGTH %d at limit: got %d bytes, %v", tc.dataLen, len(data), err)
		}
		if !tc.ok && !errors.Is(err, errMalformedReply) {
			t.Fatalf("DATA_LENGTH %d above lowered limit: got %v, want errMalformedReply", tc.dataLen, err)
		}
	}
}

// TestRecvAllocatesOnlyWhatArrives: a header that declares the largest
// allowed DATA_LENGTH but is followed by a few bytes must not make recv
// allocate the declared length.
func TestRecvAllocatesOnlyWhatArrives(t *testing.T) {
	c, send := replyPipe(t)
	frame := make([]byte, frameHeader+8)
	binary.BigEndian.PutUint32(frame, math.MaxInt32)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	send(frame)
	_, err := c.recv()
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatalf("recv of a truncated reply succeeded")
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<20 {
		t.Fatalf("recv allocated %d bytes for a reply of %d bytes", grew, len(frame))
	}
}

// TestRecvLargeReplyIntact: a reply larger than recvChunk is read in pieces
// and returned byte for byte.
func TestRecvLargeReplyIntact(t *testing.T) {
	c, send := replyPipe(t)
	body := make([]byte, 3*recvChunk+5)
	for i := range body {
		body[i] = byte(i * 7)
	}
	frame := append(buildProtocolHeader(len(body), [SizeCASInfo]byte{1, 2, 3, 4}), body...)
	send(frame)
	data, err := c.recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if !bytes.Equal(data, frame[SizeDataLength:]) {
		t.Fatalf("large reply was not returned intact (%d bytes, want %d)", len(data), len(frame)-SizeDataLength)
	}
}

// TestMalformedRepliesRetireConnection drives every request path against a
// fake CAS that answers one request with a malformed reply: a negative count
// or size, a count or size the reply cannot hold, a truncated body, or a
// DATA_LENGTH of almost 4 GiB. Each must return an error without panicking,
// and the connection must be retired: IsValid reports false and the next
// request returns driver.ErrBadConn without reaching the CAS.
func TestMalformedRepliesRetireConnection(t *testing.T) {
	p := len(selectPrepareBody(1)) // EXECUTE part of a PREPARE_AND_EXECUTE reply starts here
	bodyInt := func(off int, v int32) func([]byte) []byte {
		return func(r []byte) []byte { return withBodyInt(r, off, v) }
	}
	truncate := func(n int) func([]byte) []byte {
		return func(r []byte) []byte { return truncatedBody(r, n) }
	}
	ctx := context.Background()
	prepare := func(c *conn) error { _, err := c.PrepareContext(ctx, "SELECT v FROM t"); return err }
	query := func(c *conn) error { _, err := c.QueryContext(ctx, "SELECT v FROM t", nil); return err }
	stmtExec := func(c *conn) error {
		ds, err := c.Prepare("UPDATE t SET v = 1")
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		_, err = ds.(*stmt).ExecContext(ctx, nil)
		return err
	}
	stmtQuery := func(c *conn) error {
		ds, err := c.Prepare("SELECT v FROM t")
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		_, err = ds.(*stmt).QueryContext(ctx, nil)
		return err
	}
	fetch := func(c *conn) error {
		dr, err := c.QueryContext(ctx, "SELECT v FROM t", nil)
		if err != nil {
			t.Fatalf("QueryContext: %v", err)
		}
		dest := make([]driver.Value, 1)
		if err := dr.Next(dest); err != nil {
			t.Fatalf("first Next (inline row): %v", err)
		}
		return dr.Next(dest) // FETCH
	}
	commit := func(c *conn) error {
		dt, err := c.Begin()
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		return dt.Commit()
	}
	ping := func(c *conn) error { return c.Ping(ctx) }

	for _, tc := range []struct {
		name    string
		fc      byte
		mutate  func([]byte) []byte
		run     func(*conn) error
		badConn bool // the error must match driver.ErrBadConn (Ping)
		// strCol makes the result column a VARCHAR, so the value size
		// field decides how many bytes are read.
		strCol bool
	}{
		{name: "PREPARE negative column count", fc: FuncPrepare, mutate: bodyInt(14, -1), run: prepare},
		{name: "PREPARE column count beyond reply", fc: FuncPrepare, mutate: bodyInt(14, math.MaxInt32), run: prepare},
		{name: "PREPARE name length beyond reply", fc: FuncPrepare, mutate: bodyInt(25, 1<<30), run: prepare},
		{name: "PREPARE oversized DATA_LENGTH", fc: FuncPrepare, mutate: oversizedFrame, run: prepare},
		{name: "PREPARE_AND_EXECUTE negative column count", fc: FuncPrepareAndExecute, mutate: bodyInt(14, -1), run: query},
		{name: "PREPARE_AND_EXECUTE negative result count", fc: FuncPrepareAndExecute, mutate: bodyInt(p+5, -1), run: query},
		{name: "PREPARE_AND_EXECUTE result count beyond reply", fc: FuncPrepareAndExecute, mutate: bodyInt(p+5, math.MaxInt32), run: query},
		{name: "PREPARE_AND_EXECUTE negative tuple count", fc: FuncPrepareAndExecute, mutate: bodyInt(p+18, -1), run: query},
		{name: "PREPARE_AND_EXECUTE tuple count beyond reply", fc: FuncPrepareAndExecute, mutate: bodyInt(p+18, math.MaxInt32), run: query},
		{name: "PREPARE_AND_EXECUTE value size beyond reply", fc: FuncPrepareAndExecute, mutate: bodyInt(p+34, 1<<30), run: query, strCol: true},
		{name: "PREPARE_AND_EXECUTE oversized DATA_LENGTH", fc: FuncPrepareAndExecute, mutate: oversizedFrame, run: query},
		{name: "EXECUTE negative result count", fc: FuncExecute, mutate: bodyInt(5, -1), run: stmtExec},
		{name: "EXECUTE result count beyond reply", fc: FuncExecute, mutate: bodyInt(5, math.MaxInt32), run: stmtExec},
		{name: "EXECUTE oversized DATA_LENGTH", fc: FuncExecute, mutate: oversizedFrame, run: stmtExec},
		{name: "EXECUTE negative tuple count", fc: FuncExecute, mutate: bodyInt(18, -1), run: stmtQuery},
		{name: "EXECUTE tuple count beyond reply", fc: FuncExecute, mutate: bodyInt(18, math.MaxInt32), run: stmtQuery},
		{name: "EXECUTE value size beyond reply", fc: FuncExecute, mutate: bodyInt(34, 1<<30), run: stmtQuery, strCol: true},
		{name: "FETCH negative tuple count", fc: FuncFetch, mutate: bodyInt(4, -1), run: fetch},
		{name: "FETCH tuple count beyond reply", fc: FuncFetch, mutate: bodyInt(4, math.MaxInt32), run: fetch},
		{name: "FETCH value size beyond reply", fc: FuncFetch, mutate: bodyInt(20, 1<<30), run: fetch, strCol: true},
		{name: "FETCH oversized DATA_LENGTH", fc: FuncFetch, mutate: oversizedFrame, run: fetch},
		{name: "END_TRAN truncated", fc: FuncEndTran, mutate: truncate(2), run: commit},
		{name: "END_TRAN oversized DATA_LENGTH", fc: FuncEndTran, mutate: oversizedFrame, run: commit},
		{name: "GET_DB_VERSION truncated", fc: FuncGetDbVersion, mutate: truncate(0), run: ping, badConn: true},
		{name: "GET_DB_VERSION oversized DATA_LENGTH", fc: FuncGetDbVersion, mutate: oversizedFrame, run: ping, badConn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCAS(t)
			f.rowsTotal = 2
			f.answerFetch = true
			f.rewrite = func(fc byte, reply []byte) []byte {
				if tc.strCol && (fc == FuncPrepare || fc == FuncPrepareAndExecute) {
					reply = withStringColumn(reply)
				}
				if fc == tc.fc {
					return tc.mutate(reply)
				}
				return reply
			}
			dc, err := (&Driver{}).Open(f.dsn())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			c := dc.(*conn)
			defer func() { _ = c.Close() }()

			err = tc.run(c)
			assertMalformed(t, err)
			if tc.badConn {
				if !errors.Is(err, driver.ErrBadConn) {
					t.Fatalf("got %v, want an error matching driver.ErrBadConn", err)
				}
			} else {
				if errors.Is(err, driver.ErrBadConn) {
					t.Fatalf("got %v: driver.ErrBadConn after the request was sent could replay it", err)
				}
				var oe *OperationalError
				if !errors.As(err, &oe) {
					t.Fatalf("got %T (%v), want *OperationalError", err, err)
				}
			}
			if c.IsValid() {
				t.Fatalf("IsValid reports the connection that read a malformed reply as valid")
			}
			before := len(f.entries())
			if _, err := c.PrepareContext(ctx, "SELECT v FROM t"); !errors.Is(err, driver.ErrBadConn) {
				t.Fatalf("next request = %v, want driver.ErrBadConn", err)
			}
			if got := f.entries(); len(got) != before {
				t.Fatalf("the retired connection sent %q", got[before:])
			}
		})
	}
}

// TestMalformedOpenDatabaseReply: a malformed OpenDatabase reply fails
// Driver.Open with an error instead of a panic.
func TestMalformedOpenDatabaseReply(t *testing.T) {
	for name, mutate := range map[string]func([]byte) []byte{
		"truncated broker info": func(r []byte) []byte { return truncatedBody(r, 8) },
		"empty body":            func(r []byte) []byte { return truncatedBody(r, 0) },
		"oversized DATA_LENGTH": oversizedFrame,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeCAS(t)
			f.rewrite = func(fc byte, reply []byte) []byte {
				if fc == 0 {
					return mutate(reply)
				}
				return reply
			}
			dc, err := (&Driver{}).Open(f.dsn())
			if dc != nil {
				t.Fatalf("Open returned a connection for a malformed OpenDatabase reply")
			}
			assertMalformed(t, err)
		})
	}
}

// TestMalformedCloseReqReplyRetiresConnection: CLOSE_REQ_HANDLE is
// best-effort and its error is not reported, but a malformed reply still
// retires the connection.
func TestMalformedCloseReqReplyRetiresConnection(t *testing.T) {
	f := newFakeCAS(t)
	f.rewrite = func(fc byte, reply []byte) []byte {
		if fc == FuncCloseReqHandle {
			return truncatedBody(reply, 1)
		}
		return reply
	}
	dc, err := (&Driver{}).Open(f.dsn())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := dc.(*conn)
	defer func() { _ = c.Close() }()
	if _, err := c.ExecContext(context.Background(), "UPDATE t SET v = 1", nil); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	f.waitFor("s1 CLOSE_REQ UPDATE t SET v = 1")
	if c.IsValid() {
		t.Fatalf("IsValid reports the connection that read a malformed CLOSE_REQ reply as valid")
	}
}

// TestMalformedReplyConnectionLeavesPool: through database/sql, the query
// that read a malformed reply fails, its connection is discarded, and the
// next query runs on a new CAS session.
func TestMalformedReplyConnectionLeavesPool(t *testing.T) {
	f := newFakeCAS(t)
	var once atomic.Bool
	f.rewrite = func(fc byte, reply []byte) []byte {
		if fc == FuncPrepareAndExecute && once.CompareAndSwap(false, true) {
			return withBodyInt(reply, 14, -1)
		}
		return reply
	}
	db, err := sql.Open("cubrid", f.dsn())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var v int
	err = db.QueryRowContext(ctx, "SELECT v FROM t").Scan(&v)
	assertMalformed(t, err)
	if err := db.QueryRowContext(ctx, "SELECT v FROM t").Scan(&v); err != nil {
		t.Fatalf("second query: %v", err)
	}
	if v != 7 {
		t.Fatalf("second query = %d, want 7", v)
	}
	if got := sessionLog(f.entries(), "s2"); len(got) == 0 || got[0] != "s2 PREPARE_AND_EXECUTE SELECT v FROM t" {
		t.Fatalf("second query did not run on a new session; log: %q", f.entries())
	}
}

// FuzzParseReplies feeds arbitrary bytes, as the reply after DATA_LENGTH, to
// every reply parser. None may panic, and every error must be either a
// malformed-reply error or the server's own error.
func FuzzParseReplies(f *testing.F) {
	for _, body := range [][]byte{
		selectPrepareBody(1),
		oneRowSelectBody(1, 2),
		selectExecuteBody(2),
		oneRowFetchBody(),
		oneRowAffectedBody(StmtUpdate),
		append(noColumnPrepareBody(1, StmtInsert), oneRowAffectedBody(StmtInsert)...),
		{0, 0, 0, 0},
		{0xFF, 0xFF, 0xFF, 0xFF, 0, 0, 0x01, 0xED, 'e', 'r', 'r', 0},
	} {
		f.Add(outTranPacket(body)[SizeDataLength:])
	}
	cols := []ColumnMetaData{{ColumnType: TypeInt}, {ColumnType: TypeString}, {ColumnType: TypeNull}}
	check := func(t *testing.T, what string, err error) {
		if err == nil || errors.Is(err, errMalformedReply) {
			return
		}
		var ce *CubridError
		var ie *IntegrityError
		var pe *ProgrammingError
		if errors.As(err, &ce) || errors.As(err, &ie) || errors.As(err, &pe) {
			return
		}
		t.Fatalf("%s: unexpected error %T: %v", what, err, err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		res, err := ParsePrepare(data)
		check(t, "ParsePrepare", err)
		if err == nil {
			_, err = ParseExecute(data, res.Columns, StmtSelect, ProtoVersion)
			check(t, "ParseExecute(prepared columns)", err)
		}
		_, err = ParsePrepareAndExecute(data, ProtoVersion)
		check(t, "ParsePrepareAndExecute", err)
		_, err = ParseExecute(data, cols, StmtSelect, ProtoVersion)
		check(t, "ParseExecute", err)
		_, err = ParseExecute(data, cols, StmtCallSP, 1)
		check(t, "ParseExecute(call)", err)
		_, err = ParseFetch(data, cols, StmtSelect)
		check(t, "ParseFetch", err)
		_, err = ParseFetch(data, nil, StmtSelect)
		check(t, "ParseFetch(no columns)", err)
		check(t, "ParseSimpleResponse", ParseSimpleResponse(data))
		check(t, "ParseCheckCas", ParseCheckCas(data))
		_, err = ParseGetDbVersion(data)
		check(t, "ParseGetDbVersion", err)
		_, err = ParseGetLastInsertId(data)
		check(t, "ParseGetLastInsertId", err)
		_, err = ParseOpenDatabase(data)
		check(t, "ParseOpenDatabase", err)
	})
}
