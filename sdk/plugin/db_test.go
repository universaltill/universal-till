//go:build !wasip1

package plugin

import (
	"errors"
	"strings"
	"testing"
)

func dbHost(t *testing.T) *FakeHost {
	t.Helper()
	h := NewFakeHost()
	UseFakeHost(t, h)
	return h
}

func TestDBExec_PassesStatementAndTypedArgs(t *testing.T) {
	h := dbHost(t)
	var gotSQL string
	var gotArgs []any
	h.DBExec = func(sql string, args []any) (int64, error) { gotSQL, gotArgs = sql, args; return 3, nil }
	n, err := DBExec("UPDATE t SET a=? WHERE b=?", "x", 7, 1.5, true, nil, DBBlob("hi"))
	if err != nil || n != 3 {
		t.Fatalf("DBExec = %d, %v; want 3, nil", n, err)
	}
	if gotSQL != "UPDATE t SET a=? WHERE b=?" || len(gotArgs) != 6 {
		t.Fatalf("host saw %q %v", gotSQL, gotArgs)
	}
	if gotArgs[0] != "x" || gotArgs[1] != int64(7) || gotArgs[2] != 1.5 || gotArgs[3] != true || gotArgs[4] != nil || string(gotArgs[5].([]byte)) != "hi" {
		t.Fatalf("args decoded as %#v", gotArgs)
	}
}

func TestDB_RefusesWhatTheHostRefuses(t *testing.T) {
	h := dbHost(t)
	called := false
	h.DBExec = func(string, []any) (int64, error) { called = true; return 0, nil }
	h.DBQuery = func(string, []any) ([]byte, error) { called = true; return []byte("[]"), nil }
	for _, q := range []string{
		"ATTACH DATABASE 'x' AS y", "pragma journal_mode=off", "VACUUM", "BEGIN", "COMMIT",
		"SELECT 1; SELECT 2", "DELETE FROM t; DROP TABLE t", "SELECT * FROM _ut_migrations",
		"SELECT load_extension('x')", "  ", "-- only a comment",
	} {
		if _, err := DBExec(q); !errors.Is(err, ErrInvalid) {
			t.Errorf("DBExec(%q) = %v; want ErrInvalid", q, err)
		}
		if _, err := DBQuery(q); !errors.Is(err, ErrInvalid) {
			t.Errorf("DBQuery(%q) = %v; want ErrInvalid", q, err)
		}
	}
	if called {
		t.Fatal("a refused statement reached the database")
	}
	// A trailing semicolon and a ';' inside a string are one statement.
	for _, q := range []string{"SELECT 1;", "SELECT 'a;b'", "/* c */ select 1 ; -- end"} {
		if _, err := DBQuery(q); err != nil {
			t.Errorf("DBQuery(%q) = %v; want ok", q, err)
		}
	}
}

func TestDB_ErrorTextFromDBError(t *testing.T) {
	h := dbHost(t)
	h.DBExec = func(string, []any) (int64, error) { return 0, nil }
	_, err := DBExec("PRAGMA x")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "statement refused") {
		t.Fatalf("err = %v; want ErrInvalid carrying the host text", err)
	}
	if DBError() != "statement refused" {
		t.Fatalf("DBError = %q", DBError())
	}
	h.DBExec = func(string, []any) (int64, error) { return 0, ErrQuota }
	if _, err := DBExec("INSERT INTO t VALUES (1)"); !errors.Is(err, ErrQuota) {
		t.Fatalf("err = %v; want ErrQuota", err)
	}
}

func TestDB_BadArgs(t *testing.T) {
	h := dbHost(t)
	h.DBExec = func(string, []any) (int64, error) { return 0, nil }
	if _, err := DBExec("DELETE FROM t WHERE a=?", []int{1}); !errors.Is(err, ErrInvalid) {
		t.Errorf("nested arg: %v; want ErrInvalid", err)
	}
	many := make([]any, 65)
	if _, err := DBExec("DELETE FROM t", many...); !errors.Is(err, ErrInvalid) {
		t.Errorf("65 args: %v; want ErrInvalid", err)
	}
	if _, err := DBExec("DELETE FROM t", strings.Repeat("x", 300<<10)); !errors.Is(err, ErrInvalid) {
		t.Errorf("300 KiB args: %v; want ErrInvalid", err)
	}
}

