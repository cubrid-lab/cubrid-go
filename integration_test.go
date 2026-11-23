package cubrid

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// defaultIntegrationDSN is the server the live tests use unless
// CUBRID_GO_TEST_DSN names another one (a Go cubrid:// URL, unlike the
// JDBC-style CUBRID_TEST_DSN that CI also sets).
const defaultIntegrationDSN = "cubrid://dba:@localhost:33000/testdb?autocommit=true"

// openIntegrationDB opens the live test database. It is closed in
// t.Cleanup, after the cleanups of tables created later in the test.
func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("CUBRID_GO_TEST_DSN")
	if dsn == "" {
		dsn = defaultIntegrationDSN
	}
	db, err := sql.Open("cubrid", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// liveTableName matches the only identifiers the live tests create or drop.
var liveTableName = regexp.MustCompile(`^go_test_[a-z0-9]+_[0-9a-f]{16}$`)

// newLiveTableName returns a table name unique to this run, such as
// go_test_basic_1f0c9a7e2b4d6c88. It never equals a fixed go_test_* name.
func newLiveTableName(t testing.TB, purpose string) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random table suffix: %v", err)
	}
	name := "go_test_" + purpose + "_" + hex.EncodeToString(b[:])
	if !liveTableName.MatchString(name) {
		t.Fatalf("invalid live test table name %q", name)
	}
	return name
}

// createOwnedTable creates table name with a plain CREATE TABLE, so an
// existing table of that name makes it fail instead of being reused. Only
// when the CREATE succeeds does the table belong to the test and is a
// DROP registered in t.Cleanup; a failing DROP fails the test.
func createOwnedTable(t testing.TB, db *sql.DB, name, columns string) error {
	t.Helper()
	if !liveTableName.MatchString(name) {
		return fmt.Errorf("invalid live test table name %q", name)
	}
	if _, err := db.Exec("CREATE TABLE " + name + " (" + columns + ")"); err != nil {
		return fmt.Errorf("CREATE TABLE %s (not reusing or dropping it): %w", name, err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DROP TABLE " + name); err != nil {
			t.Errorf("cleanup: DROP TABLE %s: %v", name, err)
		}
	})
	return nil
}

// newOwnedTable creates a fresh run-owned table and returns its name.
func newOwnedTable(t testing.TB, db *sql.DB, purpose, columns string) string {
	t.Helper()
	name := newLiveTableName(t, purpose)
	if err := createOwnedTable(t, db, name, columns); err != nil {
		t.Fatal(err)
	}
	return name
}

// tableExists reports whether the connected user owns a table of that
// name, so another user's table of the same name does not count.
func tableExists(t testing.TB, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM db_class WHERE class_name = ? AND owner_name = CURRENT_USER", name).Scan(&n); err != nil {
		t.Fatalf("db_class lookup for %s: %v", name, err)
	}
	return n > 0
}

