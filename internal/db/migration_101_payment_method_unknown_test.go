package db

import (
	"path/filepath"
	"testing"
)

func TestMigration101_PaymentMethodUnknownPlaceholder(t *testing.T) {
	// Opened at 100, the schema just before migration 101.
	path := filepath.Join(t.TempDir(), "pm-unknown.db")
	d := openMigratedTo(t, path, 100)
	stmts := `
INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES
 ('sumup', 'sumup', 'cash', 1, 0, NULL),
 ('okc', 'okc~okc', 'cash', 0, 0, NULL),
 ('cashplug', 'cashplug', 'cash', 1, 100, 'com.x.p'),
 ('qr', 'qr.label', 'cash', 1, 101, NULL),
 ('cashback-voucher', 'cashback-voucher', 'voucher', 1, 0, NULL);
`
	if _, err := d.DB.Exec(stmts); err != nil {
		t.Fatal(err)
	}
	// The seeded cash row, and a variant proving the id clause.
	if _, err := d.DB.Exec(`UPDATE payment_methods SET name = 'Cash', sort_order = 0 WHERE id = 'cash'`); err != nil {
		t.Fatal(err)
	}
	genBefore := scalarInt(t, d, `SELECT generation FROM sync_admin_version WHERE id = 1`)
	d.Close()

	d = openMigratedTo(t, path, 101)
	defer d.Close()

	typeOf := func(id string) string {
		var typ string
		if err := d.DB.QueryRow(`SELECT type FROM payment_methods WHERE id = ?`, id).Scan(&typ); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return typ
	}
	want := map[string]string{
		"sumup": "unknown", "okc": "unknown",
		"cash": "cash", "card": "card", "gift": "voucher", "voucher": "voucher",
		"cashplug": "cash", "qr": "cash", "cashback-voucher": "voucher",
	}
	for id, w := range want {
		var n int
		_ = d.DB.QueryRow(`SELECT COUNT(*) FROM payment_methods WHERE id = ?`, id).Scan(&n)
		if n == 0 {
			continue // not every seeded id exists in every schema
		}
		if got := typeOf(id); got != w {
			t.Errorf("%s: type=%q, want %q", id, got, w)
		}
	}
	if got := scalarInt(t, d, `SELECT generation FROM sync_admin_version WHERE id = 1`); got != genBefore+2 {
		t.Errorf("sync_admin_version.generation=%d, want %d (one bump per repaired row)", got, genBefore+2)
	}
}
