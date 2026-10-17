package data

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// IsStorageError tells the database failing (abort and retry) from a
// statement the data refused (a constraint: the caller's input), so the
// cloud catalogue pull never records a disk or lock failure as a refused
// row (ut-docs#4463 review).
func TestIsStorageError(t *testing.T) {
	dbo, err := db.Open(testsupport.MigratedDBFile(t, "storage-error.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	_, generic := dbo.Exec(`SELECT * FROM no_such_table`)
	if generic == nil {
		t.Fatal("no driver error")
	}
	if _, err := dbo.Exec(`INSERT INTO settings (key, value) VALUES ('k', 'v')`); err != nil {
		t.Fatal(err)
	}
	_, unique := dbo.Exec(`INSERT INTO settings (key, value) VALUES ('k', 'v')`)
	if unique == nil {
		t.Fatal("no constraint error")
	}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain", errors.New("price must not be negative"), false},
		{"driver error", generic, true},
		{"bad conn", fmt.Errorf("save: %w", driver.ErrBadConn), true},
		{"tx done", fmt.Errorf("save: %w", sql.ErrTxDone), true},
		{"conn done", sql.ErrConnDone, true},
		{"driver error wrapped %w", fmt.Errorf("save item: %w", generic), true},
		{"constraint", unique, false},
		{"constraint wrapped", fmt.Errorf("save: %w", unique), false},
		{"savepoint", fmt.Errorf("%w: release", ErrSavepoint), true},
	} {
		if got := IsStorageError(tc.err); got != tc.want {
			t.Errorf("%s: IsStorageError(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}
