package cubrid

import (
	"database/sql"
	"fmt"
	"testing"
)

// TestDatabaseSQLIntegration tests the full database/sql driver flow.
func TestDatabaseSQLIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db, err := sql.Open("cubrid", "cubrid://dba:@localhost:33000/testdb?autocommit=true")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	// Ping
	if err := db.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	t.Log("Ping OK")

	// Simple query
	var result int
	err = db.QueryRow("SELECT 1+1 AS val").Scan(&result)
	if err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if result != 2 {
		t.Fatalf("expected 2, got %d", result)
	}
	t.Logf("SELECT 1+1 = %d", result)

	// DDL
	_, err = db.Exec("DROP TABLE IF EXISTS go_test_basic")
	if err != nil {
		t.Fatalf("DROP TABLE: %v", err)
	}
	_, err = db.Exec("CREATE TABLE go_test_basic (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(100), age INT)")
	if err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	defer db.Exec("DROP TABLE IF EXISTS go_test_basic")
	t.Log("CREATE TABLE OK")

	// INSERT
	res, err := db.Exec("INSERT INTO go_test_basic (name, age) VALUES (?, ?)", "Alice", 30)
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

	res, err = db.Exec("INSERT INTO go_test_basic (name, age) VALUES (?, ?)", "Bob", 25)
	if err != nil {
		t.Fatalf("INSERT 2: %v", err)
	}

	// SELECT multiple rows
	rows, err := db.Query("SELECT id, name, age FROM go_test_basic ORDER BY id")
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
	res, err = db.Exec("UPDATE go_test_basic SET age = ? WHERE name = ?", 31, "Alice")
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	affected, _ := res.RowsAffected()
	t.Logf("UPDATE: rowsAffected=%d", affected)

	// Verify update
	var updatedAge int
	err = db.QueryRow("SELECT age FROM go_test_basic WHERE name = ?", "Alice").Scan(&updatedAge)
	if err != nil {
		t.Fatalf("SELECT after UPDATE: %v", err)
	}
	if updatedAge != 31 {
		t.Fatalf("expected age 31, got %d", updatedAge)
	}

	// DELETE
	res, err = db.Exec("DELETE FROM go_test_basic WHERE name = ?", "Bob")
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	affected, _ = res.RowsAffected()
	t.Logf("DELETE: rowsAffected=%d", affected)

	// Verify count
	var finalCount int
	err = db.QueryRow("SELECT COUNT(*) FROM go_test_basic").Scan(&finalCount)
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
	_, err = tx.Exec("INSERT INTO go_test_basic (name, age) VALUES (?, ?)", "Charlie", 35)
	if err != nil {
		t.Fatalf("tx INSERT: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// Verify rollback
	err = db.QueryRow("SELECT COUNT(*) FROM go_test_basic").Scan(&finalCount)
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
	db, err := sql.Open("cubrid", "cubrid://dba:@localhost:33000/testdb?autocommit=true")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	// Enough rows that the EXECUTE reply cannot carry them all, so Next
	// has to FETCH after the statement is closed.
	const total = 2000
	for _, q := range []string{
		"DROP TABLE IF EXISTS go_test_86a",
		"DROP TABLE IF EXISTS go_test_86b",
		"CREATE TABLE go_test_86a (v INT)",
		"CREATE TABLE go_test_86b (v INT)",
		fmt.Sprintf("INSERT INTO go_test_86a SELECT ROWNUM FROM db_class x, db_class y, db_class z LIMIT %d", total),
		fmt.Sprintf("INSERT INTO go_test_86b SELECT 1000000 + ROWNUM FROM db_class x, db_class y, db_class z LIMIT %d", total),
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	defer db.Exec("DROP TABLE IF EXISTS go_test_86a")
	defer db.Exec("DROP TABLE IF EXISTS go_test_86b")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()
	st, err := tx.Prepare("SELECT v FROM go_test_86a ORDER BY v")
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

	other, err := tx.Prepare("SELECT v FROM go_test_86b ORDER BY v")
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
	db, err := sql.Open("cubrid", "cubrid://dba:@localhost:33000/testdb?autocommit=true")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	const total = 2000
	for _, q := range []string{
		"DROP TABLE IF EXISTS go_test_86r",
		"CREATE TABLE go_test_86r (v INT)",
		fmt.Sprintf("INSERT INTO go_test_86r SELECT ROWNUM FROM db_class x, db_class y, db_class z LIMIT %d", total),
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	defer db.Exec("DROP TABLE IF EXISTS go_test_86r")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()
	st, err := tx.Prepare("SELECT v FROM go_test_86r ORDER BY v")
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
	if _, err := tx.Exec("UPDATE go_test_86r SET v = v + 100000"); err != nil {
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
