package cubrid

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
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
