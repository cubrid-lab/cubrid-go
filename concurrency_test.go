package cubrid

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// oneRowFetchBody is a FETCH reply body that carries one row of the
// one-INTEGER-column SELECT of selectPrepareBody.
func oneRowFetchBody() []byte {
	w := newPacketWriter()
	w.writeInt(0) // response code
	w.writeInt(1) // tuple count
	w.writeInt(1) // row index
	w.writeRawBytes(make([]byte, SizeOID))
	w.writeInt(4)
	w.writeInt(8)
	return w.toBytes()
}

// holdReply makes f hold its reply to the first request with function code
// fc for a moment and reports whether the client sent anything else on that
// socket in the meantime. started is closed when that request arrives.
func holdReply(f *fakeCAS, fc byte) (started <-chan struct{}, interleaved <-chan bool) {
	arrived := make(chan struct{})
	result := make(chan bool, 1)
	var once sync.Once
	f.beforeReply = func(got byte, nc net.Conn) bool {
		held := false
		if got == fc {
			once.Do(func() { held = true })
		}
		if !held {
			return true
		}
		close(arrived)
		_ = nc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		n, _ := nc.Read(make([]byte, 1))
		_ = nc.SetReadDeadline(time.Time{})
		result <- n > 0
		return n == 0 // if the request stream is out of step, drop the session
	}
	return arrived, result
}

// TestTxStmtCloseDoesNotInterleaveWithRowsFetch pins the database/sql side of
// the concurrency contract. A statement prepared on a Tx can be closed from
// one goroutine while another goroutine iterates rows of the same Tx, and
// both use one physical connection. database/sql makes that safe: it calls
// Rows.Next and the driver statement's Close with the same per-connection
// lock held, so the CAS never receives the CLOSE_REQ (or its CHECK_CAS probe)
// while the FETCH reply is still outstanding.
func TestTxStmtCloseDoesNotInterleaveWithRowsFetch(t *testing.T) {
	f := newFakeCAS(t)
	f.rowsTotal = 2
	f.answerFetch = true
	fetching, interleaved := holdReply(f, FuncFetch)

	db, err := sql.Open("cubrid", f.dsn())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	st, err := tx.Prepare("SELECT v FROM t")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	rows, err := st.Query()
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !rows.Next() {
		t.Fatalf("first Next: %v", rows.Err())
	}

	closed := make(chan error, 1)
	go func() {
		<-fetching
		closed <- st.Close()
	}()
	if !rows.Next() {
		t.Fatalf("second Next (FETCH): %v", rows.Err())
	}
	if <-interleaved {
		t.Fatalf("the CAS received another request while the FETCH reply was outstanding")
	}
	if err := <-closed; err != nil {
		t.Fatalf("stmt Close: %v", err)
	}
	if rows.Next() {
		t.Fatalf("expected the end of the result set")
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("rows Close: %v", err)
	}
	f.waitFor("s1 CLOSE_REQ SELECT v FROM t")
}

// TestRowsClosedByCancelWhileNextReconnects covers database/sql closing rows
// from another goroutine: when the query's context is canceled, database/sql
// closes the rows on its own goroutine. Here that happens while Next finds
// the CAS gone and reconnects. Whichever runs first, the result set's handle
// belongs to the earlier session, so the new session must receive neither a
// FETCH nor a CLOSE_REQ for it, and Next must not return a row.
func TestRowsClosedByCancelWhileNextReconnects(t *testing.T) {
	f := newFakeCAS(t)
	f.rowsTotal = 2
	f.distinctHandles = true
	db, err := sql.Open("cubrid", f.dsn())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rows, err := db.QueryContext(ctx, "SELECT v FROM t")
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	if !rows.Next() {
		t.Fatalf("first Next: %v", rows.Err())
	}

	f.restart()
	go cancel()
	if rows.Next() {
		t.Fatalf("Next returned a row from a result set whose session is gone")
	}
	var opErr *OperationalError
	if err := rows.Err(); !errors.As(err, &opErr) && !errors.Is(err, context.Canceled) {
		t.Fatalf("rows.Err() = %v, want an OperationalError or context.Canceled", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("rows Close: %v", err)
	}

	// Use the pool again, so a request reaches whichever session is open
	// now, then check what every later session received.
	if _, err := db.Exec("UPDATE t SET v = 1"); err != nil {
		t.Fatalf("Exec after the canceled result set: %v", err)
	}
	f.waitFor("s2 PREPARE_AND_EXECUTE UPDATE t SET v = 1")
	for _, e := range f.entries() {
		if strings.Contains(e, "unknown handle") || (!strings.HasPrefix(e, "s1 ") &&
			(strings.Contains(e, " FETCH ") || strings.Contains(e, " CLOSE_REQ SELECT"))) {
			t.Fatalf("a later session received %q; the handle belongs to session 1", e)
		}
	}
}

// TestConcurrentDBUseAcrossBrokerRestarts runs queries, prepared statements,
// fetches and transactions from several goroutines on one *sql.DB while the
// broker restarts, which makes connections reconnect mid-use. It checks,
// with the race detector, that pooled connections share no driver state, and
// that no session ever receives a server handle that it did not issue: each
// session numbers its handles from a different base, so the fake CAS logs a
// handle from another session as "unknown handle". Errors are expected (a
// request in flight when the broker restarts has an unknown outcome); the
// connection that reported one must not be used to send a stale handle.
func TestConcurrentDBUseAcrossBrokerRestarts(t *testing.T) {
	f := newFakeCAS(t)
	f.rowsTotal = 2
	f.answerFetch = true
	f.distinctHandles = true
	db, err := sql.Open("cubrid", f.dsn())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(4)

	stmt, err := db.Prepare("SELECT v FROM t")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() { _ = stmt.Close() }()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				switch (g + i) % 4 {
				case 0:
					if rows, err := db.Query("SELECT v FROM t"); err == nil {
						for rows.Next() {
						}
						_ = rows.Close()
					}
				case 1:
					if rows, err := stmt.Query(); err == nil {
						for rows.Next() {
						}
						_ = rows.Close()
					}
				case 2:
					_, _ = db.Exec("UPDATE t SET v = 1")
				case 3:
					if tx, err := db.Begin(); err == nil {
						if st, err := tx.Prepare("SELECT v FROM t"); err == nil {
							if rows, err := st.Query(); err == nil {
								for rows.Next() {
								}
								_ = rows.Close()
							}
							_ = st.Close()
						}
						_ = tx.Rollback()
					}
				}
			}
		}(g)
	}
	for i := 0; i < 5; i++ {
		time.Sleep(20 * time.Millisecond)
		f.restart()
	}
	close(stop)
	wg.Wait()

	for _, e := range f.entries() {
		if strings.Contains(e, "unknown handle") {
			t.Fatalf("a session received a handle it never issued: %q", e)
		}
	}
}
