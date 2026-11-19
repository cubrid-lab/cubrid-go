package cubrid

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
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

// probeRaceConn lets the cancel watcher expire the socket deadline just before
// the CHECK_CAS probe installs its own deadline. The first SetDeadline call is
// the probe's: it cancels the request and waits until the watcher's expiry
// (the second call) has been applied, then applies the probe deadline.
type probeRaceConn struct {
	net.Conn
	cancel  context.CancelFunc
	calls   atomic.Int32
	expired chan struct{}
}

func (p *probeRaceConn) SetDeadline(t time.Time) error {
	switch p.calls.Add(1) {
	case 1:
		p.cancel()
		select {
		case <-p.expired:
		case <-time.After(2 * time.Second):
		}
	case 2:
		err := p.Conn.SetDeadline(t)
		close(p.expired)
		return err
	}
	return p.Conn.SetDeadline(t)
}

// TestCancelBeforeProbeDeadlineReturnsPromptly verifies that a cancellation
// is not lost when the cancel watcher expires the socket just before the
// CHECK_CAS probe sets its deadline, and the CAS never answers. The request
// must return context.Canceled at once instead of waiting for the probe
// deadline (the DSN timeout), or forever when the DSN sets timeout=0 (#39).
func TestCancelBeforeProbeDeadlineReturnsPromptly(t *testing.T) {
	for name, timeout := range map[string]time.Duration{
		"default timeout": 30 * time.Second,
		"no timeout":      0,
	} {
		t.Run(name, func(t *testing.T) {
			client, server := net.Pipe() // the CAS never reads the probe
			defer func() { _ = server.Close() }()
			// If the fix regresses, the request goroutine stays blocked in
			// the probe write; closing the client end releases it.
			t.Cleanup(func() { _ = client.Close() })

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sock := &probeRaceConn{Conn: client, cancel: cancel, expired: make(chan struct{})}
			c := &conn{socket: sock, timeout: timeout, autoCommit: true, protoVer: ProtoVersion}

			done := make(chan error, 1)
			go func() {
				_, err := c.ExecContext(ctx, "UPDATE t SET v = 1", nil)
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected context.Canceled, got %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("canceled request waited for the CHECK_CAS probe deadline")
			}
			if sock.calls.Load() < 2 {
				t.Fatalf("the watcher never expired the socket before the probe")
			}
			if !c.closed {
				t.Fatalf("connection must be unusable after an interrupted probe")
			}
		})
	}
}

