package data

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

// The transaction-taking AuthRepo reads/writes behind the custom role
// directives (ADR-0128 §3, ut-docs#3165).

func TestAuthRepo_CloudRoleLifecycle(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)
	const role = "c_01j9z3k4m5n6p7q8r9s0t1v2w3"

	withTx(t, d.DB, func(tx *sql.Tx) {
		if _, ok, err := repo.GetRoleTx(ctx, tx, role); err != nil || ok {
			t.Fatalf("GetRoleTx before create: %v %v", ok, err)
		}
		admin, ok, err := repo.GetRoleTx(ctx, tx, "admin")
		if err != nil || !ok || admin.Origin != "builtin" || admin.Label != "" {
			t.Fatalf("GetRoleTx admin: %+v %v %v", admin, ok, err)
		}
		actions, err := repo.ListActionsTx(ctx, tx)
		if err != nil || len(actions) < 5 {
			t.Fatalf("ListActionsTx: %v %v", actions, err)
		}
		if err := repo.UpsertCloudRoleTx(ctx, tx, role, "Shift lead"); err != nil {
			t.Fatal(err)
		}
		if err := repo.ReplaceRoleGrantsTx(ctx, tx, role, []string{"refund", "audit"}); err != nil {
			t.Fatal(err)
		}
		got, err := repo.RoleGrantsTx(ctx, tx, role)
		if err != nil || !reflect.DeepEqual(got, []string{"audit", "refund"}) {
			t.Fatalf("RoleGrantsTx: %v %v", got, err)
		}
		// Every known action has a row: granted=0 for the unlisted ones.
		var rows int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM role_permissions WHERE role = ?`, role).Scan(&rows); err != nil || rows != len(actions) {
			t.Fatalf("grant rows %d, want %d (%v)", rows, len(actions), err)
		}
		// Replace: the complete new set.
		if err := repo.ReplaceRoleGrantsTx(ctx, tx, role, []string{"void"}); err != nil {
			t.Fatal(err)
		}
		if got, _ := repo.RoleGrantsTx(ctx, tx, role); !reflect.DeepEqual(got, []string{"void"}) {
			t.Fatalf("after replace: %v", got)
		}
		if err := repo.UpsertCloudRoleTx(ctx, tx, role, "Lead"); err != nil {
			t.Fatal(err)
		}
		roles, err := repo.ListRolesTx(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range roles {
			if r.Role == role {
				found = r.Label == "Lead" && r.Origin == "cloud"
			}
		}
		if !found {
			t.Fatalf("ListRolesTx: %+v", roles)
		}
		// A built-in key is never turned into a cloud role.
		if err := repo.UpsertCloudRoleTx(ctx, tx, "manager", "Boss"); err == nil {
			t.Fatal("UpsertCloudRoleTx on a built-in role must fail")
		}
		if err := repo.DeleteCloudRoleTx(ctx, tx, "manager"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := repo.GetRoleTx(ctx, tx, "manager"); !ok {
			t.Fatal("DeleteCloudRoleTx must never delete a built-in role")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, username, display_name, role, is_active) VALUES ('u1', 'u1', 'U1', ?, 0)`, role); err != nil {
			t.Fatal(err)
		}
		if n, err := repo.CountUsersWithRoleTx(ctx, tx, role); err != nil || n != 1 {
			t.Fatalf("CountUsersWithRoleTx (inactive counts): %d %v", n, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = 'u1'`); err != nil {
			t.Fatal(err)
		}
		if err := repo.DeleteCloudRoleTx(ctx, tx, role); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := repo.GetRoleTx(ctx, tx, role); ok {
			t.Fatal("cloud role not deleted")
		}
	})
	if origin, ok, err := repo.RoleOrigin(ctx, "cashier"); err != nil || !ok || origin != "builtin" {
		t.Fatalf("RoleOrigin cashier: %q %v %v", origin, ok, err)
	}
	if _, ok, err := repo.RoleOrigin(ctx, "nope"); err != nil || ok {
		t.Fatalf("RoleOrigin missing: %v %v", ok, err)
	}
}
