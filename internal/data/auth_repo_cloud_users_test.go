package data

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// The transaction-taking AuthRepo writes behind the cloud user directives
// (ut-docs reference/till-user-directives.md §4): everything runs inside
// the caller's one write transaction and is invisible until it commits.

func withTx(t *testing.T, db *sql.DB, f func(tx *sql.Tx)) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	f(tx)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthRepo_CreateUserWithIDAndProfileUpdate(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)
	const id = "7d1c0b1e-0000-4000-8000-000000000001"
	withTx(t, d.DB, func(tx *sql.Tx) {
		if err := repo.CreateUserWithID(ctx, tx, id, "anna", "Anna", "cashier"); err != nil {
			t.Fatalf("CreateUserWithID: %v", err)
		}
		u, ok, err := repo.GetUserTx(ctx, tx, id)
		if err != nil || !ok || u.Username != "anna" || u.Role != "cashier" || !u.IsActive || u.PinHash != "" {
			t.Fatalf("GetUserTx: %+v %v %v", u, ok, err)
		}
		if err := repo.SetUserPINTx(ctx, tx, id, "pbkdf2$x"); err != nil {
			t.Fatal(err)
		}
	})
	u, _, _ := repo.GetUser(ctx, id)
	if u.PinHash != "pbkdf2$x" {
		t.Fatalf("pin hash = %q", u.PinHash)
	}
	// A duplicate id errors.
	withTx(t, d.DB, func(tx *sql.Tx) {
		if err := repo.CreateUserWithID(ctx, tx, id, "other", "Other", "cashier"); err == nil {
			t.Fatal("duplicate id accepted")
		}
	})

	other, err := repo.CreateUser(ctx, "ben", "Ben", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	withTx(t, d.DB, func(tx *sql.Tx) {
		taken, err := repo.UsernameTakenByOther(ctx, tx, "ben", id)
		if err != nil || !taken {
			t.Fatalf("ben taken by other: %v %v", taken, err)
		}
		if taken, _ := repo.UsernameTakenByOther(ctx, tx, "ben", other); taken {
			t.Fatal("a user's own username counted as taken")
		}
		if taken, _ := repo.UsernameTakenByOther(ctx, tx, "nobody", id); taken {
			t.Fatal("free username counted as taken")
		}
		name, disp := "anna2", "Anna Two"
		if err := repo.UpdateUserProfile(ctx, tx, id, &name, &disp); err != nil {
			t.Fatal(err)
		}
		// nil keeps.
		if err := repo.UpdateUserProfile(ctx, tx, id, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := repo.UpdateUserProfile(ctx, tx, "missing", &name, nil); err == nil {
			t.Fatal("update of a missing user succeeded")
		}
	})
	u, _, _ = repo.GetUser(ctx, id)
	if u.Username != "anna2" || u.DisplayName != "Anna Two" {
		t.Fatalf("profile = %+v", u)
	}
	if err := repo.SetUserPINTx(ctx, nil, "missing", "h"); err == nil {
		t.Fatal("SetUserPINTx on a missing user succeeded")
	}
}

func TestAuthRepo_RevokeUserSessionsTx(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)
	u1, _ := repo.CreateUser(ctx, "one", "One", "cashier")
	if _, err := repo.InsertSession(ctx, "tok", u1, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RevokeUserSessionsTx(ctx, tx, u1); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback()
	var n int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ? AND revoked_at IS NOT NULL`, u1).Scan(&n)
	if n != 0 {
		t.Fatal("revocation escaped a rolled-back tx")
	}
	withTx(t, d.DB, func(tx *sql.Tx) {
		if err := repo.RevokeUserSessionsTx(ctx, tx, u1); err != nil {
			t.Fatal(err)
		}
	})
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ? AND revoked_at IS NOT NULL`, u1).Scan(&n)
	if n != 1 {
		t.Fatalf("revoked = %d", n)
	}
}