func TestDB_DeniedWithoutHooksOrByDeny(t *testing.T) {
	h := dbHost(t)
	if _, err := DBExec("DELETE FROM t"); !errors.Is(err, ErrDenied) {
		t.Errorf("no hook: %v; want ErrDenied", err)
	}
	h.DBQuery = func(string, []any) ([]byte, error) { return []byte("[]"), nil }
	h.Deny["db_query"] = true
	if _, err := DBQuery("SELECT 1"); !errors.Is(err, ErrDenied) {
		t.Errorf("Deny: %v; want ErrDenied", err)
	}
}

func TestDBQuery_ResultCapAndRetryReplay(t *testing.T) {
	h := dbHost(t)
	big := []byte("[" + strings.Repeat(" ", 200<<10) + "]")
	runs := 0
	h.DBQuery = func(string, []any) ([]byte, error) { runs++; return big, nil }
	out, err := DBQuery("INSERT INTO t VALUES (1) RETURNING id")
	if err != nil || len(out) != len(big) {
		t.Fatalf("DBQuery = %d bytes, %v; want %d", len(out), err, len(big))
	}
	if runs != 1 {
		t.Fatalf("statement ran %d times; the retry must replay the cached result", runs)
	}
	h.DBQuery = func(string, []any) ([]byte, error) { return make([]byte, 1<<20+1), nil }
	if _, err := DBQuery("SELECT 1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over 1 MiB: %v; want ErrInvalid", err)
	}
}

func TestDBQueryInto(t *testing.T) {
	h := dbHost(t)
	h.DBQuery = func(string, []any) ([]byte, error) { return []byte(`[{"id":1,"n":"a"}]`), nil }
	var rows []struct {
		ID int    `json:"id"`
		N  string `json:"n"`
	}
	if err := DBQueryInto(&rows, "SELECT id, n FROM t"); err != nil || len(rows) != 1 || rows[0].N != "a" {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
}

func TestImportDB(t *testing.T) {
	h := dbHost(t)
	h.ImportFiles[42] = []byte("SQLite format 3\x00")
	h.ImportDBQuery = func(file []byte, sql string, args []any) ([]byte, error) {
		if string(file[:6]) != "SQLite" {
			t.Errorf("file = %q", file)
		}
		return []byte(`[{"c":2}]`), nil
	}
	d, err := OpenImportDB(ImportFileHandle(42))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]int
	if err := d.QueryInto(&rows, "SELECT count(*) AS c FROM items"); err != nil || rows[0]["c"] != 2 {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	if _, err := d.Query("PRAGMA writable_schema=1"); !errors.Is(err, ErrInvalid) {
		t.Errorf("PRAGMA on import db: %v; want ErrInvalid", err)
	}
	if _, err := OpenImportDB(ImportFileHandle(99)); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown handle: %v; want ErrNotFound", err)
	}
	if _, err := (&ImportDB{h: 99}).Query("SELECT 1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown db handle: %v; want ErrNotFound", err)
	}
	if _, err := OpenImportDB(ImportFileHandle(42)); err != nil {
		t.Fatalf("second open: %v", err)
	}
	if _, err := OpenImportDB(ImportFileHandle(42)); !errors.Is(err, ErrBusy) {
		t.Errorf("third open: %v; want ErrBusy (2 per event)", err)
	}
}

func TestDB_DeniedInFileImportSandbox(t *testing.T) {
	h := dbHost(t)
	h.FileImport = true
	h.DBQuery = func(string, []any) ([]byte, error) { return []byte("[]"), nil }
	if _, err := DBQuery("SELECT 1"); !errors.Is(err, ErrDenied) {
		t.Fatalf("db_query in file-import sandbox: %v; want ErrDenied", err)
	}
}
