package db

import (
	"path/filepath"
	"reflect"
	"testing"
)

// ut-docs#3560: 098_no_sale_permission.sql adds the dedicated no_sale action,
// mirroring every cash_adjustment holder (ADR-0100: append-only).

func noSaleHolders(t *testing.T, d *DB) []string {
	t.Helper()
	rows, err := d.Query(`SELECT role FROM role_permissions WHERE action = 'no_sale' AND granted = 1 ORDER BY role`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestMigration098_FreshOpenSeedsNoSale(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m098.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if n := scalarInt(t, d, `SELECT COUNT(*) FROM permission_actions WHERE action = 'no_sale'`); n != 1 {
		t.Fatalf("no_sale action rows = %d, want 1", n)
	}
	if got, want := noSaleHolders(t, d), []string{"admin", "manager", "super_admin"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("no_sale holders = %v, want %v (cashier must not hold it)", got, want)
	}
}

func TestMigration098_UpgradeMirrorsCashAdjustment(t *testing.T) {
	d, path := openAtPreMigrationSchema(t, 98, "m098-upgrade.db")
	if _, err := d.DB.Exec(`
INSERT INTO roles (role, origin) VALUES ('c_x', 'cloud');
INSERT INTO role_permissions (role, action, granted) VALUES ('c_x', 'cash_adjustment', 1);
UPDATE role_permissions SET granted = 0 WHERE role = 'manager' AND action = 'cash_adjustment';`); err != nil {
		t.Fatal(err)
	}
	gen := scalarInt(t, d, `SELECT generation FROM sync_admin_version WHERE id = 1`)
	d.Close()

	d = openMigratedTo(t, path, 98)
	defer d.Close()
	if got, want := noSaleHolders(t, d), []string{"admin", "c_x", "super_admin"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("no_sale holders = %v, want %v (manager's revoked cash_adjustment must not widen)", got, want)
	}
	if g := scalarInt(t, d, `SELECT generation FROM sync_admin_version WHERE id = 1`); g <= gen {
		t.Fatalf("sync generation %d, want > %d", g, gen)
	}
	// Idempotent.
	before := scalarInt(t, d, `SELECT COUNT(*) FROM role_permissions`)
	if _, err := d.DB.Exec(loadMigrationVersion(t, 98).SQL); err != nil {
		t.Fatal(err)
	}
	if after := scalarInt(t, d, `SELECT COUNT(*) FROM role_permissions`); after != before {
		t.Fatalf("re-run changed role_permissions %d -> %d", before, after)
	}
}
