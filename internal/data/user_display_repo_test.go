package data

import (
	"context"
	"database/sql"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// newUserDisplayTestDB opens a REAL fully migrated database (ut-docs#3149):
// the repo's whole contract depends on the user_display_settings table and
// its FK to users, so a hand-rolled CREATE TABLE here would test nothing.
func newUserDisplayTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbh, err := db.Open(testsupport.MigratedDBFile(t, "user_display.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbh.Close() })
	for _, u := range []struct{ id, name string }{{"u-alice", "alice"}, {"u-bob", "bob"}} {
		if _, err := dbh.DB.Exec(`INSERT INTO users (id, username, display_name, role) VALUES (?, ?, ?, 'cashier')`, u.id, u.name, u.name); err != nil {
			t.Fatalf("seed user %s: %v", u.id, err)
		}
	}
	return dbh.DB
}

func TestUserDisplayRepo_GetMissReturnsNotFound(t *testing.T) {
	repo := NewUserDisplayRepo(newUserDisplayTestDB(t))
	v, ok, err := repo.Get(context.Background(), "u-alice", "theme")
	if err != nil || ok || v != "" {
		t.Fatalf("Get on empty table = (%q, %v, %v), want (\"\", false, nil)", v, ok, err)
	}
}

func TestUserDisplayRepo_SetGetRoundTripAndOverwrite(t *testing.T) {
	ctx := context.Background()
	repo := NewUserDisplayRepo(newUserDisplayTestDB(t))

	if err := repo.Set(ctx, "u-alice", "theme", "dark"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, ok, err := repo.Get(ctx, "u-alice", "theme"); err != nil || !ok || v != "dark" {
		t.Fatalf("Get after Set = (%q, %v, %v), want (\"dark\", true, nil)", v, ok, err)
	}
	// Second Set on the same (user, key) is an upsert, not a PK violation.
	if err := repo.Set(ctx, "u-alice", "theme", "light"); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}
	if v, ok, err := repo.Get(ctx, "u-alice", "theme"); err != nil || !ok || v != "light" {
		t.Fatalf("Get after overwrite = (%q, %v, %v), want (\"light\", true, nil)", v, ok, err)
	}
	// Another user's key is untouched by alice's row.
	if v, ok, err := repo.Get(ctx, "u-bob", "theme"); err != nil || ok {
		t.Fatalf("Get for bob = (%q, %v, %v), want not found", v, ok, err)
	}
}

func TestUserDisplayRepo_DeleteThenGetMisses(t *testing.T) {
	ctx := context.Background()
	repo := NewUserDisplayRepo(newUserDisplayTestDB(t))
	if err := repo.Set(ctx, "u-alice", "theme", "dark"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := repo.Set(ctx, "u-bob", "theme", "dark"); err != nil {
		t.Fatalf("Set bob: %v", err)
	}
	if err := repo.Delete(ctx, "u-alice", "theme"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if v, ok, err := repo.Get(ctx, "u-alice", "theme"); err != nil || ok {
		t.Fatalf("Get after Delete = (%q, %v, %v), want not found", v, ok, err)
	}
	// Delete is scoped to the one user.
	if v, ok, err := repo.Get(ctx, "u-bob", "theme"); err != nil || !ok || v != "dark" {
		t.Fatalf("bob's row after deleting alice's = (%q, %v, %v), want (\"dark\", true, nil)", v, ok, err)
	}
	// Deleting a row that doesn't exist is not an error.
	if err := repo.Delete(ctx, "u-alice", "theme"); err != nil {
		t.Fatalf("Delete of missing row: %v", err)
	}
}

func TestUserDisplayRepo_GetAllScopedToUser(t *testing.T) {
	ctx := context.Background()
	repo := NewUserDisplayRepo(newUserDisplayTestDB(t))
	if err := repo.Set(ctx, "u-alice", "theme", "dark"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, "u-alice", "sale.browsing_mode", "strip_overflow"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetAll(ctx, "u-alice")
	if err != nil {
		t.Fatalf("GetAll alice: %v", err)
	}
	want := map[string]string{"theme": "dark", "sale.browsing_mode": "strip_overflow"}
	if len(got) != len(want) {
		t.Fatalf("GetAll alice = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("GetAll alice[%q] = %q, want %q (full: %v)", k, got[k], v, got)
		}
	}

	bob, err := repo.GetAll(ctx, "u-bob")
	if err != nil {
		t.Fatalf("GetAll bob: %v", err)
	}
	if bob == nil || len(bob) != 0 {
		t.Fatalf("GetAll bob = %#v, want empty non-nil map", bob)
	}
}

// The FK to users(id) is real: a row for an operator that doesn't exist is
// refused rather than silently orphaned.
func TestUserDisplayRepo_SetUnknownUserRefusedByFK(t *testing.T) {
	repo := NewUserDisplayRepo(newUserDisplayTestDB(t))
	if err := repo.Set(context.Background(), "u-nobody", "theme", "dark"); err == nil {
		t.Fatal("Set for a non-existent user succeeded; want a foreign-key error")
	}
}

// ut-docs#3149: user_display_settings is admin-synced, so a personal row
// set on the main till reaches a joined till (and the user it FKs onto
// arrives in the same bundle, ahead of it), and a delete on the main till
// propagates rather than leaving a stale preference behind.
func TestUserDisplaySettings_AdminDumpApplyRoundTrip(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO users (id, username, display_name, role) VALUES ('u-alice', 'alice', 'Alice', 'manager')`)
	if err := NewUserDisplayRepo(primary.DB).Set(ctx, "u-alice", "theme", "dark"); err != nil {
		t.Fatal(err)
	}

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if _, ok := bundle.Tables["user_display_settings"]; !ok {
		t.Fatal("user_display_settings missing from the admin dump")
	}
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if v, ok, err := NewUserDisplayRepo(replica.DB).Get(ctx, "u-alice", "theme"); err != nil || !ok || v != "dark" {
		t.Fatalf("replica Get = (%q, %v, %v), want (\"dark\", true, nil)", v, ok, err)
	}

	if err := NewUserDisplayRepo(primary.DB).Delete(ctx, "u-alice", "theme"); err != nil {
		t.Fatal(err)
	}
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("second dump: %v", err)
	}
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle2)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if v, ok, err := NewUserDisplayRepo(replica.DB).Get(ctx, "u-alice", "theme"); err != nil || ok {
		t.Fatalf("replica Get after primary delete = (%q, %v, %v), want not found", v, ok, err)
	}
}
