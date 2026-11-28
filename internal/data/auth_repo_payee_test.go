package data

import (
	"context"
	"testing"
)

// ut-docs#7247 / ADR-0063 amendment 1: a payee is a users row that can
// never log in and has no permissions; it exists only as a payout target.

func TestAuthRepo_EnsurePayee_CreatesInertUser(t *testing.T) {
	ctx := context.Background()
	repo, _ := newAuthTestRepo(t)

	id, created, err := repo.EnsurePayee(ctx, "  Sam  Jones ")
	if err != nil || !created || id == "" {
		t.Fatalf("EnsurePayee: id=%q created=%v err=%v", id, created, err)
	}
	u, ok, _ := repo.GetUser(ctx, id)
	if !ok || u.Role != PayeeRole || u.IsActive || u.PinHash != "" || u.DisplayName != "Sam Jones" {
		t.Fatalf("payee must be inactive, PIN-less, role payee: %+v", u)
	}
	// Same label (any case/spacing) reuses the row.
	id2, created2, err := repo.EnsurePayee(ctx, "sam jones")
	if err != nil || created2 || id2 != id {
		t.Fatalf("repeat: id=%q created=%v err=%v", id2, created2, err)
	}
	// Never a login candidate, never granted anything.
	login, _ := repo.ListActiveUsersWithPIN(ctx)
	for _, l := range login {
		if l.ID == id {
			t.Fatal("payee listed as login candidate")
		}
	}
	if ok, _ := repo.HasPermission(ctx, PayeeRole, "sale"); ok {
		t.Fatal("payee role must hold no permission")
	}
}

func TestAuthRepo_EnsurePayee_RejectsEmptyLabel(t *testing.T) {
	repo, _ := newAuthTestRepo(t)
	if _, _, err := repo.EnsurePayee(context.Background(), " \t "); err == nil {
		t.Fatal("empty label must be refused")
	}
}

func TestAuthRepo_PayeeCannotBeMadeLoginCapable(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)
	id, _, _ := repo.EnsurePayee(ctx, "Sam")

	if err := repo.SetUserPIN(ctx, id, "hash"); err == nil {
		t.Fatal("SetUserPIN on a payee must fail")
	}
	if err := repo.SetUserActive(ctx, id, true); err == nil {
		t.Fatal("SetUserActive on a payee must fail")
	}
	if err := repo.SetUserRole(ctx, nil, id, "admin"); err == nil {
		t.Fatal("SetUserRole on a payee must fail")
	}
	tx, _ := d.DB.BeginTx(ctx, nil)
	defer tx.Rollback()
	if err := repo.SetUserPINTx(ctx, tx, id, "hash"); err == nil {
		t.Fatal("SetUserPINTx on a payee must fail")
	}
	if _, err := repo.SetUserActiveGuarded(ctx, tx, id, true); err == nil {
		t.Fatal("SetUserActiveGuarded on a payee must fail")
	}
	if _, err := repo.SetUserRoleGuarded(ctx, tx, id, "admin"); err == nil {
		t.Fatal("SetUserRoleGuarded on a payee must fail")
	}
	// Nothing may create a payee-role row or turn one into something else
	// through the sync mirror either.
	if err := repo.MirrorUser(ctx, UserRow{ID: id, Username: "x", DisplayName: "x", Role: "admin", PinHash: "h", IsActive: true}); err == nil {
		t.Fatal("MirrorUser over a payee must fail")
	}
	if err := repo.MirrorUser(ctx, UserRow{ID: "n1", Username: "n1", DisplayName: "n", Role: PayeeRole, IsActive: true}); err == nil {
		t.Fatal("MirrorUser must refuse role payee")
	}
	if err := repo.CreateUserWithID(ctx, tx, "n2", "n2", "n", PayeeRole); err == nil {
		t.Fatal("CreateUserWithID must refuse role payee")
	}
	if _, err := repo.CreateUser(ctx, "n3", "n", PayeeRole); err == nil {
		t.Fatal("CreateUser must refuse role payee")
	}
	u, _, _ := repo.GetUser(ctx, id)
	if u.IsActive || u.PinHash != "" || u.Role != PayeeRole {
		t.Fatalf("payee changed: %+v", u)
	}
}