// TestCancelDuringReconnectDial verifies that canceling a request while the
// driver reconnects to a broker that never answers, after a failed CHECK_CAS,
// returns context.Canceled promptly even with timeout=0, and that the cancel
// watcher neither crashes on a connection that has no socket yet nor races
// with the reconnect that installs the new socket (#39).
func TestCancelDuringReconnectDial(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		nc, err := l.Accept()
		if err == nil {
			accepted <- nc // hold the broker connection open and never answer
		}
	}()

	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	go func() {
		if _, err := readRequestFunc(server); err == nil {
			_ = server.Close() // fail the probe so the driver reconnects
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &conn{socket: client, host: "127.0.0.1", port: l.Addr().(*net.TCPAddr).Port,
		autoCommit: true, protoVer: ProtoVersion} // timeout=0: no handshake bound

	done := make(chan error, 1)
	go func() {
		_, err := c.ExecContext(ctx, "UPDATE t SET v = 1", nil)
		done <- err
	}()
	select {
	case nc := <-accepted:
		defer func() { _ = nc.Close() }()
	case <-time.After(2 * time.Second):
		t.Fatal("driver did not reconnect after a failed CHECK_CAS")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request waited for the broker handshake")
	}
}

// openDatabaseReply is a CAS reply to OpenDatabase for session id session.
func openDatabaseReply(session int32) []byte {
	w := newPacketWriter()
	w.writeInt(0)
	broker := make([]byte, SizeBrokerInfo)
	broker[4] = ProtoVersion
	w.writeRawBytes(broker)
	w.writeInt(session)
	return outTranPacket(w.toBytes())
}

// TestCanceledReconnectHandshakeRetiresConn verifies that a request canceled
// while a reconnect waits for the OpenDatabase reply leaves the connection
// unusable. The reply may still arrive on that socket; if the connection were
// reused, the next request would read it as the answer to its own request.
// The next request must return driver.ErrBadConn without sending anything.
func TestCanceledReconnectHandshakeRetiresConn(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	opened := make(chan struct{})
	release := make(chan struct{})
	extra := make(chan bool, 1)
	go func() {
		nc, err := l.Accept()
		if err != nil {
			return
		}
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
		close(opened)
		<-release
		_, _ = nc.Write(openDatabaseReply(2)) // the late reply to the canceled handshake
		_ = nc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, err = readRequestFunc(nc)
		extra <- err == nil
	}()

	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	go func() {
		if _, err := readRequestFunc(server); err == nil {
			_ = server.Close() // fail the probe so the driver reconnects
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &conn{socket: client, host: "127.0.0.1", port: l.Addr().(*net.TCPAddr).Port,
		autoCommit: true, protoVer: ProtoVersion}

	done := make(chan error, 1)
	go func() {
		_, err := c.ExecContext(ctx, "UPDATE t SET v = 1", nil)
		done <- err
	}()
	select {
	case <-opened:
	case <-time.After(2 * time.Second):
		t.Fatal("driver did not reconnect after a failed CHECK_CAS")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request waited for the OpenDatabase reply")
	}
	close(release)
	if !c.closed {
		t.Fatalf("connection must be unusable after an interrupted handshake")
	}

	next := make(chan error, 1)
	go func() {
		_, err := c.ExecContext(context.Background(), "UPDATE t SET v = 2", nil)
		next <- err
	}()
	select {
	case err := <-next:
		if !errors.Is(err, driver.ErrBadConn) {
			t.Fatalf("next request: got %v, want driver.ErrBadConn", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("next request on the interrupted connection did not return")
	}
	if <-extra {
		t.Fatalf("a request was sent on the interrupted connection")
	}
}

// TestSetSocketExpiresWhenAlreadyCanceled verifies that a socket installed
// after the request was canceled starts expired: the cancel watcher may have
// run while the connection had no socket, so it could not expire this one.
func TestSetSocketExpiresWhenAlreadyCanceled(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()

	c := &conn{}
	c.canceled.Store(true)
	c.setSocket(client)
	written := make(chan error, 1)
	go func() {
		_, err := client.Write([]byte{0}) // nobody reads: only a deadline ends it
		written <- err
	}()
	var err error
	select {
	case err = <-written:
	case <-time.After(time.Second):
		t.Fatal("socket installed after cancellation was not expired")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("write on a socket installed after cancellation: got %v, want a timeout", err)
	}
}

// lateCancelConn delays the cancel watcher's expiry (the only non-zero
// deadline in these tests) until the request path clears the deadline, or
// 200ms have passed. This makes the watcher fire as late as possible: after
// the request finished its I/O, and after endRequest if that runs first.
type lateCancelConn struct {
	net.Conn
	once    sync.Once
	cleared chan struct{}
}

func (l *lateCancelConn) SetDeadline(t time.Time) error {
	if t.IsZero() {
		err := l.Conn.SetDeadline(t)
		l.once.Do(func() { close(l.cleared) })
		return err
	}
	select {
	case <-l.cleared:
	case <-time.After(200 * time.Millisecond):
	}
	return l.Conn.SetDeadline(t)
}

// TestLateCancelDoesNotLeakIntoNextRequest verifies that a cancellation
// arriving after a request has finished its I/O does not affect the next
// request on the same connection. The request returns context.Canceled, but
// the connection stays usable, and a second request with a fresh context must
// succeed: the cancellation flag must be cleared and the watcher's expired
// deadline must not survive into the next request. The subtests cover a next
// request that probes the CAS first (reply OUT_TRAN) and one that writes at
// once (reply IN_TRAN).
func TestLateCancelDoesNotLeakIntoNextRequest(t *testing.T) {
	for name, status := range map[string]byte{"next request probes": 0, "next request in transaction": 1} {
		t.Run(name, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = server.Close() }()
			t.Cleanup(func() { _ = client.Close() })

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			withStatus := func(p []byte) []byte {
				p[SizeDataLength] = status
				return p
			}
			go func() {
				first := true
				for {
					fc, err := readRequestFunc(server)
					if err != nil {
						return
					}
					reply := withStatus(simpleResponsePacket(0))
					switch fc {
					case FuncCheckCas:
						reply = withStatus(emptyCheckCasResponse())
					case FuncPrepareAndExecute:
						reply = withStatus(prepareAndExecuteResponsePacket(StmtUpdate, 1, 1, 0))
						if first {
							first = false
							cancel() // the reply is already on its way
						}
					}
					if _, err := server.Write(reply); err != nil {
						return
					}
				}
			}()

			c := &conn{socket: &lateCancelConn{Conn: client, cleared: make(chan struct{})},
				autoCommit: true, protoVer: ProtoVersion}
			c.casInfo[0] = 1 // the first request does not probe
			if _, err := c.ExecContext(ctx, "UPDATE t SET v = 1", nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("first request: got %v, want context.Canceled", err)
			}
			if c.closed {
				t.Fatalf("a cancellation after the reply must not retire the connection")
			}

			done := make(chan error, 1)
			go func() {
				_, err := c.ExecContext(context.Background(), "UPDATE t SET v = 2", nil)
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("second request on the same connection: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("second request did not return")
			}
		})
	}
}
