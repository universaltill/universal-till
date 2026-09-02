package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// TestBaselineStatementsFor_ReproducesOneTableExactly: the statements
// returned for a table, executed alone on an empty database, must yield
// the same CREATE TABLE text and the same seed rows as a full Open — and
// nothing for any other table.
func TestBaselineStatementsFor_ReproducesOneTableExactly(t *testing.T) {
	stmts, err := BaselineStatementsFor("country_settings")
	if err != nil {
		t.Fatal(err)
	}
	var creates, inserts int
	for _, s := range stmts {
		up := strings.ToUpper(s)
		switch {
		case strings.HasPrefix(up, "CREATE TABLE"):
			creates++
		case strings.HasPrefix(up, "INSERT"):
			inserts++
		}
		if strings.Contains(up, "CREATE TABLE") && !strings.Contains(up, "COUNTRY_SETTINGS") {
			t.Fatalf("statement targets another table: %s", s)
		}
	}
	if creates != 1 || inserts == 0 {
		t.Fatalf("got %d CREATE TABLE and %d INSERT statements for country_settings, want exactly 1 and >= 1", creates, inserts)
	}

	bare, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Close()
	for _, s := range stmts {
		if _, err := bare.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
	full, err := Open(filepath.Join(t.TempDir(), "full.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()

	var wantDDL, gotDDL string
	if err := full.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'country_settings'`).Scan(&wantDDL); err != nil {
		t.Fatal(err)
	}
	if err := bare.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'country_settings'`).Scan(&gotDDL); err != nil {
		t.Fatal(err)
	}
	if wantDDL != gotDDL {
		t.Fatalf("DDL differs:\n--- Open ---\n%s\n--- BaselineStatementsFor ---\n%s", wantDDL, gotDDL)
	}
	var want, got int
	if err := full.QueryRow(`SELECT COUNT(*) FROM country_settings`).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if err := bare.QueryRow(`SELECT COUNT(*) FROM country_settings`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want == 0 || want != got {
		t.Fatalf("country_settings rows: Open=%d BaselineStatementsFor=%d", want, got)
	}

	if _, err := BaselineStatementsFor("no_such_table"); err == nil {
		t.Fatal("unknown table must error, not return nothing")
	}
}
