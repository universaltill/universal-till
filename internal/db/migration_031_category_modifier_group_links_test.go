package db

import (
	"path/filepath"
	"testing"
)

// categoryModifierGroupLinksMigrationVersion is
// 031_category_modifier_group_links.sql (ADR-0094, ut-docs#1915). If the
// file is ever renumbered, this constant moves with it.
const categoryModifierGroupLinksMigrationVersion = 31

// TestMigration031_CreatesBothTablesAndIsReplaySafe pins ADR-0094's
// migration contract: both new tables exist with the ADR's exact columns and
// composite primary keys, each carries its FK cascades (deleting a category
// or a group takes only that side's attachment rows with it — the group row
// itself is never touched, ADR-0094 Decision 1), each has its three
// sync_admin_version triggers (023's header: "a table added to adminTables
// later needs its own three triggers in a NEW migration"), and the file is
// re-appliable (openAtPreMigrationSchema's contract for every later
// migration) without error.
func TestMigration031_CreatesBothTablesAndIsReplaySafe(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "cat-links.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := d.DB.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}

	var applied int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, categoryModifierGroupLinksMigrationVersion).Scan(&applied); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if applied != 1 {
		t.Fatalf("migration %d not recorded as applied on a fresh DB — has it been renumbered?", categoryModifierGroupLinksMigrationVersion)
	}

	// (a) shape: columns, NOT NULL, and composite PK per the ADR's DDL.
	type col struct {
		notNull, pk int
	}
	wantTables := map[string]map[string]col{
		"category_modifier_group_links": {
			"category_id": {1, 1},
			"group_id":    {1, 2},
			"sort_order":  {1, 0},
		},
		"item_modifier_group_opt_outs": {
			"item_id":  {1, 1},
			"group_id": {1, 2},
		},
	}
	for table, wantCols := range wantTables {
		rows, err := d.DB.Query(`SELECT name, "notnull", pk FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("pragma_table_info(%s): %v", table, err)
		}
		got := map[string]col{}
		for rows.Next() {
			var name string
			var c col
			if err := rows.Scan(&name, &c.notNull, &c.pk); err != nil {
				t.Fatal(err)
			}
			got[name] = c
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if len(got) != len(wantCols) {
			t.Fatalf("%s columns = %v, want %v", table, got, wantCols)
		}
		for name, want := range wantCols {
			if got[name] != want {
				t.Fatalf("%s.%s = %+v, want %+v (full: %v)", table, name, got[name], want, got)
			}
		}
		if n := count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ?`, table); n != 3 {
			t.Fatalf("sync_admin_version triggers on %s = %d, want 3 (ins/upd/del — see 023's header)", table, n)
		}
	}
	for _, idx := range []string{"idx_category_modifier_group_links_group", "idx_item_modifier_group_opt_outs_group"} {
		if n := count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx); n != 1 {
			t.Fatalf("index %s count = %d, want 1", idx, n)
		}
	}

	// (b) FK cascades: a category delete removes only its link rows; a
	// group delete removes both its category links and its opt-outs; the
	// group row survives a category delete untouched.
	exec(`INSERT INTO categories (id, name) VALUES ('cat1', 'Drinks')`)
	exec(`INSERT INTO items (id, sku, name, base_price, category_id) VALUES ('itm-a','A','Flat White',320,'cat1')`)
	exec(`INSERT INTO item_modifier_groups (id, item_id, name, required, min_select, max_select, sort_order, is_active)
	      VALUES ('g1','itm-a','Milk',0,0,1,0,1), ('g2','itm-a','Extras',0,0,2,1,1)`)
	exec(`INSERT INTO category_modifier_group_links (category_id, group_id, sort_order) VALUES ('cat1','g1',0), ('cat1','g2',1)`)
	exec(`INSERT INTO item_modifier_group_opt_outs (item_id, group_id) VALUES ('itm-a','g1'), ('itm-a','g2')`)
	if _, err := d.DB.Exec(`INSERT INTO category_modifier_group_links (category_id, group_id, sort_order) VALUES ('cat1','g1',9)`); err == nil {
		t.Fatal("duplicate (category_id, group_id) must violate the composite PK")
	}
	if _, err := d.DB.Exec(`INSERT INTO category_modifier_group_links (category_id, group_id, sort_order) VALUES ('no-such-cat','g1',0)`); err == nil {
		t.Fatal("category FK must be enforced")
	}
	if _, err := d.DB.Exec(`INSERT INTO item_modifier_group_opt_outs (item_id, group_id) VALUES ('itm-a','no-such-group')`); err == nil {
		t.Fatal("group FK must be enforced on opt-outs")
	}

	exec(`DELETE FROM item_modifier_groups WHERE id = 'g2'`)
	if n := count(`SELECT COUNT(*) FROM category_modifier_group_links WHERE group_id = 'g2'`); n != 0 {
		t.Fatalf("category links for a deleted group = %d, want 0 (ON DELETE CASCADE)", n)
	}
	if n := count(`SELECT COUNT(*) FROM item_modifier_group_opt_outs WHERE group_id = 'g2'`); n != 0 {
		t.Fatalf("opt-outs for a deleted group = %d, want 0 (ON DELETE CASCADE)", n)
	}

	exec(`UPDATE items SET category_id = NULL WHERE id = 'itm-a'`)
	exec(`DELETE FROM categories WHERE id = 'cat1'`)
	if n := count(`SELECT COUNT(*) FROM category_modifier_group_links`); n != 0 {
		t.Fatalf("category links after category delete = %d, want 0 (ON DELETE CASCADE)", n)
	}
	if n := count(`SELECT COUNT(*) FROM item_modifier_groups WHERE id = 'g1'`); n != 1 {
		t.Fatal("deleting a category must never delete a group row (ADR-0094 Decision 1)")
	}
	if n := count(`SELECT COUNT(*) FROM item_modifier_group_opt_outs WHERE item_id = 'itm-a' AND group_id = 'g1'`); n != 1 {
		t.Fatalf("opt-out keyed by (item, group) must survive a category delete, got %d", n)
	}

	// (c) replay: rewind only the ledger row (rows and tables stay, exactly
	// what a renumbered/pre-merge build leaves behind on a device) and apply
	// the same file again — no error, existing rows intact, ledger has it
	// exactly once, triggers not duplicated.
	m := loadMigrationVersion(t, categoryModifierGroupLinksMigrationVersion)
	exec(`DELETE FROM schema_migrations WHERE version = ?`, categoryModifierGroupLinksMigrationVersion)
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("replaying %d against a database that already has its tables: %v", categoryModifierGroupLinksMigrationVersion, err)
	}
	if n := count(`SELECT COUNT(*) FROM item_modifier_group_opt_outs`); n != 1 {
		t.Fatalf("opt-out rows after replay = %d, want 1 (replay must not drop or duplicate data)", n)
	}
	if n := count(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, categoryModifierGroupLinksMigrationVersion); n != 1 {
		t.Fatalf("schema_migrations has version %d %d time(s), want 1", categoryModifierGroupLinksMigrationVersion, n)
	}
	for _, table := range []string{"category_modifier_group_links", "item_modifier_group_opt_outs"} {
		if n := count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ?`, table); n != 3 {
			t.Fatalf("triggers on %s after replay = %d, want 3", table, n)
		}
	}
}

// TestMigration031_IsOnDisk guards the number this file's sibling test
// hardcodes — same guard 025/028/029 already carry.
func TestMigration031_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, categoryModifierGroupLinksMigrationVersion)
	if m.Name != "031_category_modifier_group_links.sql" {
		t.Fatalf("migration %d on disk is %q, want 031_category_modifier_group_links.sql", categoryModifierGroupLinksMigrationVersion, m.Name)
	}
}
