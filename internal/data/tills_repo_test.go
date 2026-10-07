package data

import (
	"context"
	"testing"
)

// ut-docs#3294: a joined till's rename reaches the main till's tills row.
// UpdateName writes only on a real change, so an unchanged name (every
// periodic link report repeats it) never bumps sync_admin_version and never
// makes every joined till re-pull the admin bundle.
func TestTillsRepo_UpdateNameChangesOnlyOnARealChange(t *testing.T) {
	d := openMigratedDB(t, "tills-update-name.db")
	repo := NewTillsRepo(d.DB)
	ctx := context.Background()
	id, err := repo.InsertTill(ctx, "Till 2", "hash-2", TillRoleAdditional)
	if err != nil {
		t.Fatal(err)
	}
	generation := func() int64 {
		t.Helper()
		var g int64
		if err := d.QueryRow(`SELECT generation FROM sync_admin_version WHERE id = 1`).Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	nameOf := func() string {
		t.Helper()
		rows, err := repo.ListTills(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.ID == id {
				return r.Name
			}
		}
		t.Fatalf("till %s not listed", id)
		return ""
	}

	before := generation()
	changed, err := repo.UpdateName(ctx, id, "Bar till")
	if err != nil || !changed {
		t.Fatalf("UpdateName = %v, %v; want changed", changed, err)
	}
	if got := nameOf(); got != "Bar till" {
		t.Fatalf("name = %q, want Bar till", got)
	}
	afterRename := generation()
	if afterRename == before {
		t.Fatal("a rename must bump sync_admin_version so joined tills pull the new roster")
	}

	changed, err = repo.UpdateName(ctx, id, "Bar till")
	if err != nil || changed {
		t.Fatalf("same-name UpdateName = %v, %v; want no change", changed, err)
	}
	if generation() != afterRename {
		t.Fatal("an unchanged name must not bump sync_admin_version")
	}

	changed, err = repo.UpdateName(ctx, "no-such-till", "Ghost")
	if err != nil || changed {
		t.Fatalf("unknown till UpdateName = %v, %v; want no change, no error", changed, err)
	}
}

// ut-docs#2781: a till's role is set at enrolment and changed later from the
// Tills page. InsertTill persists the chosen role (a till enrolled before
// migration 067 reads as additional, today's behaviour); UpdateRole mirrors
// UpdateName's write-only-on-a-real-change rule, so a repeated save never
// bumps sync_admin_version (migration 067's role trigger), while a real
// change does — that bump is what sends the new role to every joined till.
func TestTillsRepo_RoleIsPersistedAndUpdatedOnlyOnARealChange(t *testing.T) {
	d := openMigratedDB(t, "tills-role.db")
	repo := NewTillsRepo(d.DB)
	ctx := context.Background()

	plain, err := repo.InsertTill(ctx, "Till 2", "hash-2", TillRoleAdditional)
	if err != nil {
		t.Fatal(err)
	}
	sat, err := repo.InsertTill(ctx, "Kiosk", "hash-3", TillRoleSatellite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InsertTill(ctx, "Bad", "hash-4", "cashier"); err == nil {
		t.Fatal("an unknown role must be refused")
	}

	roleOf := func(id string) string {
		t.Helper()
		role, ok, err := repo.RoleByID(ctx, id)
		if err != nil || !ok {
			t.Fatalf("RoleByID(%s) = %q, %v, %v", id, role, ok, err)
		}
		return role
	}
	if got := roleOf(plain); got != TillRoleAdditional {
		t.Fatalf("InsertTill role = %q, want %q", got, TillRoleAdditional)
	}
	if got := roleOf(sat); got != TillRoleSatellite {
		t.Fatalf("InsertTill(satellite) role = %q, want %q", got, TillRoleSatellite)
	}
	// A row written without a role (every till enrolled before migration
	// 067) is additional; the CHECK keeps any other value out at the
	// storage layer too.
	if _, err := d.Exec(`INSERT INTO tills (id, name, bearer_hash) VALUES ('legacy', 'Old till', 'hash-legacy')`); err != nil {
		t.Fatal(err)
	}
	if got := roleOf("legacy"); got != TillRoleAdditional {
		t.Fatalf("pre-067 row role = %q, want additional", got)
	}
	if _, err := d.Exec(`UPDATE tills SET role = 'register' WHERE id = 'legacy'`); err == nil {
		t.Fatal("the tills.role CHECK accepted 'register'")
	}
	list, err := repo.ListTills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list {
		if row.ID == sat && row.Role != TillRoleSatellite {
			t.Fatalf("ListTills role for the satellite = %q", row.Role)
		}
	}
	if _, ok, err := repo.RoleByID(ctx, "no-such-till"); err != nil || ok {
		t.Fatalf("RoleByID(unknown) ok=%v err=%v, want not found", ok, err)
	}

	generation := func() int64 {
		t.Helper()
		var g int64
		if err := d.QueryRow(`SELECT generation FROM sync_admin_version WHERE id = 1`).Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	before := generation()
	changed, err := repo.UpdateRole(ctx, plain, TillRoleSatellite)
	if err != nil || !changed {
		t.Fatalf("UpdateRole = %v, %v; want changed", changed, err)
	}
	if got := roleOf(plain); got != TillRoleSatellite {
		t.Fatalf("role after UpdateRole = %q", got)
	}
	after := generation()
	if after == before {
		t.Fatal("a role change must bump sync_admin_version so joined tills learn it")
	}
	changed, err = repo.UpdateRole(ctx, plain, TillRoleSatellite)
	if err != nil || changed {
		t.Fatalf("same-role UpdateRole = %v, %v; want no change", changed, err)
	}
	if generation() != after {
		t.Fatal("an unchanged role must not bump sync_admin_version")
	}
	if _, err := repo.UpdateRole(ctx, plain, "register"); err == nil {
		t.Fatal("UpdateRole must refuse an unknown role")
	}
	changed, err = repo.UpdateRole(ctx, "no-such-till", TillRoleAdditional)
	if err != nil || changed {
		t.Fatalf("unknown till UpdateRole = %v, %v; want no change, no error", changed, err)
	}
}

func TestValidTillRole(t *testing.T) {
	for role, want := range map[string]bool{
		TillRoleAdditional: true, TillRoleSatellite: true,
		"": false, "replica": false, "Satellite": false, "main": false,
	} {
		if got := ValidTillRole(role); got != want {
			t.Errorf("ValidTillRole(%q) = %v, want %v", role, got, want)
		}
	}
}
