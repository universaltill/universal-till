package db

import (
	"path/filepath"
	"reflect"
	"testing"
)

// personalDisplayGrantsVersion is 102_personal_display_grants.sql
// (ut-docs#3440, ADR-0100). TestMigration102_IsOnDisk pins the filename.
const personalDisplayGrantsVersion = 102

func personalDisplayRoles(t *testing.T, d *DB, granted int) []string {
	t.Helper()
	rows, err := d.Query(`SELECT role FROM role_permissions WHERE action = 'personal_display' AND granted = ? ORDER BY role`, granted)
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
	return roles
}

func TestMigration102_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, personalDisplayGrantsVersion)
	if m.Name != "102_personal_display_grants.sql" {
		t.Fatalf("migration %d on disk is %q, want 102_personal_display_grants.sql", personalDisplayGrantsVersion, m.Name)
	}
}

// A fresh Open grants personal_display to admin/manager/super_admin only;
// cashier stays sale-only (#3079) and gets no row at all.
func TestMigration102_FreshOpenGrantsAdminManagerSuperAdminNotCashier(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m102.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	want := []string{"admin", "manager", "super_admin"}
	if got := personalDisplayRoles(t, d, 1); !reflect.DeepEqual(got, want) {
		t.Fatalf("granted roles = %v, want %v", got, want)
	}
	var cashier int
	if err := d.QueryRow(`SELECT COUNT(*) FROM role_permissions WHERE action = 'personal_display' AND role = 'cashier'`).Scan(&cashier); err != nil {
		t.Fatal(err)
	}
	if cashier != 0 {
		t.Fatalf("cashier has %d personal_display row(s), want none", cashier)
	}
}

// Replaying 102 is idempotent, and INSERT OR IGNORE never re-grants a role
// whose shop already revoked it on /users/permissions.
func TestMigration102_ReplayIdempotentAndKeepsRevocation(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m102-idem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`UPDATE role_permissions SET granted = 0 WHERE role = 'manager' AND action = 'personal_display'`); err != nil {
		t.Fatal(err)
	}
	m := loadMigrationVersion(t, personalDisplayGrantsVersion)
	for i := 1; i <= 2; i++ {
		tx, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := execMigrationStatements(tx, m); err != nil {
			tx.Rollback()
			t.Fatalf("replay %d: %v", i, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if got, want := personalDisplayRoles(t, d, 1), []string{"admin", "super_admin"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("after replay %d granted = %v, want %v", i, got, want)
		}
		if got, want := personalDisplayRoles(t, d, 0), []string{"manager"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("after replay %d revoked = %v, want %v (a revoked grant must stay revoked)", i, got, want)
		}
	}
}
