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
// fc and reports whether the client sent anything else on that socket in the
// meantime. started is closed when that request arrives. The test closes
// competing just before it starts the competing call; the observation window
// (one second) begins only then, so a slow scheduler cannot make the check
// pass merely because the competing call had not started yet.
func holdReply(f *fakeCAS, fc byte) (started <-chan struct{}, competing chan<- struct{}, interleaved <-chan bool) {
	arrived := make(chan struct{})
	competitor := make(chan struct{})
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
		select {
		case <-competitor:
		case <-time.After(5 * time.Second):
			f.t.Errorf("the competing call never started")
		}
		_ = nc.SetReadDeadline(time.Now().Add(time.Second))
		n, _ := nc.Read(make([]byte, 1))
		_ = nc.SetReadDeadline(time.Time{})
		result <- n > 0
		return n == 0 // if the request stream is out of step, drop the session
	}
	return arrived, competitor, result
}

// TestTxStmtCloseDoesNotInterleaveWithRowsFetch pins the database/sql side of
// the concurrency contract. A statement prepared on a Tx can be closed from
// one goroutine while another goroutine iterates rows of the same Tx, and
// both use one physical connection. database/sql serializes the two: it calls
// Rows.Next and the driver statement's Close with the same per-connection
// lock held, so the CAS never receives the CLOSE_REQ (or its CHECK_CAS probe)
// while the FETCH reply is still outstanding. Serializing them does not by
// itself make the close safe for the rows, which share the statement's server
// handle; the driver defers the CLOSE_REQ while rows may still fetch (see #86
// and TestTxStmtCloseReleasesHandleOfOpenRows). Here the result set has two
// rows, so the FETCH in flight is the last one and the close is not deferred.
func TestTxStmtCloseDoesNotInterleaveWithRowsFetch(t *testing.T) {
	f := newFakeCAS(t)
	f.rowsTotal = 2
	f.answerFetch = true
	fetching, competing, interleaved := holdReply(f, FuncFetch)

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
		close(competing)
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

// TestRowsFetchHoldsConnMutex covers direct users of the driver interfaces.
// The conn mutex is what keeps each CAS request and its reply together and
// what guards the socket and session state that a reconnect replaces, so
// rows.Next must hold it while it fetches, as every other request path does.
// Here a Ping on the same driver connection starts while a FETCH reply is
// outstanding; the CAS must not receive the Ping (or its CHECK_CAS probe)
// before the FETCH reply has been read.
func TestRowsFetchHoldsConnMutex(t *testing.T) {
	f := newFakeCAS(t)
	f.rowsTotal = 2
	f.answerFetch = true
	fetching, competing, interleaved := holdReply(f, FuncFetch)

	dc, err := (&Driver{}).Open(f.dsn())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := dc.(*conn)
	defer func() { _ = c.Close() }()

	r, err := c.QueryContext(context.Background(), "SELECT v FROM t", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	dest := make([]driver.Value, 1)
	if err := r.Next(dest); err != nil {
		t.Fatalf("first Next: %v", err)
	}

	pinged := make(chan error, 1)
	go func() {
		<-fetching
		close(competing)
		pinged <- c.Ping()
	}()
	nextErr := r.Next(dest)
	if <-interleaved {
		t.Fatalf("the CAS received another request while the FETCH reply was outstanding")
	}
	if nextErr != nil {
		t.Fatalf("second Next (FETCH): %v", nextErr)
	}
	if err := <-pinged; err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("rows Close: %v", err)
	}
}

// TestRowsCloseHoldsConnMutex is the rows.Close counterpart of
// TestRowsFetchHoldsConnMutex: rows that own their server handle send
// CLOSE_REQ on Close, which may reconnect, so Close must hold the conn mutex
// until the CLOSE_REQ reply has been read.
func TestRowsCloseHoldsConnMutex(t *testing.T) {
	f := newFakeCAS(t)
	f.rowsTotal = 2
	closing, competing, interleaved := holdReply(f, FuncCloseReqHandle)

	dc, err := (&Driver{}).Open(f.dsn())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := dc.(*conn)
	defer func() { _ = c.Close() }()

	r, err := c.QueryContext(context.Background(), "SELECT v FROM t", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}

	pinged := make(chan error, 1)
	go func() {
		<-closing
		close(competing)
		pinged <- c.Ping()
	}()
	closeErr := r.Close()
	if <-interleaved {
		t.Fatalf("the CAS received another request while the CLOSE_REQ reply was outstanding")
	}
	if closeErr != nil {
		t.Fatalf("rows Close: %v", closeErr)
	}
	if err := <-pinged; err != nil {
		t.Fatalf("Ping: %v", err)
	}
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

// TestTxStmtCloseReleasesHandleOfOpenRows covers #86. database/sql closes a
// statement prepared on a Tx or a *sql.Conn at once, even while rows from it
// are still open, and those rows fetch with the statement's server handle.
// Releasing the handle then would let the next PREPARE on the session reuse
// its number (CUBRID 11.0 and 11.2 do), and the rows would fetch that
// statement's result. The driver defers the CLOSE_REQ until the rows no longer need the
// handle, so the rows keep working, as rows of a *sql.DB statement do, and
// the CAS never receives a FETCH for a handle after its CLOSE_REQ.
func TestTxStmtCloseReleasesHandleOfOpenRows(t *testing.T) {
	f := newFakeCAS(t)
	f.rowsTotal = 3
	f.answerFetch = true
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
	if err := st.Close(); err != nil {
		t.Fatalf("stmt Close: %v", err)
	}
	n := 1
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows of the closed statement: %v", err)
	}
	if n != 3 {
		t.Fatalf("read %d rows after closing the statement, want 3", n)
	}
	_ = rows.Close()

	want := []string{"s1 PREPARE SELECT v FROM t", "s1 EXECUTE SELECT v FROM t",
		"s1 FETCH SELECT v FROM t", "s1 FETCH SELECT v FROM t", "s1 CLOSE_REQ SELECT v FROM t"}
	if got := sessionLog(f.entries(), "s1"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CAS received %q, want %q", got, want)
	}
}
