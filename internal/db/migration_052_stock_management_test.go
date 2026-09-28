package db

import (
	"path/filepath"
	"testing"
)

// stockManagementMigrationVersion is 052_stock_management_permission.sql
// (ut-docs#3079, ADR-0100). Migration files are never renumbered once
// merged; TestMigration052_IsOnDisk pins this constant to that filename.
const stockManagementMigrationVersion = 52

// stockManagementCounts returns (permission_actions rows named
// stock_management, role_permissions rows granting it).
func stockManagementCounts(t *testing.T, d *DB) (actions, grants int) {
	t.Helper()
	if err := d.QueryRow(`SELECT COUNT(*) FROM permission_actions WHERE action = 'stock_management'`).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM role_permissions WHERE action = 'stock_management' AND granted = 1`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	return actions, grants
}

// TestMigration052_FreshOpenSeedsStockManagement: ut-docs#3079 gates the
// stock page (/inventory) and the goods-in / stock-override writes on a new
// stock_management action. A fresh Open (the real migration runner) must
// carry exactly one action row granted to admin, manager and super_admin —
// and NOT to cashier, who must stay sale-only.
func TestMigration052_FreshOpenSeedsStockManagement(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m052.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if actions, grants := stockManagementCounts(t, d); actions != 1 || grants != 3 {
		t.Fatalf("stock_management rows after fresh Open = %d action / %d grants, want 1 / 3", actions, grants)
	}
	rows, err := d.Query(`SELECT role FROM role_permissions WHERE action = 'stock_management' AND granted = 1 ORDER BY role`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		roles = append(roles, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"admin", "manager", "super_admin"}
	if len(roles) != len(want) {
		t.Fatalf("granted roles = %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("granted roles = %v, want %v", roles, want)
		}
	}
	var cashier int
	if err := d.QueryRow(`SELECT COUNT(*) FROM role_permissions WHERE action = 'stock_management' AND role = 'cashier'`).Scan(&cashier); err != nil {
		t.Fatal(err)
	}
	if cashier != 0 {
		t.Fatalf("cashier has %d stock_management row(s), want none (cashier is sale-only, #3079)", cashier)
	}
}

// TestMigration052_IsOnDisk guards the number this file's sibling tests
// hardcode: version 052 must be THIS file.
func TestMigration052_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, stockManagementMigrationVersion)
	if m.Name != "052_stock_management_permission.sql" {
		t.Fatalf("migration %d on disk is %q, want 052_stock_management_permission.sql", stockManagementMigrationVersion, m.Name)
	}
}

// TestMigration052_IsIdempotent replays 052 against a database that already
// has its rows; INSERT OR IGNORE against the PRIMARY KEYs keeps the counts
// unchanged.
func TestMigration052_IsIdempotent(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m052-idem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	m := loadMigrationVersion(t, stockManagementMigrationVersion)
	for i := 1; i <= 2; i++ {
		tx, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := execMigrationStatements(tx, m); err != nil {
			tx.Rollback()
			t.Fatalf("replay %d of 052 must not fail: %v", i, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if actions, grants := stockManagementCounts(t, d); actions != 1 || grants != 3 {
			t.Fatalf("after replay %d: stock_management rows = %d action / %d grants, want 1 / 3 unchanged", i, actions, grants)
		}
	}
}
