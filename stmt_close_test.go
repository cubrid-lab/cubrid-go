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
	if s := ds.(*stmt); s.openRows != 0 || s.queryHandle != 0 {
		t.Fatalf("stmt keeps handle %d with %d open rows", s.queryHandle, s.openRows)
	}
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

// TestStmtCloseWithTwoOpenResultSets opens two result sets from one
// statement prepared on a Tx. The second execution gets its own handle
// (PREPARE_AND_EXECUTE) because the first rows still fetch with the
// prepared one. Closing the statement, then the first rows, releases the
// prepared handle; the second rows keep fetching with their own handle,
// never the released one, and release it when they close.
func TestStmtCloseWithTwoOpenResultSets(t *testing.T) {
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
	assertSessionLog(t, f, "s1", "s1 PREPARE SELECT v FROM t", "s1 EXECUTE SELECT v FROM t",
		"s1 PREPARE_AND_EXECUTE SELECT v FROM t")
	if err := rows1.Close(); err != nil {
		t.Fatalf("first rows Close: %v", err)
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
		"s1 EXECUTE SELECT v FROM t", "s1 PREPARE_AND_EXECUTE SELECT v FROM t",
		"s1 CLOSE_REQ SELECT v FROM t",
		"s1 FETCH SELECT v FROM t", "s1 FETCH SELECT v FROM t",
		"s1 CLOSE_REQ SELECT v FROM t")
}

// TestStmtReexecuteWithOpenRowsUsesOwnHandle covers executing a statement
// again while rows from it can still fetch. EXECUTE on the prepared handle
// would replace the server cursor those rows fetch from: on CUBRID 11.2
// they went on to read the new result from their own offset, with no error.
// While such rows are open, Query and Exec use PREPARE_AND_EXECUTE instead,
// so each result set has its own handle; once the rows are done, the
// prepared handle is executed again.
func TestStmtReexecuteWithOpenRowsUsesOwnHandle(t *testing.T) {
	f := newFakeCAS(t)
	f.rowsTotal = 3
	f.answerFetch = true
	st, rows1 := openTxStmtRows(t, f)

	rows2, err := st.Query()
	if err != nil {
		t.Fatalf("second Query: %v", err)
	}
	if _, err := st.Exec(); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	for _, rs := range []*sql.Rows{rows1, rows2} {
		for rs.Next() {
		}
		if err := rs.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
	}
	rows3, err := st.Query()
	if err != nil {
		t.Fatalf("third Query: %v", err)
	}
	_ = rows3.Close()
	if err := st.Close(); err != nil {
		t.Fatalf("stmt Close: %v", err)
	}

	assertSessionLog(t, f, "s1", "s1 PREPARE SELECT v FROM t", "s1 EXECUTE SELECT v FROM t",
		"s1 PREPARE_AND_EXECUTE SELECT v FROM t",               // rows2
		"s1 PREPARE_AND_EXECUTE SELECT v FROM t",               // Exec
		"s1 CLOSE_REQ SELECT v FROM t",                         // Exec's handle
		"s1 FETCH SELECT v FROM t", "s1 FETCH SELECT v FROM t", // rows1
		"s1 FETCH SELECT v FROM t", "s1 FETCH SELECT v FROM t", // rows2
		"s1 CLOSE_REQ SELECT v FROM t", // rows2's handle
		"s1 EXECUTE SELECT v FROM t",   // rows3, prepared handle
		"s1 CLOSE_REQ SELECT v FROM t") // the statement
}

// TestRowsCloseKeepsOpenStmtHandle: rows closed before their last row stop
// counting against the statement but must not release its handle while the
// statement is still open; the statement's next execution uses it again.
func TestRowsCloseKeepsOpenStmtHandle(t *testing.T) {
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
	s := ds.(*stmt)
	for i := 0; i < 2; i++ {
		r, err := s.Query(nil)
		if err != nil {
			t.Fatalf("Query %d: %v", i+1, err)
		}
		if s.openRows != 1 {
			t.Fatalf("openRows = %d with one result set open, want 1", s.openRows)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("rows Close: %v", err)
		}
		if s.openRows != 0 {
			t.Fatalf("openRows = %d after rows Close, want 0", s.openRows)
		}
	}
	assertSessionLog(t, f, "s1", "s1 PREPARE SELECT v FROM t",
		"s1 EXECUTE SELECT v FROM t", "s1 EXECUTE SELECT v FROM t")
	if s.queryHandle == 0 {
		t.Fatalf("the open statement lost its handle")
	}
}

// TestStmtCloseWithSingleReplyRows: rows that arrived whole with the EXECUTE
// reply never fetch, so they do not hold the statement's handle; closing the
// statement releases it at once, and the rows still return their row.
func TestStmtCloseWithSingleReplyRows(t *testing.T) {
	f := newFakeCAS(t) // rowsTotal 1: the EXECUTE reply carries every row
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
	if n := ds.(*stmt).openRows; n != 0 {
		t.Fatalf("openRows = %d for rows that cannot fetch, want 0", n)
	}
	if err := ds.Close(); err != nil {
		t.Fatalf("stmt Close: %v", err)
	}
	assertSessionLog(t, f, "s1", "s1 PREPARE SELECT v FROM t", "s1 EXECUTE SELECT v FROM t",
		"s1 CLOSE_REQ SELECT v FROM t")
	dest := make([]driver.Value, 1)
	if err := r.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if err := r.Next(dest); err != io.EOF {
		t.Fatalf("second Next = %v, want io.EOF", err)
	}
	_ = r.Close()
}