// TestDatabaseSQLIntegration tests the full database/sql driver flow.
func TestDatabaseSQLIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := openIntegrationDB(t)

	// Ping
	if err := db.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	t.Log("Ping OK")

	// Simple query
	var result int
	err := db.QueryRow("SELECT 1+1 AS val").Scan(&result)
	if err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if result != 2 {
		t.Fatalf("expected 2, got %d", result)
	}
	t.Logf("SELECT 1+1 = %d", result)

	// DDL: a fresh table owned by this run, so the first AUTO_INCREMENT
	// value below is 1.
	tbl := newOwnedTable(t, db, "basic", "id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(100), age INT")
	t.Logf("CREATE TABLE %s OK", tbl)

	// INSERT
	res, err := db.Exec("INSERT INTO "+tbl+" (name, age) VALUES (?, ?)", "Alice", 30)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	lastID, _ := res.LastInsertId()
	rowsAffected, _ := res.RowsAffected()
	if lastID != 1 {
		t.Fatalf("expected lastInsertId=1, got %d", lastID)
	}
	if rowsAffected != 1 {
		t.Fatalf("expected rowsAffected=1, got %d", rowsAffected)
	}
	t.Logf("INSERT: lastID=%d, rowsAffected=%d", lastID, rowsAffected)

	res, err = db.Exec("INSERT INTO "+tbl+" (name, age) VALUES (?, ?)", "Bob", 25)
	if err != nil {
		t.Fatalf("INSERT 2: %v", err)
	}

	// SELECT multiple rows
	rows, err := db.Query("SELECT id, name, age FROM " + tbl + " ORDER BY id")
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var id, age int
		var name string
		if err := rows.Scan(&id, &name, &age); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		t.Logf("  Row: id=%d, name=%s, age=%d", id, name, age)
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 rows, got %d", count)
	}

	// UPDATE
	res, err = db.Exec("UPDATE "+tbl+" SET age = ? WHERE name = ?", 31, "Alice")
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	affected, _ := res.RowsAffected()
	t.Logf("UPDATE: rowsAffected=%d", affected)

	// Verify update
	var updatedAge int
	err = db.QueryRow("SELECT age FROM "+tbl+" WHERE name = ?", "Alice").Scan(&updatedAge)
	if err != nil {
		t.Fatalf("SELECT after UPDATE: %v", err)
	}
	if updatedAge != 31 {
		t.Fatalf("expected age 31, got %d", updatedAge)
	}

	// DELETE
	res, err = db.Exec("DELETE FROM "+tbl+" WHERE name = ?", "Bob")
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	affected, _ = res.RowsAffected()
	t.Logf("DELETE: rowsAffected=%d", affected)

	// Verify count
	var finalCount int
	err = db.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&finalCount)
	if err != nil {
		t.Fatalf("COUNT: %v", err)
	}
	if finalCount != 1 {
		t.Fatalf("expected 1 row remaining, got %d", finalCount)
	}

	// Transaction test
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// Ends the transaction if the test stops before Rollback below, so
	// its locks cannot block the cleanup DROP. ErrTxDone is expected.
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO "+tbl+" (name, age) VALUES (?, ?)", "Charlie", 35)
	if err != nil {
		t.Fatalf("tx INSERT: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// Verify rollback
	err = db.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&finalCount)
	if err != nil {
		t.Fatalf("COUNT after rollback: %v", err)
	}
	if finalCount != 1 {
		t.Fatalf("expected 1 row after rollback, got %d", finalCount)
	}
	t.Log("Transaction rollback verified")

	fmt.Println("All database/sql integration tests PASSED!")
}

// TestTxStmtCloseWithOpenRowsIntegration is the live check for #86. A
// statement prepared on a Tx is closed while its rows are open and still
// have rows to FETCH, and another statement is then prepared on the same
// session. CUBRID gives a released handle number to the next PREPARE, so if
// the driver released the handle at once, the open rows would go on to
// return the second statement's rows. They must return their own.
func TestTxStmtCloseWithOpenRowsIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := openIntegrationDB(t)
	db.SetMaxOpenConns(1)

	// Enough rows that the EXECUTE reply cannot carry them all, so Next
	// has to FETCH after the statement is closed.
	const total = 2000
	tblA := newOwnedTable(t, db, "86a", "v INT")
	tblB := newOwnedTable(t, db, "86b", "v INT")
	for _, q := range []string{
		fmt.Sprintf("INSERT INTO %s SELECT ROWNUM FROM db_class x, db_class y, db_class z LIMIT %d", tblA, total),
		fmt.Sprintf("INSERT INTO %s SELECT 1000000 + ROWNUM FROM db_class x, db_class y, db_class z LIMIT %d", tblB, total),
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()
	st, err := tx.Prepare("SELECT v FROM " + tblA + " ORDER BY v")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	rows, err := st.Query()
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("first Next: %v", rows.Err())
	}
	if err := st.Close(); err != nil {
		t.Fatalf("stmt Close: %v", err)
	}

	other, err := tx.Prepare("SELECT v FROM " + tblB + " ORDER BY v")
	if err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	defer other.Close()
	var first int
	if err := other.QueryRow().Scan(&first); err != nil {
		t.Fatalf("second statement: %v", err)
	}

	want := 1
	for {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if v != want {
			t.Fatalf("row %d of the closed statement is %d; it returned another statement's rows", want, v)
		}
		want++
		if !rows.Next() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows of the closed statement: %v", err)
	}
	if want-1 != total {
		t.Fatalf("read %d rows, want %d", want-1, total)
	}
}

