package data

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

// RolesReport is the read side of the main till's check-in report of roles
// and permission actions (ADR-0128 §5, ut-docs#3323): every roles row with
// its granted actions, and every known action, from one snapshot.
func TestAuthRepo_RolesReport(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)
	const role = "c_01j9z3k4m5n6p7q8r9s0t1v2w3"
	withTx(t, d.DB, func(tx *sql.Tx) {
		if err := repo.UpsertCloudRoleTx(ctx, tx, role, "Shift lead"); err != nil {
			t.Fatal(err)
		}
		if err := repo.ReplaceRoleGrantsTx(ctx, tx, role, []string{"void", "refund"}); err != nil {
			t.Fatal(err)
		}
	})
	// A cloud role with no grants at all still reports `[]`, not nil.
	const empty = "c_01j9z3k4m5n6p7q8r9s0t1v2w4"
	withTx(t, d.DB, func(tx *sql.Tx) {
		if err := repo.UpsertCloudRoleTx(ctx, tx, empty, "Trainee"); err != nil {
			t.Fatal(err)
		}
		if err := repo.ReplaceRoleGrantsTx(ctx, tx, empty, nil); err != nil {
			t.Fatal(err)
		}
	})

	roles, actions, err := repo.RolesReport(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Expected from the same tables through the per-role reads.
	var wantRoles []RoleInfo
	var wantActions []string
	wantGrants := map[string][]string{}
	withTx(t, d.DB, func(tx *sql.Tx) {
		if wantRoles, err = repo.ListRolesTx(ctx, tx); err != nil {
			t.Fatal(err)
		}
		if wantActions, err = repo.ListActionsTx(ctx, tx); err != nil {
			t.Fatal(err)
		}
		for _, ri := range wantRoles {
			g, err := repo.RoleGrantsTx(ctx, tx, ri.Role)
			if err != nil {
				t.Fatal(err)
			}
			wantGrants[ri.Role] = g
		}
	})
	if !reflect.DeepEqual(actions, wantActions) || len(actions) < 5 {
		t.Fatalf("actions = %v, want %v", actions, wantActions)
	}
	if len(roles) != len(wantRoles) {
		t.Fatalf("roles = %+v, want %d rows", roles, len(wantRoles))
	}
	for i, r := range roles {
		if r.RoleInfo != wantRoles[i] {
			t.Fatalf("roles[%d] = %+v, want %+v", i, r.RoleInfo, wantRoles[i])
		}
		if r.Grants == nil || !reflect.DeepEqual(r.Grants, wantGrants[r.Role]) {
			t.Fatalf("grants of %s = %#v, want %#v", r.Role, r.Grants, wantGrants[r.Role])
		}
	}
	byRole := map[string]RoleReportRow{}
	for _, r := range roles {
		byRole[r.Role] = r
	}
	if c := byRole[role]; c.Origin != RoleOriginCloud || c.Label != "Shift lead" || !reflect.DeepEqual(c.Grants, []string{"refund", "void"}) {
		t.Fatalf("cloud role = %+v", c)
	}
	if e := byRole[empty]; e.Grants == nil || len(e.Grants) != 0 {
		t.Fatalf("empty role grants = %#v, want []", e.Grants)
	}
	if a := byRole["super_admin"]; a.Origin != RoleOriginBuiltin || len(a.Grants) == 0 {
		t.Fatalf("super_admin = %+v", a)
	}
}
