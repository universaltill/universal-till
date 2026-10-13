package db

import (
	"strings"
	"testing"
)

// itemsItemTypeMigrationVersion is 071_items_item_type.sql (ut-docs#4214,
// the gift voucher item type).
const itemsItemTypeMigrationVersion = 71

func TestMigration071_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, itemsItemTypeMigrationVersion)
	if m.Name != "071_items_item_type.sql" {
		t.Fatalf("migration %d on disk is %q, want 071_items_item_type.sql", itemsItemTypeMigrationVersion, m.Name)
	}
}

// TestMigration071_ItemTypeColumn: items gains item_type, NOT NULL DEFAULT
// 'standard' with a CHECK on the closed vocabulary. An existing item reads
// 'standard' (sells exactly as before); 'gift_voucher' is accepted; any
// other value is refused.
func TestMigration071_ItemTypeColumn(t *testing.T) {
	d, path := openAtPreMigrationSchema(t, itemsItemTypeMigrationVersion, "m071.db")
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price) VALUES ('pre', 'PRE', 'Rice', 200)`); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (runs 071): %v", err)
	}
	defer d.Close()

	var typ string
	if err := d.DB.QueryRow(`SELECT item_type FROM items WHERE id = 'pre'`).Scan(&typ); err != nil {
		t.Fatalf("select item_type: %v", err)
	}
	if typ != "standard" {
		t.Fatalf("existing item item_type = %q, want standard", typ)
	}
	if _, err := d.DB.Exec(`UPDATE items SET item_type = 'gift_voucher' WHERE id = 'pre'`); err != nil {
		t.Fatalf("gift_voucher refused: %v", err)
	}
	_, err = d.DB.Exec(`UPDATE items SET item_type = 'deposit' WHERE id = 'pre'`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "check") {
		t.Fatalf("item_type 'deposit' = %v, want a CHECK constraint failure", err)
	}
}
