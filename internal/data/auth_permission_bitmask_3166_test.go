package data

import (
	"context"
	"fmt"
	"testing"
)

// rowLookupHasPermission is the pre-#3166 row lookup, kept here as the
// oracle the bitmask must agree with for every role × action.
func rowLookupHasPermission(t *testing.T, repo *AuthRepo, role, action string) bool {
	t.Helper()
	var granted int
	err := repo.db.QueryRow(`SELECT granted FROM role_permissions WHERE role = ? AND action = ?`, role, action).Scan(&granted)
	if err != nil {
		return false
	}
	return granted != 0
}

// TestAuthRepo_HasPermission_BitmaskMatchesRowLookup is ADR-0128 §6's table
// test (ut-docs#3166): the in-memory bitmask answers exactly like the
// role_permissions row lookup for every role × action, plus an unknown role
// and an unknown action.
func TestAuthRepo_HasPermission_BitmaskMatchesRowLookup(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)

	// A granted=0 row must deny, like no row at all.
	if _, err := d.DB.Exec(`UPDATE role_permissions SET granted = 0 WHERE role = 'manager' AND action = 'refund'`); err != nil {
		t.Fatal(err)
	}

	// Pad the catalog past 64 actions so a mask spans two uint64 words,
	// granting every third one to admin so the second word has set bits.
	for i := 0; i < 60; i++ {
		a := fmt.Sprintf("bitmask_pad_%02d", i)
		if _, err := d.DB.Exec(`INSERT INTO permission_actions(action) VALUES (?)`, a); err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			if _, err := d.DB.Exec(`INSERT INTO role_permissions(role, action, granted) VALUES ('admin', ?, 1)`, a); err != nil {
				t.Fatal(err)
			}
		}
	}

	roles := queryCol(t, repo, `SELECT role FROM roles`)
	actions := queryCol(t, repo, `SELECT action FROM permission_actions`)
	if len(roles) < 4 || len(actions) <= 64 {
		t.Fatalf("seed too small to exercise the bitmask: %d roles, %d actions", len(roles), len(actions))
	}
	roles = append(roles, "no-such-role")
	actions = append(actions, "no-such-action")

	granted := 0
	for _, role := range roles {
		for _, action := range actions {
			got, err := repo.HasPermission(ctx, role, action)
			if err != nil {
				t.Fatalf("HasPermission(%s, %s): %v", role, action, err)
			}
			want := rowLookupHasPermission(t, repo, role, action)
			if got != want {
				t.Fatalf("HasPermission(%s, %s) = %v, row lookup says %v", role, action, got, want)
			}
			if got {
				granted++
			}
		}
	}
	if granted == 0 {
		t.Fatal("no grant answered true — the table test proves nothing")
	}
	if repo.perm == nil {
		t.Fatal("HasPermission did not build the in-memory bitmask")
	}
}

// TestAuthRepo_HasPermission_EditTakesEffectWithoutRestart: a grant change
// bumps sync_admin_version.generation (migration 023's triggers), and the
// next check on the SAME long-lived repo sees it — no restart, no new repo.
func TestAuthRepo_HasPermission_EditTakesEffectWithoutRestart(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)

	if ok, err := repo.HasPermission(ctx, "manager", "refund"); err != nil || !ok {
		t.Fatalf("seed: manager refund = %v, %v; want granted", ok, err)
	}
	gen := repo.perm.gen

	tx, err := d.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetRolePermission(ctx, tx, "manager", "refund", false); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.HasPermission(ctx, "manager", "refund"); err != nil || ok {
		t.Fatalf("after revoke: manager refund = %v, %v; want denied", ok, err)
	}
	if repo.perm.gen == gen {
		t.Fatal("the bitmask was not rebuilt on the new generation")
	}

	// A brand-new action granted to a role (the directive/bundle shape)
	// gets a bit on the rebuild.
	if _, err := d.DB.Exec(`INSERT INTO permission_actions(action) VALUES ('bitmask_test_action')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO role_permissions(role, action, granted) VALUES ('cashier', 'bitmask_test_action', 1)`); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.HasPermission(ctx, "cashier", "bitmask_test_action"); err != nil || !ok {
		t.Fatalf("new grant: cashier bitmask_test_action = %v, %v; want granted", ok, err)
	}
	if ok, err := repo.HasPermission(ctx, "manager", "bitmask_test_action"); err != nil || ok {
		t.Fatalf("new action, no grant: manager = %v, %v; want denied", ok, err)
	}
}

// TestAuthRepo_HasPermission_NoGenerationRowFallsBack: with no counter row
// to key on (a hand-edited DB) the cache is never trusted, so the answer
// still follows the rows.
func TestAuthRepo_HasPermission_NoGenerationRowFallsBack(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)

	if _, err := d.DB.Exec(`DELETE FROM sync_admin_version`); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.HasPermission(ctx, "manager", "refund"); err != nil || !ok {
		t.Fatalf("manager refund = %v, %v; want granted", ok, err)
	}
	// Generation can't move now, so only a row read sees this revoke.
	if _, err := d.DB.Exec(`UPDATE role_permissions SET granted = 0 WHERE role = 'manager' AND action = 'refund'`); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.HasPermission(ctx, "manager", "refund"); err != nil || ok {
		t.Fatalf("after revoke with no counter row: manager refund = %v, %v; want denied", ok, err)
	}
}

func queryCol(t *testing.T, repo *AuthRepo, q string) []string {
	t.Helper()
	rows, err := repo.db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}
