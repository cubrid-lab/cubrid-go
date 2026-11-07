package cubrid

import (
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// injectedWriteConn reports exactly how many bytes a transport accepted.
// The partial-write cases exercise a real net.Pipe, so the peer observes
// the accepted prefix without receiving the remaining request bytes.
type injectedWriteConn struct {
	net.Conn
	accept  int
	failure error
	writes  int
}

func (c *injectedWriteConn) Write(data []byte) (int, error) {
	c.writes++
	if c.accept == 0 {
		return 0, c.failure
	}
	n, err := c.Conn.Write(data[:c.accept])
	if err != nil {
		return n, err
	}
	return n, c.failure
}

func TestRoundTripReadFailureAfterSentInsertMustNotBeRetried(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	c := &conn{socket: client}
	c.casInfo[0] = 1 // A transaction is active; no CHECK_CAS probe.

	sqlSent := make(chan byte, 1)
	go func() {
		fc, err := readRequestFunc(server)
		if err == nil {
			sqlSent <- fc
		}
		_ = server.Close() // CAS may have executed INSERT; reply is lost.
	}()

	request := WritePrepareAndExecute("INSERT INTO t VALUES (1)", false, c.casInfo)
	_, err := c.roundTrip(request)
	var operational *OperationalError
	if !errors.As(err, &operational) {
		t.Fatalf("reply lost after INSERT: got %v, want *OperationalError", err)
	}
	if errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("database/sql would replay an INSERT whose execution outcome is unknown: %v", err)
	}
	if !c.closed {
		t.Fatal("the ambiguous CAS session must be retired")
	}
	select {
	case fc := <-sqlSent:
		if fc != FuncPrepareAndExecute {
			t.Fatalf("CAS received function code %d, want PREPARE_AND_EXECUTE", fc)
		}
	case <-time.After(time.Second):
		t.Fatal("CAS never received the original INSERT")
	}

	// The broken session must not send a second copy of the SQL.
	if _, err = c.sendAndRecv(request); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("subsequent attempt on retired session: got %v, want bad connection", err)
	}
}

func TestRoundTripWriteFailureClassifiesByAcceptedBytes(t *testing.T) {
	tests := []struct {
		name          string
		accepted      int
		writeError    error
		wantSafeRetry bool
	}{
		{"nothing sent", 0, io.ErrClosedPipe, true},
		{"partially sent", 4, io.ErrUnexpectedEOF, false},
		{"short write without error", 4, nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer server.Close()
			tracked := &injectedWriteConn{
				Conn: client, accept: tc.accepted, failure: tc.writeError,
			}
			c := &conn{socket: tracked}
			c.casInfo[0] = 1

			seen := make(chan []byte, 1)
			if tc.accepted > 0 {
				go func() {
					got := make([]byte, tc.accepted)
					_, _ = io.ReadFull(server, got)
					// Close after the accepted prefix so a buggy short-write
					// implementation cannot hang forever waiting for a reply.
					_ = server.Close()
					seen <- got
				}()
			}

			request := WritePrepareAndExecute("INSERT INTO t VALUES (2)", false, c.casInfo)
			_, err := c.roundTrip(request)
			if errors.Is(err, driver.ErrBadConn) != tc.wantSafeRetry {
				t.Fatalf("accepted %d bytes: got %v, safe-retry classification should be %v",
					tc.accepted, err, tc.wantSafeRetry)
			}
			if !tc.wantSafeRetry {
				var operational *OperationalError
				if !errors.As(err, &operational) {
					t.Fatalf("accepted %d bytes: got %v, want *OperationalError", tc.accepted, err)
				}
			}
			if tracked.writes != 1 || !c.closed {
				t.Fatalf("wanted one attempted write and retired socket; writes=%d closed=%v",
					tracked.writes, c.closed)
			}
			if tc.accepted > 0 {
				select {
				case got := <-seen:
					if string(got) != string(request[:tc.accepted]) {
						t.Fatalf("unexpected bytes sent to CAS: %x", got)
					}
				case <-time.After(time.Second):
					t.Fatal("fake CAS did not receive the accepted prefix")
				}
			}
		})
	}
}
