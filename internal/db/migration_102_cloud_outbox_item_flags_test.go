package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// ut-docs#6082/#6202: migration 102 makes the item flag and net-quantity
// columns upload (the update trigger now watches them) and fills the four
// fields into the upload bases (cloud_links.base) that lack them, from the
// local row, so an edit made between the upgrade and the first cloud tick
// still has a base to differ from.
func TestMigration102BackfillsItemBases_6082(t *testing.T) {
	loadMigrationVersion(t, 102) // fails loudly if renumbered off disk
	path := filepath.Join(t.TempDir(), "outbox-item-flags.db")
	d := openMigratedTo(t, path, 101)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO items (id, sku, name, base_price, is_weighed, age_restricted, net_quantity_value, net_quantity_unit) VALUES
 ('i-old',  'S1', 'Cola',  250, 1, 0, 330, 'ml'),
 ('i-none', 'S2', 'Bread', 200, 0, 1, NULL, NULL),
 ('i-has',  'S3', 'Milk',  100, 1, 1, 500, 'g'),
 ('i-bad',  'S4', 'Tea',   100, 1, 1, 500, 'g'),
 ('i-nobase', 'S5', 'Nobase', 100, 1, 1, 500, 'g')`)
	const old = `{"name":"x","price_minor":250,"barcodes":[]}`
	exec(`INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at, base) VALUES
 ('item', 'c-old',  'i-old',  1, 'now', ?),
 ('item', 'c-none', 'i-none', 1, 'now', ?),
 ('item', 'c-has',  'i-has',  1, 'now', '{"name":"x","is_weighed":false,"age_restricted":false,"net_quantity_value":null,"net_quantity_unit":null}'),
 ('item', 'c-bad',  'i-bad',  1, 'now', 'not json'),
 ('item', 'c-nobase', 'i-nobase', 1, 'now', NULL),
 ('item', 'c-gone', 'i-gone', 1, 'now', ?),
 ('category', 'cat-1', 'i-old', 1, 'now', ?)`, old, old, old, old)
	d.Close()

	d = openMigratedTo(t, path, 102)
	defer d.Close()
	base := func(cloudID, entity string) sql.NullString {
		t.Helper()
		var b sql.NullString
		if err := d.DB.QueryRow(`SELECT base FROM cloud_links WHERE entity = ? AND cloud_id = ?`, entity, cloudID).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	field := func(cloudID, path string) sql.NullString {
		t.Helper()
		var v sql.NullString
		if err := d.DB.QueryRow(`SELECT json_extract(base, ?) FROM cloud_links WHERE entity = 'item' AND cloud_id = ?`, path, cloudID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	// A base without the fields gets them from the local row.
	if got := field("c-old", "$.is_weighed").String; got != "1" {
		t.Fatalf("c-old is_weighed = %q, want 1 (JSON true)", got)
	}
	var typ string
	if err := d.DB.QueryRow(`SELECT json_type(base, '$.is_weighed') FROM cloud_links WHERE cloud_id = 'c-old'`).Scan(&typ); err != nil || typ != "true" {
		t.Fatalf("c-old is_weighed json type = %q, %v; want true", typ, err)
	}
	if got := field("c-old", "$.age_restricted"); got.String != "0" {
		t.Fatalf("c-old age_restricted = %v, want 0 (JSON false)", got)
	}
	if got := field("c-old", "$.net_quantity_value"); got.String != "330" {
		t.Fatalf("c-old net_quantity_value = %v, want 330", got)
	}
	if got := field("c-old", "$.net_quantity_unit"); got.String != "ml" {
		t.Fatalf("c-old net_quantity_unit = %v, want ml", got)
	}
	if got := field("c-old", "$.name"); got.String != "x" {
		t.Fatalf("c-old name = %v, the base's other fields must stay", got)
	}
	// No net quantity: the pair is JSON null (the key is present).
	var nullType string
	if err := d.DB.QueryRow(`SELECT json_type(base, '$.net_quantity_value') FROM cloud_links WHERE cloud_id = 'c-none'`).Scan(&nullType); err != nil || nullType != "null" {
		t.Fatalf("c-none net_quantity_value json type = %q, %v; want null", nullType, err)
	}
	if err := d.DB.QueryRow(`SELECT json_type(base, '$.net_quantity_unit') FROM cloud_links WHERE cloud_id = 'c-none'`).Scan(&nullType); err != nil || nullType != "null" {
		t.Fatalf("c-none net_quantity_unit json type = %q, %v; want null", nullType, err)
	}
	if got := field("c-none", "$.age_restricted"); got.String != "1" {
		t.Fatalf("c-none age_restricted = %v, want 1", got)
	}

	// A base that already has the fields, a damaged one, a NULL one, a
	// link whose item is gone and a non-item link stay as they were.
	if got := base("c-has", "item").String; got != `{"name":"x","is_weighed":false,"age_restricted":false,"net_quantity_value":null,"net_quantity_unit":null}` {
		t.Fatalf("c-has base changed: %s", got)
	}
	if got := base("c-bad", "item").String; got != "not json" {
		t.Fatalf("a damaged base changed: %q", got)
	}
	if b := base("c-nobase", "item"); b.Valid {
		t.Fatalf("a NULL base was filled: %q", b.String)
	}
	if got := base("c-gone", "item").String; got != old {
		t.Fatalf("a link without its item changed: %q", got)
	}
	if got := base("cat-1", "category").String; got != old {
		t.Fatalf("a category link changed: %q", got)
	}

	// Re-running the migration changes nothing (the backfill only fills
	// bases that lack the fields).
	before := base("c-old", "item").String
	if _, err := d.DB.Exec(loadMigrationVersion(t, 102).SQL); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if after := base("c-old", "item").String; after != before {
		t.Fatalf("re-run changed the base: %s -> %s", before, after)
	}

	// The update trigger fires on each new column.
	exec2 := func(q string) {
		t.Helper()
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec2(`INSERT INTO settings (key, value) VALUES ('entitlement.catalog_master', 'cloud')`)
	for _, col := range []string{"is_weighed = 0", "age_restricted = 0", "net_quantity_value = 1", "net_quantity_unit = 'g'"} {
		exec2(`DELETE FROM cloud_outbox`)
		exec2(`UPDATE items SET ` + col + ` WHERE id = 'i-old'`)
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM cloud_outbox WHERE entity = 'item' AND local_id = 'i-old'`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s: marks = %d, %v; want 1", col, n, err)
		}
	}
}
