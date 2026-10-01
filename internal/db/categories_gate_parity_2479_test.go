package db

import (
	"path/filepath"
	"testing"
)

// TestSeededRoles_SettingsAndCatalogManagementCoOccur pins the premise
// ut-docs#2479 rests on: /categories moved its gate from "settings" to
// "catalog_management" without changing who can reach it, because every
// migration-seeded role holds both or neither. A future migration that
// grants or revokes one of them for a seeded role without the other would
// silently move /categories access for that role — this test catches it.
func TestSeededRoles_SettingsAndCatalogManagementCoOccur(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "parity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	rows, err := d.Query(`
		SELECT r.role,
		       COALESCE((SELECT granted FROM role_permissions p WHERE p.role = r.role AND p.action = 'settings'), 0),
		       COALESCE((SELECT granted FROM role_permissions p WHERE p.role = r.role AND p.action = 'catalog_management'), 0)
		FROM (SELECT DISTINCT role FROM role_permissions) r
		ORDER BY r.role`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	granted := map[string]bool{}
	for rows.Next() {
		var role string
		var settings, catalog int
		if err := rows.Scan(&role, &settings, &catalog); err != nil {
			t.Fatal(err)
		}
		seen++
		if settings != catalog {
			t.Errorf("seeded role %q: settings granted=%d, catalog_management granted=%d — they must co-occur (ut-docs#2479)", role, settings, catalog)
		}
		granted[role] = catalog == 1
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("no seeded roles found in role_permissions")
	}
	for _, role := range []string{"admin", "manager", "super_admin"} {
		if !granted[role] {
			t.Errorf("seeded role %q lacks catalog_management, so it would lose /categories (ut-docs#2479)", role)
		}
	}
	if granted["cashier"] {
		t.Error("cashier holds catalog_management, so it would gain /categories (ut-docs#2479)")
	}
}
