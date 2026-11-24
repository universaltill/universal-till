package db

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// ut-docs#6958: a ledger above every embedded migration means a newer build
// already ran on this data — its own error, never the ADR-0074 "delete the
// data directory" advice.
func TestCheckNotAheadOfBinary(t *testing.T) {
	migs := []migration{{Version: 1}, {Version: 2}, {Version: 3}}
	cases := []struct {
		name    string
		migs    []migration
		current int
		wantErr bool
	}{
		{"behind", migs, 2, false},
		{"equal", migs, 3, false},
		{"ahead", migs, 4, true},
		{"far ahead", migs, 115, true},
		{"empty set", nil, 5, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkNotAheadOfBinary(tc.migs, tc.current)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil {
				return
			}
			if !errors.Is(err, ErrDatabaseNewerThanApp) {
				t.Fatalf("err %v is not ErrDatabaseNewerThanApp", err)
			}
			if !strings.Contains(err.Error(), "data at migration") {
				t.Errorf("message %q lacks the ledger version", err.Error())
			}
		})
	}
}

func TestErrDatabaseNewerThanApp_NeverSuggestsDeleting(t *testing.T) {
	msg := strings.ToLower(ErrDatabaseNewerThanApp.Error())
	for _, bad := range []string{"delete", "reset", "wipe", "data directory", "data folder"} {
		if strings.Contains(msg, bad) {
			t.Errorf("sentinel text contains %q: %s", bad, msg)
		}
	}
}

func TestOpen_DatabaseNewerThanAppIsItsOwnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	ahead := migs[len(migs)-1].Version + 1
	if _, err := d.Exec(`INSERT INTO schema_migrations(version, name, checksum) VALUES (?, ?, ?)`, ahead, "future.sql", "x"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Open(path)
	if err == nil {
		t.Fatal("Open on a DB ahead of the binary must fail")
	}
	if !errors.Is(err, ErrDatabaseNewerThanApp) {
		t.Fatalf("err = %v, want ErrDatabaseNewerThanApp", err)
	}
	if !strings.Contains(err.Error(), "run migrations:") {
		t.Errorf("err %q should keep the run-migrations wrap", err.Error())
	}
	if strings.Contains(strings.ToLower(err.Error()), "delete the data directory") {
		t.Errorf("err %q must not advise deleting the data directory", err.Error())
	}
}