// TestStmtReexecuteWithOpenRowsIntegration is the live check that executing
// a statement again while rows from it are open does not replace those
// rows' server cursor (#86). The data changes between the two executions,
// so rows that read the second result from their own offset are detected.
func TestStmtReexecuteWithOpenRowsIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := openIntegrationDB(t)
	db.SetMaxOpenConns(1)

	const total = 2000
	tbl := newOwnedTable(t, db, "86r", "v INT")
	q := fmt.Sprintf("INSERT INTO %s SELECT ROWNUM FROM db_class x, db_class y, db_class z LIMIT %d", tbl, total)
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()
	st, err := tx.Prepare("SELECT v FROM " + tbl + " ORDER BY v")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer st.Close()
	rows1, err := st.Query()
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows1.Close()
	if !rows1.Next() {
		t.Fatalf("first Next: %v", rows1.Err())
	}
	if _, err := tx.Exec("UPDATE " + tbl + " SET v = v + 100000"); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	rows2, err := st.Query()
	if err != nil {
		t.Fatalf("second Query: %v", err)
	}
	defer rows2.Close()

	check := func(name string, rows *sql.Rows, first int, alreadyAt bool) {
		t.Helper()
		want, n := first, 0
		if !alreadyAt && !rows.Next() {
			t.Fatalf("%s: no rows: %v", name, rows.Err())
		}
		for {
			var v int
			if err := rows.Scan(&v); err != nil {
				t.Fatalf("%s: Scan: %v", name, err)
			}
			if v != want {
				t.Fatalf("%s: row %d is %d, want %d", name, n+1, v, want)
			}
			want++
			n++
			if !rows.Next() {
				break
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if n != total {
			t.Fatalf("%s: read %d rows, want %d", name, n, total)
		}
	}
	check("second result set", rows2, 100001, false)
	check("first result set", rows1, 1, true)
}

// TestOwnedTableFixtureIntegration checks the live table fixture itself:
// a table the test did not create is neither reused nor dropped, and a
// table it did create is dropped even when the test stops partway.
func TestOwnedTableFixtureIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := openIntegrationDB(t)

	// A table that already exists under the generated name: the fixture
	// must refuse it and must not register a DROP for it.
	existing := newOwnedTable(t, db, "own", "v INT")
	if _, err := db.Exec("INSERT INTO " + existing + " (v) VALUES (42)"); err != nil {
		t.Fatalf("sentinel INSERT: %v", err)
	}
	t.Run("existing table is refused", func(t *testing.T) {
		err := createOwnedTable(t, db, existing, "v INT")
		if err == nil {
			t.Fatalf("createOwnedTable reused existing table %s", existing)
		}
		t.Logf("refused: %v", err)
		if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("CREATE TABLE %s failed for another reason than an existing table: %v", existing, err)
		}
	})
	if !tableExists(t, db, existing) {
		t.Fatalf("existing table %s is gone after the refused create", existing)
	}
	var v, n int
	if err := db.QueryRow("SELECT COUNT(*), MAX(v) FROM "+existing).Scan(&n, &v); err != nil {
		t.Fatalf("existing table after refused create: %v", err)
	}
	if n != 1 || v != 42 {
		t.Fatalf("existing table changed: %d rows, max %d; want 1 row of 42", n, v)
	}

	// A test that hits an error after creating its table still drops it.
	var created string
	t.Run("stops after an error", func(t *testing.T) {
		created = newOwnedTable(t, db, "abort", "v INT")
		if _, err := db.Exec("INSERT INTO " + created + " (missing_col) VALUES (1)"); err == nil {
			t.Fatalf("INSERT into a missing column succeeded")
		}
		// Stop here the way t.Fatal would, but without failing the parent.
		t.SkipNow()
	})
	if created == "" {
		t.Fatal("subtest did not create its table")
	}
	if tableExists(t, db, created) {
		t.Fatalf("table %s survived the cleanup of the test that created it", created)
	}
}
