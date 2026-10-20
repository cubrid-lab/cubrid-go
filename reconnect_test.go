package cubrid

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// readRequestFunc reads one framed request and returns its function code.
func readRequestFunc(conn net.Conn) (byte, error) {
	lenBuf := make([]byte, SizeDataLength)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return 0, err
	}
	payload := make([]byte, int(binary.BigEndian.Uint32(lenBuf))+SizeCASInfo)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return 0, err
	}
	if len(payload) <= SizeCASInfo {
		return 0, errors.New("request has no function code")
	}
	return payload[SizeCASInfo], nil
}

// outOfTran rewrites a response packet's CAS_INFO status to OUT_TRAN (0), as
// the CAS reports after an autocommitted statement.
func outOfTran(packet []byte) []byte {
	packet[SizeDataLength] = 0
	return packet
}

// emptyCheckCasResponse is a CHECK_CAS success reply with no body.
func emptyCheckCasResponse() []byte {
	var casInfo [SizeCASInfo]byte
	return buildProtocolHeader(0, casInfo)
}

// lastInsertIDResponsePacket builds the PREPARE_AND_EXECUTE reply for
// `SELECT LAST_INSERT_ID()`: one NUMERIC column and one row holding id.
func lastInsertIDResponsePacket(id int64) []byte {
	value := strconv.FormatInt(id, 10)
	w := newPacketWriter()
	w.writeInt(2)           // query handle
	w.writeInt(0)           // result cache lifetime
	w.writeByte(StmtSelect) // statement type
	w.writeInt(0)           // bind count
	w.writeByte(0)          // is_updatable
	w.writeInt(1)           // column count
	w.writeByte(TypeNumeric)
	w.writeShort(0)
	w.writeInt(38)
	w.writeNullTermString("last_insert_id()")
	w.writeNullTermString("")
	w.writeNullTermString("")
	w.writeByte(1)
	w.writeNullTermString("")
	for i := 0; i < 7; i++ {
		w.writeByte(0)
	}
	w.writeInt(1) // total tuple count
	w.writeByte(0)
	w.writeInt(0) // result-info count
	w.writeByte(0)
	w.writeInt(0) // shard_id
	w.writeInt(0) // fetch_code
	w.writeInt(1) // tuple count
	w.writeInt(0) // row index
	w.writeRawBytes(make([]byte, SizeOID))
	w.writeNullTermString(value) // column value size + NUMERIC text
	body := w.toBytes()

	var casInfo [SizeCASInfo]byte
	return append(buildProtocolHeader(len(body), casInfo), body...)
}

// TestExecInsertKeepsSessionForLastInsertID verifies that an autocommitted
// INSERT (reply status OUT_TRAN) does not make the driver open a new CAS
// session before reading LAST_INSERT_ID(): it probes with CHECK_CAS on the
// same socket and reads the generated id there (issue #26).
func TestExecInsertKeepsSessionForLastInsertID(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	c := &conn{socket: client, autoCommit: true, protoVer: ProtoVersion}
	c.casInfo[0] = 1

	replies := [][]byte{
		outOfTran(prepareAndExecuteResponsePacket(StmtInsert, 1, 1, 0)), // INSERT
		lastInsertIDResponsePacket(42),                                  // SELECT LAST_INSERT_ID()
		outOfTran(simpleResponsePacket(0)),                              // close id handle
		outOfTran(simpleResponsePacket(0)),                              // close insert handle
	}
	codes := make(chan []byte, 1)
	go func() {
		var seen []byte
		defer func() { codes <- seen }()
		for len(replies) > 0 {
			fc, err := readRequestFunc(server)
			if err != nil {
				return
			}
			seen = append(seen, fc)
			if fc == FuncCheckCas {
				_, _ = server.Write(emptyCheckCasResponse())
				continue
			}
			_, _ = server.Write(replies[0])
			replies = replies[1:]
		}
	}()

	res, err := c.ExecContext(context.Background(), "INSERT INTO t (v) VALUES (1)", nil)
	if err != nil {
		t.Fatalf("ExecContext error: %v", err)
	}
	lastID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId error: %v", err)
	}
	if lastID != 42 {
		t.Fatalf("expected lastInsertId=42, got %d", lastID)
	}
	if c.socket != client {
		t.Fatalf("the CAS session was replaced; expected the original socket")
	}

	var got []byte
	select {
	case got = <-codes:
	case <-time.After(time.Second):
		t.Fatal("fake CAS did not finish")
	}
	want := []byte{
		FuncPrepareAndExecute, FuncCheckCas, FuncPrepareAndExecute,
		FuncCheckCas, FuncCloseReqHandle, FuncCheckCas, FuncCloseReqHandle,
	}
	if string(got) != string(want) {
		t.Fatalf("request function codes = %v, want %v", got, want)
	}
}

func TestCheckReconnectSkipsProbeInTransaction(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	c := &conn{socket: client}
	c.casInfo[0] = 1
	if err := c.checkReconnect(); err != nil {
		t.Fatalf("checkReconnect error: %v", err)
	}
	if c.socket != client {
		t.Fatalf("socket replaced while IN_TRAN")
	}
}

