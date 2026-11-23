package cubrid

import (
	"encoding/binary"
	"errors"
	"math"
	"net"
	"testing"
)

func TestCheckRequestLength(t *testing.T) {
	const header = SizeDataLength + SizeCASInfo
	for _, packetLen := range []int64{header, header + 1, header + math.MaxInt32} {
		if err := checkRequestLength(packetLen); err != nil {
			t.Errorf("checkRequestLength(%d) = %v, want nil", packetLen, err)
		}
	}
	for _, packetLen := range []int64{header + math.MaxInt32 + 1, header + math.MaxUint32 + 1} {
		err := checkRequestLength(packetLen)
		var pe *ProgrammingError
		if !errors.As(err, &pe) {
			t.Errorf("checkRequestLength(%d) = %v, want *ProgrammingError", packetLen, err)
		}
	}
}

// countingConn counts Write calls and bytes accepted by the transport.
type countingConn struct {
	net.Conn
	writes, written int
}

func (c *countingConn) Write(data []byte) (int, error) {
	c.writes++
	n, err := c.Conn.Write(data)
	c.written += n
	return n, err
}

func TestRoundTripRejectsOversizedRequestWithoutWriting(t *testing.T) {
	small := WriteCheckCas([SizeCASInfo]byte{1})
	oldLimit := maxRequestPayload
	maxRequestPayload = int64(len(small) - SizeDataLength - SizeCASInfo)
	defer func() { maxRequestPayload = oldLimit }()

	client, server := net.Pipe()
	defer server.Close()
	tracked := &countingConn{Conn: client}
	c := &conn{socket: tracked}
	c.casInfo[0] = 1

	oversized := WritePrepareAndExecute("SELECT 1", false, c.casInfo)
	_, err := c.roundTrip(oversized)
	var pe *ProgrammingError
	if !errors.As(err, &pe) {
		t.Fatalf("oversized request: got %v, want *ProgrammingError", err)
	}
	if tracked.writes != 0 || tracked.written != 0 {
		t.Fatalf("oversized request reached the socket: writes=%d bytes=%d", tracked.writes, tracked.written)
	}
	if c.closed {
		t.Fatal("rejecting an oversized request must not retire the connection")
	}

	// The same connection still completes a request within the limit.
	served := make(chan error, 1)
	go func() {
		if _, err := readRequestFunc(server); err != nil {
			served <- err
			return
		}
		resp := make([]byte, SizeDataLength+SizeCASInfo+SizeInt) // body: response code 0
		binary.BigEndian.PutUint32(resp, SizeInt)
		_, err := server.Write(resp)
		served <- err
	}()
	resp, err := c.roundTrip(small)
	if err != nil {
		t.Fatalf("follow-up request on the same connection: %v", err)
	}
	if err := <-served; err != nil {
		t.Fatalf("fake CAS: %v", err)
	}
	if len(resp) != SizeCASInfo+SizeInt || tracked.written != len(small) {
		t.Fatalf("follow-up round trip: resp=%d bytes, written=%d, want %d", len(resp), tracked.written, len(small))
	}
}
