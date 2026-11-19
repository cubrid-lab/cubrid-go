package cubrid

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
)

// Tests for #86: closing a prepared statement while rows from it are open
// defers the statement's CLOSE_REQ until those rows no longer need the handle.

// assertSessionLog fails unless session s of f received exactly want.
func assertSessionLog(t *testing.T, f *fakeCAS, s string, want ...string) {
	t.Helper()
	if got := sessionLog(f.entries(), s); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("session %s received %q, want %q", s, got, want)
	}
}

// openTxStmtRows prepares SELECT on a Tx of a one-connection pool, queries
// it and reads the first row, which arrives with the EXECUTE reply.
func openTxStmtRows(t *testing.T, f *fakeCAS) (*sql.Stmt, *sql.Rows) {
	t.Helper()
	db, err := sql.Open("cubrid", f.dsn())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
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
	return st, rows
}

// TestStmtCloseDeferredUntilRowsClose closes the statement, then the rows
// before they reach the end: the CLOSE_REQ is sent by the rows' Close, and
// not before.
func TestStmtCloseDeferredUntilRowsClose(t *testing.T) {
	f := newFakeCAS(t)
	f.rowsTotal = 3
	f.answerFetch = true
	st, rows := openTxStmtRows(t, f)

	if err := st.Close(); err != nil {
		t.Fatalf("stmt Close: %v", err)
	}
	assertSessionLog(t, f, "s1", "s1 PREPARE SELECT v FROM t", "s1 EXECUTE SELECT v FROM t")
	if err := rows.Close(); err != nil {
		t.Fatalf("rows Close: %v", err)
	}
	assertSessionLog(t, f, "s1", "s1 PREPARE SELECT v FROM t", "s1 EXECUTE SELECT v FROM t",
		"s1 CLOSE_REQ SELECT v FROM t")
}

// TestStmtCloseAfterRowsDrainedToEOF uses the driver directly: rows that
// have read their last row no longer FETCH, so closing the statement before
// closing those rows releases the handle at once, and the rows' Close sends
// nothing.
func TestStmtCloseAfterRowsDrainedToEOF(t *testing.T) {
	f := newFakeCAS(t)
	f.rowsTotal = 3
	f.answerFetch = true
	dc, err := (&Driver{}).Open(f.dsn())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := dc.(*conn)
	defer func() { _ = c.Close() }()

	ds, err := c.Prepare("SELECT v FROM t")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	r, err := ds.(*stmt).Query(nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	dest := make([]driver.Value, 1)
	n := 0
	for {
		err := r.Next(dest)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		n++
	}
	if n != 3 {
		t.Fatalf("read %d rows, want 3", n)
	}
	if err := ds.Close(); err != nil {
		t.Fatalf("stmt Close: %v", err)
	}
	want := []string{"s1 PREPARE SELECT v FROM t", "s1 EXECUTE SELECT v FROM t",
		"s1 FETCH SELECT v FROM t", "s1 FETCH SELECT v FROM t", "s1 CLOSE_REQ SELECT v FROM t"}
	assertSessionLog(t, f, "s1", want...)
	if err := r.Close(); err != nil {
		t.Fatalf("rows Close: %v", err)
	}
	assertSessionLog(t, f, "s1", want...)
}

// TestDeferredStmtCloseDropsStaleHandle covers a deferred CLOSE_REQ whose
// handle belongs to a session that is gone by the time the rows close. The
// new session numbers its handles from 1 again, so the old handle number may
// name another statement there; the deferred close must send nothing for it
// on the new session, whether the reconnect happens before the rows close or
// is triggered by the rows' Close itself.
func TestDeferredStmtCloseDropsStaleHandle(t *testing.T) {
	for _, tc := range []struct {
		name string
		// reprepare makes another request reconnect, and prepare DELETE as
		// handle 1 on the new session, before the rows close.
		reprepare bool
	}{
		{name: "reconnect before rows close", reprepare: true},
		{name: "rows close reconnects", reprepare: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCAS(t)
			f.rowsTotal = 3
			f.answerFetch = true
			dc, err := (&Driver{}).Open(f.dsn())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			c := dc.(*conn)
			defer func() { _ = c.Close() }()

			ds, err := c.Prepare("SELECT v FROM t")
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			r, err := ds.(*stmt).Query(nil)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			dest := make([]driver.Value, 1)
			if err := r.Next(dest); err != nil {
				t.Fatalf("first Next: %v", err)
			}
			if err := ds.Close(); err != nil {
				t.Fatalf("stmt Close: %v", err)
			}

			f.restart()
			var s2 []string
			if tc.reprepare {
				if _, err := c.Prepare("DELETE FROM t"); err != nil {
					t.Fatalf("Prepare after broker restart: %v", err)
				}
				s2 = append(s2, "s2 PREPARE DELETE FROM t")
				var opErr *OperationalError
				if err := r.Next(dest); !errors.As(err, &opErr) {
					t.Fatalf("Next after reconnect: got %v, want an OperationalError", err)
				}
			}
			if err := r.Close(); err != nil {
				t.Fatalf("rows Close: %v", err)
			}
			if c.generation != 2 {
				t.Fatalf("generation = %d, want 2: the test did not reconnect", c.generation)
			}
			assertSessionLog(t, f, "s1", "s1 PREPARE SELECT v FROM t", "s1 EXECUTE SELECT v FROM t")
			assertSessionLog(t, f, "s2", s2...)
			if ds.(*stmt).queryHandle != 0 || ds.(*stmt).openRows != 0 {
				t.Fatalf("stmt keeps handle %d with %d open rows after its rows closed",
					ds.(*stmt).queryHandle, ds.(*stmt).openRows)
			}
		})
	}
}

// TestStmtCloseWaitsForAllOpenRows opens two result sets from one statement
// prepared on a Tx and closes the statement: the CLOSE_REQ waits for the
// last of them, the other keeps fetching meanwhile, and no FETCH follows the
// CLOSE_REQ.
func TestStmtCloseWaitsForAllOpenRows(t *testing.T) {
	f := newFakeCAS(t)
	f.rowsTotal = 3
	f.answerFetch = true
	st, rows1 := openTxStmtRows(t, f)
	rows2, err := st.Query()
	if err != nil {
		t.Fatalf("second Query: %v", err)
	}
	if !rows2.Next() {
		t.Fatalf("second rows, first Next: %v", rows2.Err())
	}

	if err := st.Close(); err != nil {
		t.Fatalf("stmt Close: %v", err)
	}
	if err := rows1.Close(); err != nil {
		t.Fatalf("first rows Close: %v", err)
	}
	for _, e := range f.entries() {
		if strings.Contains(e, "CLOSE_REQ") {
			t.Fatalf("CLOSE_REQ sent while the second rows are open: %q", f.entries())
		}
	}
	n := 1
	for rows2.Next() {
		n++
	}
	if err := rows2.Err(); err != nil {
		t.Fatalf("second rows after the statement closed: %v", err)
	}
	if n != 3 {
		t.Fatalf("second rows read %d rows, want 3", n)
	}
	_ = rows2.Close()

	assertSessionLog(t, f, "s1", "s1 PREPARE SELECT v FROM t",
		"s1 EXECUTE SELECT v FROM t", "s1 EXECUTE SELECT v FROM t",
		"s1 FETCH SELECT v FROM t", "s1 FETCH SELECT v FROM t",
		"s1 CLOSE_REQ SELECT v FROM t")
}