// TestCheckReconnectReconnectsWhenProbeFails verifies that an OUT_TRAN CAS
// that does not answer CHECK_CAS successfully is replaced by a new session.
func TestCheckReconnectReconnectsWhenProbeFails(t *testing.T) {
	cases := map[string]func(net.Conn){
		"socket closed": func(server net.Conn) { _ = server.Close() },
		"error code": func(server net.Conn) {
			w := newPacketWriter()
			w.writeInt(-1)
			w.writeInt(-1)
			w.writeRawBytes([]byte("cas down\x00"))
			body := w.toBytes()
			var casInfo [SizeCASInfo]byte
			_, _ = server.Write(append(buildProtocolHeader(len(body), casInfo), body...))
		},
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			port, dialed := fakeBroker(t)

			client, server := net.Pipe()
			defer func() { _ = server.Close() }()
			go func() {
				fc, err := readRequestFunc(server)
				if err != nil || fc != FuncCheckCas {
					_ = server.Close()
					return
				}
				reply(server)
			}()

			c := &conn{
				socket:  client,
				host:    "127.0.0.1",
				port:    port,
				timeout: time.Second,
			}
			if err := c.checkReconnect(); err == nil {
				t.Fatalf("expected the failed handshake to surface as an error")
			}
			select {
			case <-dialed:
			case <-time.After(time.Second):
				t.Fatal("driver did not reconnect after a failed CHECK_CAS")
			}
		})
	}
}

func TestParseCheckCas(t *testing.T) {
	if err := ParseCheckCas(emptyCheckCasResponse()[SizeDataLength:]); err != nil {
		t.Fatalf("empty body: %v", err)
	}
	if err := ParseCheckCas(simpleResponsePacket(0)[SizeDataLength:]); err != nil {
		t.Fatalf("zero code: %v", err)
	}
	w := newPacketWriter()
	w.writeInt(-1)
	w.writeInt(-1)
	w.writeRawBytes([]byte("cas down\x00"))
	body := w.toBytes()
	var casInfo [SizeCASInfo]byte
	pkt := append(buildProtocolHeader(len(body), casInfo), body...)
	if err := ParseCheckCas(pkt[SizeDataLength:]); err == nil {
		t.Fatalf("expected an error for a negative code")
	}
}

// fakeBroker listens for a reconnect and reports whether one was dialed. The
// accepted connection is closed at once, failing the handshake.
func fakeBroker(t *testing.T) (port int, dialed <-chan struct{}) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	ch := make(chan struct{}, 1)
	go func() {
		nc, err := l.Accept()
		if err != nil {
			return
		}
		ch <- struct{}{}
		_ = nc.Close()
	}()
	return l.Addr().(*net.TCPAddr).Port, ch
}

// TestExecCanceledDuringProbeSendsNoSQL verifies that canceling the request
// while CHECK_CAS is pending neither reconnects nor sends the SQL.
func TestExecCanceledDuringProbeSendsNoSQL(t *testing.T) {
	port, dialed := fakeBroker(t)
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	extra := make(chan bool, 1)
	go func() {
		fc, err := readRequestFunc(server)
		if err != nil || fc != FuncCheckCas {
			extra <- true
			return
		}
		cancel() // never answer the probe
		_ = server.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, err = readRequestFunc(server)
		extra <- err == nil
	}()

	c := &conn{socket: client, host: "127.0.0.1", port: port, timeout: 5 * time.Second,
		autoCommit: true, protoVer: ProtoVersion}
	_, err := c.ExecContext(ctx, "UPDATE t SET v = 1", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if !c.closed {
		t.Fatalf("connection must be unusable after an interrupted probe")
	}
	if <-extra {
		t.Fatalf("a request was sent after the canceled probe")
	}
	select {
	case <-dialed:
		t.Fatalf("driver reconnected after cancellation")
	default:
	}
}

// TestCheckReconnectStalledProbeReconnects verifies that a CAS which keeps the
// socket open but never answers CHECK_CAS is bounded by the connection timeout
// and replaced.
func TestCheckReconnectStalledProbeReconnects(t *testing.T) {
	port, dialed := fakeBroker(t)
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	go func() { _, _ = readRequestFunc(server) }() // read the probe, never answer

	c := &conn{socket: client, host: "127.0.0.1", port: port, timeout: 200 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- c.checkReconnect() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("expected the failed reconnect handshake to surface as an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CHECK_CAS probe was not bounded by the connection timeout")
	}
	select {
	case <-dialed:
	case <-time.After(time.Second):
		t.Fatal("driver did not reconnect after a stalled CHECK_CAS")
	}
}

// TestCheckCASRestoresRequestDeadline verifies that the probe uses the earlier
// of the connection timeout and the request deadline, then restores the
// request deadline.
func TestCheckCASRestoresRequestDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	go func() {
		if _, err := readRequestFunc(server); err == nil {
			_, _ = server.Write(emptyCheckCasResponse())
		}
	}()

	tracked := &deadlineTrackingConn{Conn: client}
	c := &conn{socket: tracked, timeout: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if err := c.applyContextDeadline(ctx); err != nil {
		t.Fatalf("applyContextDeadline: %v", err)
	}
	requestDeadline, _ := ctx.Deadline()

	if err := c.checkReconnect(); err != nil {
		t.Fatalf("checkReconnect: %v", err)
	}
	calls := tracked.deadlineCalls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 SetDeadline calls (request, probe, restore), got %d", len(calls))
	}
	if !calls[1].Before(requestDeadline) || time.Until(calls[1]) > time.Second {
		t.Fatalf("probe deadline %v not bounded by the connection timeout", calls[1])
	}
	if !calls[2].Equal(requestDeadline) {
		t.Fatalf("request deadline not restored: got %v, want %v", calls[2], requestDeadline)
	}

	c.endRequest()
	if c.reqCtx != nil || !c.reqDeadline.IsZero() {
		t.Fatalf("endRequest did not clear the request state")
	}
}
