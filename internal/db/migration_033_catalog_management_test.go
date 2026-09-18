package db

import (
	"path/filepath"
	"testing"
)

// catalogManagementMigrationVersion is 033_catalog_management_permission.sql
// (ut-docs#2395, ADR-0100). Migration files are never renumbered once
// merged; TestMigration033_IsOnDisk pins this constant to that filename.
const catalogManagementMigrationVersion = 33

// catalogManagementCounts returns (permission_actions rows named
// catalog_management, role_permissions rows granting it) — the two numbers
// every test in this file pins to exactly 1 and 3.
func catalogManagementCounts(t *testing.T, d *DB) (actions, grants int) {
	t.Helper()
	if err := d.QueryRow(`SELECT COUNT(*) FROM permission_actions WHERE action = 'catalog_management'`).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM role_permissions WHERE action = 'catalog_management' AND granted = 1`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	return actions, grants
}

// TestMigration033_FreshOpenSeedsCatalogManagement: ut-docs#2312 introduced
// the catalog_management permission by editing the applied baseline
// (ut-docs#2395). After 001_init.sql was restored to its v0.18.0 content,
// 033 is what actually delivers the action row and the three grants —
// exactly one action row, exactly the three roles, cashier NOT granted.
func TestMigration033_FreshOpenSeedsCatalogManagement(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m033.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if actions, grants := catalogManagementCounts(t, d); actions != 1 || grants != 3 {
		t.Fatalf("catalog_management rows after fresh Open = %d action / %d grants, want 1 / 3", actions, grants)
	}
	rows, err := d.Query(`SELECT role FROM role_permissions WHERE action = 'catalog_management' AND granted = 1 ORDER BY role`)
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
	if err := d.QueryRow(`SELECT COUNT(*) FROM role_permissions WHERE action = 'catalog_management' AND role = 'cashier'`).Scan(&cashier); err != nil {
		t.Fatal(err)
	}
	if cashier != 0 {
		t.Fatalf("cashier has %d catalog_management row(s), want none (same shape as tax_code_management)", cashier)
	}
}

// TestMigration033_IsOnDisk guards the number this file's sibling tests
// hardcode: version 033 must be THIS file, so a concurrent card that lands
// a 033 of its own is caught here rather than by a confusing checksum
// mismatch on a till.
func TestMigration033_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, catalogManagementMigrationVersion)
	if m.Name != "033_catalog_management_permission.sql" {
		t.Fatalf("migration %d on disk is %q, want 033_catalog_management_permission.sql", catalogManagementMigrationVersion, m.Name)
	}
}

// TestMigration033_IsIdempotent replays the shipped 033 statements a second
// (and third) time against a DB that already has the rows — the exact
// state of a fresh v0.19.0–v0.19.2 install, whose edited 001 already
// carried them (ut-docs#2395) — and the counts must not move. INSERT OR
// IGNORE against the two tables' PRIMARY KEYs is what makes this hold;
// a plain INSERT here would fail the second run outright.
func TestMigration033_IsIdempotent(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m033-idem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	m := loadMigrationVersion(t, catalogManagementMigrationVersion)
	for i := 1; i <= 2; i++ {
		tx, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := execMigrationStatements(tx, m); err != nil {
			tx.Rollback()
			t.Fatalf("replay %d of 033 must not fail: %v", i, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if actions, grants := catalogManagementCounts(t, d); actions != 1 || grants != 3 {
			t.Fatalf("after replay %d: catalog_management rows = %d action / %d grants, want 1 / 3 unchanged", i, actions, grants)
		}
	}
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM role_permissions`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	// 54 baseline grants (001 as shipped in v0.18.0) + the 3 from 033.
	if total != 57 {
		t.Fatalf("role_permissions total = %d after replays, want 57 (54 baseline + 3 from 033)", total)
	}
}
