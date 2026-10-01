package db

import (
	"strings"
	"testing"
)

// itemsNetQuantityMigrationVersion is 060_items_net_quantity.sql
// (ut-docs#3391; renumbered twice during rebase — first 055→059 when main's
// 055_items_age_restricted.sql — ut-docs#3340 — claimed 055 first, then
// 059→060 when main's 059_fiscal_order_starts.sql claimed 059 first).
// Migration files are never renumbered once merged;
// TestMigration060_IsOnDisk pins this constant to that filename.
const itemsNetQuantityMigrationVersion = 60

func TestMigration060_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, itemsNetQuantityMigrationVersion)
	if m.Name != "060_items_net_quantity.sql" {
		t.Fatalf("migration %d on disk is %q, want 060_items_net_quantity.sql", itemsNetQuantityMigrationVersion, m.Name)
	}
}

// TestMigration060_NetQuantityColumns: items gains two nullable columns,
// net_quantity_value and net_quantity_unit (CHECK g/ml/ea). An existing
// item keeps both NULL (= no net quantity configured, label unchanged); the
// CHECK refuses any other unit; replay is safe (the runner skips the
// existing columns).
func TestMigration060_NetQuantityColumns(t *testing.T) {
	d, path := openAtPreMigrationSchema(t, itemsNetQuantityMigrationVersion, "m060.db")
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price) VALUES ('pre', 'PRE', 'Rice', 200)`); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (runs 060): %v", err)
	}
	defer d.Close()

	var v, u any
	if err := d.DB.QueryRow(`SELECT net_quantity_value, net_quantity_unit FROM items WHERE id = 'pre'`).Scan(&v, &u); err != nil {
		t.Fatalf("select new columns: %v", err)
	}
	if v != nil || u != nil {
		t.Fatalf("existing item got value=%v unit=%v, want both NULL", v, u)
	}
	for _, unit := range []string{"g", "ml", "ea"} {
		if _, err := d.DB.Exec(`UPDATE items SET net_quantity_value = 500, net_quantity_unit = ? WHERE id = 'pre'`, unit); err != nil {
			t.Fatalf("unit %q refused: %v", unit, err)
		}
	}
	_, err = d.DB.Exec(`UPDATE items SET net_quantity_unit = 'kg' WHERE id = 'pre'`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "check") {
		t.Fatalf("unit 'kg' = %v, want a CHECK constraint failure", err)
	}

	m := loadMigrationVersion(t, itemsNetQuantityMigrationVersion)
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version = ?`, itemsNetQuantityMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("replaying 060: %v", err)
	}
	for _, c := range []string{"net_quantity_value", "net_quantity_unit"} {
		if n := columnCount(t, d, "items", c); n != 1 {
			t.Fatalf("items.%s count = %d after replay", c, n)
		}
	}
}
