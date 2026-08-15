package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// openTestDB opens a fresh, fully-migrated till DB in a temp dir — this
// tool runs against the real schema (unlike internal/pages' hand-built
// fixture), so it exercises the real migrated `users` table.
func openTestDB(t *testing.T) (*db.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bootstrap.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d, path
}

func TestRun_PromotesExistingUser(t *testing.T) {
	d, path := openTestDB(t)
	ctx := context.Background()
	repo := data.NewAuthRepo(d.DB)

	if _, err := repo.CreateUser(ctx, "amir", "Amir", "admin"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := run(path, "amir", false); err != nil {
		t.Fatalf("run: %v", err)
	}

	users, err := repo.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	found := false
	for _, u := range users {
		if u.Username == "amir" {
			found = true
			if u.Role != "super_admin" {
				t.Fatalf("Role = %q, want super_admin", u.Role)
			}
		}
	}
	if !found {
		t.Fatal("amir not found")
	}

	posRepo := data.NewPOSRepo(d.DB)
	entries, err := posRepo.ListAudit(ctx, data.AuditFilters{EntityType: "user", ActorID: "system"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	auditFound := false
	for _, e := range entries {
		if e.Action == "user_role_changed" {
			auditFound = true
		}
	}
	if !auditFound {
		t.Fatal("expected a user_role_changed audit entry from the bootstrap CLI")
	}
}

func TestRun_RefusesWhenSuperAdminAlreadyExistsWithoutForce(t *testing.T) {
	d, path := openTestDB(t)
	ctx := context.Background()
	repo := data.NewAuthRepo(d.DB)

	existingID, err := repo.CreateUser(ctx, "sam", "Sam", "admin")
	if err != nil {
		t.Fatalf("CreateUser sam: %v", err)
	}
	if err := repo.SetUserRole(ctx, nil, existingID, "super_admin"); err != nil {
		t.Fatalf("SetUserRole: %v", err)
	}
	if _, err := repo.CreateUser(ctx, "amir", "Amir", "admin"); err != nil {
		t.Fatalf("CreateUser amir: %v", err)
	}

	if err := run(path, "amir", false); err == nil {
		t.Fatal("expected run to refuse when a super_admin already exists")
	}

	if u, _, _ := repo.GetUser(ctx, existingID); u.Role != "super_admin" {
		t.Fatalf("existing super_admin's role must be untouched, got %q", u.Role)
	}
	users, _ := repo.ListUsers(ctx)
	for _, u := range users {
		if u.Username == "amir" && u.Role == "super_admin" {
			t.Fatal("amir must not have been promoted without --force")
		}
	}
}

func TestRun_ForceOverridesExistingSuperAdminGuard(t *testing.T) {
	d, path := openTestDB(t)
	ctx := context.Background()
	repo := data.NewAuthRepo(d.DB)

	existingID, err := repo.CreateUser(ctx, "sam", "Sam", "admin")
	if err != nil {
		t.Fatalf("CreateUser sam: %v", err)
	}
	if err := repo.SetUserRole(ctx, nil, existingID, "super_admin"); err != nil {
		t.Fatalf("SetUserRole: %v", err)
	}
	if _, err := repo.CreateUser(ctx, "amir", "Amir", "admin"); err != nil {
		t.Fatalf("CreateUser amir: %v", err)
	}

	if err := run(path, "amir", true); err != nil {
		t.Fatalf("run with --force: %v", err)
	}

	users, err := repo.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	found := false
	for _, u := range users {
		if u.Username == "amir" {
			found = true
			if u.Role != "super_admin" {
				t.Fatalf("Role = %q, want super_admin", u.Role)
			}
		}
	}
	if !found {
		t.Fatal("amir not found")
	}
}

func TestRun_UnknownUsernameErrors(t *testing.T) {
	_, path := openTestDB(t)

	if err := run(path, "nope", false); err == nil {
		t.Fatal("expected an error for an unknown username")
	}
}

// TestRun_NonexistentDBPathErrors covers ut-docs#761 review finding 7:
// db.Open MkdirAll's the parent directory and runs every migration on
// whatever it finds, so a typo'd path silently creates a brand-new, empty,
// fully-migrated DB rather than failing — and the operator then sees the
// misleading "no user with username" error instead of "wrong path". The
// tool refuses up front instead.
func TestRun_NonexistentDBPathErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.db")

	err := run(path, "amir", false)
	if err == nil {
		t.Fatal("expected an error for a nonexistent db path")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("run must not have created a new DB file at the nonexistent path")
	}
}

func TestRun_AlreadySuperAdminErrors(t *testing.T) {
	d, path := openTestDB(t)
	ctx := context.Background()
	repo := data.NewAuthRepo(d.DB)

	id, err := repo.CreateUser(ctx, "amir", "Amir", "admin")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := repo.SetUserRole(ctx, nil, id, "super_admin"); err != nil {
		t.Fatalf("SetUserRole: %v", err)
	}

	if err := run(path, "amir", false); err == nil {
		t.Fatal("expected an error promoting a user who is already super_admin")
	}
}
