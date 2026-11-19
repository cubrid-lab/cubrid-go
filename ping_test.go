package cubrid

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests for #88: conn implements driver.Pinger, so db.Ping and
// db.PingContext send GET_DB_VERSION on a pooled connection.

// pingSessionLog fails unless session s of f received exactly want.
func pingSessionLog(t *testing.T, f *fakeCAS, s string, want ...string) {
	t.Helper()
	if got := sessionLog(f.entries(), s); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("session %s received %q, want %q", s, got, want)
	}
}

// TestDBPingContextSendsGetDbVersionOnPooledConn pings twice through a
// one-connection pool: the first ping opens the connection, the second
// reuses it, and both reach the CAS as GET_DB_VERSION on the same session.
func TestDBPingContextSendsGetDbVersionOnPooledConn(t *testing.T) {
	f := newFakeCAS(t)
	db, err := sql.Open("cubrid", f.dsn())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	for i := 0; i < 2; i++ {
		if err := db.PingContext(context.Background()); err != nil {
			t.Fatalf("PingContext %d: %v", i+1, err)
		}
	}
	if n := db.Stats().OpenConnections; n != 1 {
		t.Fatalf("OpenConnections = %d, want 1", n)
	}
	pingSessionLog(t, f, "s1", "s1 GET_DB_VERSION", "s1 GET_DB_VERSION")
}

// TestDBPingContextReconnectsDroppedSession: when the CAS dropped an idle
// pooled session but the broker is up, the ping's CHECK_CAS probe fails and
// the connection reconnects, like any other request, so the ping succeeds
// on the new session.
func TestDBPingContextReconnectsDroppedSession(t *testing.T) {
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

	f.restart()
	if err := db.Ping(); err != nil {
		t.Fatalf("Ping after the session was dropped: %v", err)
	}
	pingSessionLog(t, f, "s2", "s2 GET_DB_VERSION")
}

// TestPingDeadSessionReturnsErrBadConn checks the driver's answer when the
// session is dead and cannot be replaced: driver.ErrBadConn, which tells
// database/sql to discard the connection, and the connection is retired.
func TestPingDeadSessionReturnsErrBadConn(t *testing.T) {
	for _, tc := range []struct {
		name string
		// inTran marks the session as in a transaction, so no CHECK_CAS
		// probe runs and the GET_DB_VERSION reply itself is lost; otherwise
		// the probe fails and the reconnect cannot reach the broker.
		inTran bool
	}{
		{name: "probe fails and broker is down", inTran: false},
		{name: "reply lost in transaction", inTran: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCAS(t)
			dc, err := (&Driver{}).Open(f.dsn())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			c := dc.(*conn)
			defer func() { _ = c.Close() }()

			if tc.inTran {
				c.casInfo[0] = 1 // IN_TRAN
			} else {
				_ = f.l.Close()
			}
			f.restart()

			if err := c.Ping(context.Background()); err != driver.ErrBadConn {
				t.Fatalf("Ping on a dead session = %v, want driver.ErrBadConn", err)
			}
			if !c.closed {
				t.Fatalf("the connection was not retired")
			}
			if err := c.Ping(context.Background()); err != driver.ErrBadConn {
				t.Fatalf("Ping on a retired connection = %v, want driver.ErrBadConn", err)
			}
		})
	}
}

// TestDBPingContextDiscardsDeadConn: when the broker is down, the ping on
// the pooled connection returns driver.ErrBadConn. database/sql returns that
// error from PingContext and closes the connection instead of returning it
// to the pool.
func TestDBPingContextDiscardsDeadConn(t *testing.T) {
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

	_ = f.l.Close()
	f.restart()
	if err := db.PingContext(context.Background()); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("PingContext with the broker down = %v, want driver.ErrBadConn", err)
	}
	if n := db.Stats().OpenConnections; n != 0 {
		t.Fatalf("OpenConnections = %d after a failed ping, want 0: the dead connection was kept", n)
	}
}

// holdGetDbVersion makes f hold its reply to the first GET_DB_VERSION until
// the test ends. arrived is closed when the request arrives.
func holdGetDbVersion(t *testing.T, f *fakeCAS) (arrived <-chan struct{}) {
	got := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var once sync.Once
	f.beforeReply = func(fc byte, _ net.Conn) bool {
		held := false
		if fc == FuncGetDbVersion {
			once.Do(func() { held = true })
		}
		if !held {
			return true
		}
		close(got)
		<-release
		return false
	}
	return got
}

// TestPingContextCanceled checks that db.PingContext honors its context:
// an already canceled context sends nothing, and canceling, or reaching the
// deadline, while the CAS does not answer returns promptly with the
// context's error. The interrupted connection is retired and never sends
// another request; the next statement runs on a new session.
func TestPingContextCanceled(t *testing.T) {
	t.Run("already canceled", func(t *testing.T) {
		f := newFakeCAS(t)
		dc, err := (&Driver{}).Open(f.dsn())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		c := dc.(*conn)
		defer func() { _ = c.Close() }()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := c.Ping(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Ping = %v, want context.Canceled", err)
		}
		pingSessionLog(t, f, "s1")
	})

	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		// cancelOnArrival cancels the context once the request reaches the
		// CAS; otherwise its deadline ends it.
		cancelOnArrival bool
		want            error
	}{
		{name: "canceled in flight", cancelOnArrival: true, want: context.Canceled,
			ctx: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }},
		{name: "deadline in flight", want: context.DeadlineExceeded,
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 200*time.Millisecond)
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
			if _, err := db.Exec("UPDATE t SET v = 1"); err != nil { // open the pooled connection
				t.Fatalf("Exec: %v", err)
			}
			arrived := holdGetDbVersion(t, f)

			ctx, cancel := tc.ctx()
			defer cancel()
			start := time.Now()
			done := make(chan error, 1)
			go func() { done <- db.PingContext(ctx) }()
			select {
			case <-arrived:
			case <-time.After(3 * time.Second):
				t.Fatalf("GET_DB_VERSION never reached the CAS")
			}
			if tc.cancelOnArrival {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, tc.want) {
					t.Fatalf("PingContext = %v, want %v", err, tc.want)
				}
				if d := time.Since(start); d > time.Second {
					t.Fatalf("PingContext returned after %v", d)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("PingContext did not return after its context ended")
			}

			// database/sql retries Exec on driver.ErrBadConn from the
			// retired connection.
			if _, err := db.Exec("UPDATE t SET v = 2"); err != nil {
				t.Fatalf("Exec after the interrupted ping: %v", err)
			}
			pingSessionLog(t, f, "s1", "s1 PREPARE_AND_EXECUTE UPDATE t SET v = 1",
				"s1 CLOSE_REQ UPDATE t SET v = 1", "s1 GET_DB_VERSION")
			pingSessionLog(t, f, "s2", "s2 PREPARE_AND_EXECUTE UPDATE t SET v = 2",
				"s2 CLOSE_REQ UPDATE t SET v = 2")
		})
	}
}
