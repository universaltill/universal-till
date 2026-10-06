package data

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// ADR-0128 §6 "Admin-bundle prune" (ut-docs#3165): custom roles are created
// and deleted on the main till by cloud directives and reach every other
// till in the admin bundle. ApplyAdmin prunes a local origin='cloud' role
// the bundle no longer carries — its role_permissions rows first, then the
// roles row — while built-in roles keep #1589's never-prune rule and the
// role_permissions skew exemption stays for built-in (or unknown) roles
// only.

const syncTestCloudRole = "c_01j9z3k4m5n6p7q8r9s0t1v2w3"

// primaryCreateCloudRole writes a custom role the way the save_role apply
// does: one tx through AuthRepo.
func primaryCreateCloudRole(t *testing.T, d *db.DB, role, label string, grants []string) {
	t.Helper()
	ctx := context.Background()
	repo := NewAuthRepo(d.DB)
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := repo.UpsertCloudRoleTx(ctx, tx, role, label); err != nil {
		t.Fatalf("upsert cloud role: %v", err)
	}
	if err := repo.ReplaceRoleGrantsTx(ctx, tx, role, grants); err != nil {
		t.Fatalf("replace grants: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func primaryDeleteCloudRole(t *testing.T, d *db.DB, role string) {
	t.Helper()
	ctx := context.Background()
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := NewAuthRepo(d.DB).DeleteCloudRoleTx(ctx, tx, role); err != nil {
		t.Fatalf("delete cloud role: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, d *db.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func pullAdmin(t *testing.T, primary, replica *db.DB) {
	t.Helper()
	ctx := context.Background()
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

func TestAdminSync_CustomRoleCreateThenDeleteOnMain(t *testing.T) {
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	primaryCreateCloudRole(t, primary, syncTestCloudRole, "Shift lead", []string{"refund", "audit"})
	// A user on the main till holds the custom role; the bundle carries it.
	mustExec(t, primary, `INSERT INTO users (id, username, display_name, role) VALUES ('u-lead', 'lead', 'Lead', ?)`, syncTestCloudRole)
	pullAdmin(t, primary, replica)

	var label, origin string
	if err := replica.DB.QueryRow(`SELECT label, origin FROM roles WHERE role = ?`, syncTestCloudRole).Scan(&label, &origin); err != nil {
		t.Fatalf("custom role did not reach the satellite: %v", err)
	}
	if label != "Shift lead" || origin != "cloud" {
		t.Fatalf("satellite role label=%q origin=%q", label, origin)
	}
	if n := countRows(t, replica, `SELECT COUNT(*) FROM role_permissions WHERE role = ? AND granted = 1`, syncTestCloudRole); n != 2 {
		t.Fatalf("satellite custom grants = %d, want 2", n)
	}
	if ok, _ := NewAuthRepo(replica.DB).HasPermission(context.Background(), syncTestCloudRole, "refund"); !ok {
		t.Fatal("custom role must be enforced on the satellite")
	}

	// Satellite-side state the prune must leave alone or still heal:
	//   * a built-in role from a newer replica migration, with a grant
	//     (#1589 skew) — survives;
	//   * a fabricated grant on a role/action the primary knows — healed;
	//   * a grant of the custom role on an action only this (newer) replica
	//     knows — must go with its role, or the roles DELETE hits the FK.
	mustExec(t, replica, `INSERT INTO roles (role) VALUES ('shift_lead')`)
	mustExec(t, replica, `INSERT INTO permission_actions (action) VALUES ('inventory_count')`)
	mustExec(t, replica, `INSERT INTO role_permissions (role, action, granted) VALUES ('shift_lead', 'refund', 1)`)
	mustExec(t, replica, `INSERT INTO role_permissions (role, action, granted) VALUES (?, 'inventory_count', 1)`, syncTestCloudRole)
	mustExec(t, replica, `INSERT INTO role_permissions (role, action, granted) VALUES ('cashier', 'audit', 1)
		ON CONFLICT (role, action) DO UPDATE SET granted = 1`)

	// The main till moves the user off the role (delete is refused while
	// held), then deletes it.
	mustExec(t, primary, `UPDATE users SET role = 'cashier' WHERE id = 'u-lead'`)
	primaryDeleteCloudRole(t, primary, syncTestCloudRole)
	pullAdmin(t, primary, replica)

	if n := countRows(t, replica, `SELECT COUNT(*) FROM roles WHERE role = ?`, syncTestCloudRole); n != 0 {
		t.Errorf("deleted custom role still on the satellite (%d)", n)
	}
	if n := countRows(t, replica, `SELECT COUNT(*) FROM role_permissions WHERE role = ?`, syncTestCloudRole); n != 0 {
		t.Errorf("deleted custom role's grants still on the satellite (%d)", n)
	}
	var userRole string
	if err := replica.DB.QueryRow(`SELECT role FROM users WHERE id = 'u-lead'`).Scan(&userRole); err != nil || userRole != "cashier" {
		t.Errorf("user role on satellite = %q (%v), want cashier", userRole, err)
	}
	if n := countRows(t, replica, `SELECT COUNT(*) FROM roles WHERE role = 'shift_lead'`); n != 1 {
		t.Error("a built-in role from a newer replica must survive (#1589)")
	}
	if n := countRows(t, replica, `SELECT COUNT(*) FROM role_permissions WHERE role = 'shift_lead' AND action = 'refund' AND granted = 1`); n != 1 {
		t.Error("a built-in skew grant must survive (#1589)")
	}
	if n := countRows(t, replica, `SELECT COUNT(*) FROM role_permissions WHERE role = 'cashier' AND action = 'audit' AND granted = 1`); n != 0 {
		t.Error("a fabricated grant on a known role/action must still be healed (#1554)")
	}
}

// A satellite that never saw the role keeps nothing; a local cloud role
// the bundle carries is kept even if the bundle comes from an older
// primary without the label/origin columns (tableColumns drops nothing it
// needs: the key is in the bundle, so nothing is pruned).
func TestAdminSync_OlderPrimaryBundleWithoutLabelOriginApplies(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	primaryCreateCloudRole(t, primary, syncTestCloudRole, "Shift lead", []string{"refund"})
	pullAdmin(t, primary, replica)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bundle = wireTrip(t, bundle)
	for _, rec := range bundle.Tables["roles"] {
		if _, ok := rec["origin"]; !ok {
			t.Fatal("DumpAdmin must carry roles.origin")
		}
		if _, ok := rec["label"]; !ok {
			t.Fatal("DumpAdmin must carry roles.label")
		}
		delete(rec, "origin")
		delete(rec, "label")
	}
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, bundle); err != nil {
		t.Fatalf("apply older-shape bundle: %v", err)
	}
	var label, origin string
	if err := replica.DB.QueryRow(`SELECT label, origin FROM roles WHERE role = ?`, syncTestCloudRole).Scan(&label, &origin); err != nil {
		t.Fatalf("cloud role pruned by an older-shape bundle that still lists it: %v", err)
	}
	if label != "Shift lead" || origin != "cloud" {
		t.Fatalf("older-shape bundle rewrote label=%q origin=%q", label, origin)
	}
}

// A bundle without a roles table (never sent today, but ApplyAdmin leaves
// absent tables untouched) prunes no role.
func TestAdminSync_BundleWithoutRolesPrunesNoRole(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	primaryCreateCloudRole(t, primary, syncTestCloudRole, "Shift lead", []string{"refund"})
	pullAdmin(t, primary, replica)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bundle = wireTrip(t, bundle)
	delete(bundle.Tables, "roles")
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, bundle); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n := countRows(t, replica, `SELECT COUNT(*) FROM roles WHERE role = ?`, syncTestCloudRole); n != 1 {
		t.Fatal("a bundle without roles must not prune any role")
	}
}

// A bundle that carries `roles` but no rows (impossible from a healthy
// primary, which always has the seeded built-in roles) must not be read as
// "the primary deleted every custom role" (#3165 review, nit 5).
func TestAdminSync_EmptyRolesTablePrunesNoRole(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	primaryCreateCloudRole(t, primary, syncTestCloudRole, "Shift lead", []string{"refund"})
	pullAdmin(t, primary, replica)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bundle = wireTrip(t, bundle)
	bundle.Tables["roles"] = []map[string]any{}
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, bundle); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n := countRows(t, replica, `SELECT COUNT(*) FROM roles WHERE role = ?`, syncTestCloudRole); n != 1 {
		t.Fatal("an empty roles table in the bundle must not prune any role")
	}
}
