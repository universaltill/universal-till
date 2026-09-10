package db

import (
	"path/filepath"
	"testing"
)

// modifierGroupLinksMigrationVersion is 025_modifier_group_links.sql
// (ADR-0090, ut-docs#2013). If the file is ever renumbered, this constant
// moves with it.
const modifierGroupLinksMigrationVersion = 25

// loadMigrationVersion returns the on-disk migration carrying version, so a
// test can re-apply exactly the shipped file through the real runner rather
// than a copy of its SQL that could drift.
func loadMigrationVersion(t *testing.T, version int) migration {
	t.Helper()
	migs, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for _, m := range migs {
		if m.Version == version {
			return m
		}
	}
	t.Fatalf("no migration with version %d on disk", version)
	return migration{}
}

// TestMigration025_BackfillsOneLinkPerExistingGroup pins ADR-0090's
// migration contract against a database that already carries a real shop's
// pre-025 modifier data: every existing group gets exactly one
// item_modifier_group_links row pointing at the item it already belonged
// to, with its sort_order carried across unchanged; item_modifier_groups
// itself is left completely untouched (its item_id column, values and
// child options all survive — the parent-table-rebuild hazard the ADR's
// "hard constraint" section rules out); and the migration is re-appliable
// (openAtPreMigrationSchema's contract for every later migration) without
// error and without duplicating the backfilled rows.
func TestMigration025_BackfillsOneLinkPerExistingGroup(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "links.db"))
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

	// Simulate a till that has never seen 025: drop what it created (the
	// table takes its index and its sync triggers with it) and rewind its
	// ledger row, exactly as openAtPreMigrationSchema does for 009/011.
	exec(`DROP TABLE item_modifier_group_links`)
	exec(`DELETE FROM schema_migrations WHERE version = ?`, modifierGroupLinksMigrationVersion)
	if n := count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'item_modifier_group_links'`); n != 0 {
		t.Fatalf("pre-025 simulation still has the link table (%d)", n)
	}

	// A real shop's data, written the OLD way: a group belongs to exactly
	// one item via item_modifier_groups.item_id, no link rows anywhere.
	exec(`INSERT INTO items (id, sku, name, base_price) VALUES ('itm-a','A','Flat White',320), ('itm-b','B','Latte',350)`)
	exec(`INSERT INTO item_modifier_groups (id, item_id, name, required, min_select, max_select, sort_order, is_active)
	      VALUES ('g1','itm-a','Extras',0,0,2,3,1), ('g2','itm-a','Milk',1,1,1,1,1), ('g3','itm-b','Size',1,1,1,0,0)`)
	exec(`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order) VALUES ('o1','g1','Extra shot',50,1), ('o2','g3','Large',100,1)`)

	m := loadMigrationVersion(t, modifierGroupLinksMigrationVersion)
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("apply 025 against pre-existing groups: %v", err)
	}

	// (a) one link per pre-existing group, item_id + sort_order copied across.
	rows, err := d.DB.Query(`SELECT item_id, group_id, sort_order FROM item_modifier_group_links ORDER BY group_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type link struct {
		item, group string
		sort        int
	}
	var got []link
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.item, &l.group, &l.sort); err != nil {
			t.Fatal(err)
		}
		got = append(got, l)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []link{{"itm-a", "g1", 3}, {"itm-a", "g2", 1}, {"itm-b", "g3", 0}}
	if len(got) != len(want) {
		t.Fatalf("backfilled links = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("backfilled link[%d] = %+v, want %+v (full: %+v)", i, got[i], want[i], got)
		}
	}

	// (b) item_modifier_groups itself is untouched: the legacy anchor column
	// is still there with its original values, and the options (children of
	// the parent table the ADR forbids rebuilding) all survived.
	if n := columnCount(t, d, "item_modifier_groups", "item_id"); n != 1 {
		t.Fatalf("item_modifier_groups.item_id column count = %d, want 1 (025 must not touch the anchor column)", n)
	}
	for _, tc := range []struct {
		id, item string
		sort     int
	}{{"g1", "itm-a", 3}, {"g2", "itm-a", 1}, {"g3", "itm-b", 0}} {
		var item string
		var sort int
		if err := d.DB.QueryRow(`SELECT item_id, sort_order FROM item_modifier_groups WHERE id = ?`, tc.id).Scan(&item, &sort); err != nil {
			t.Fatalf("group %s vanished after 025: %v", tc.id, err)
		}
		if item != tc.item || sort != tc.sort {
			t.Fatalf("group %s = (%s, %d) after 025, want (%s, %d) — the migration must not rewrite the group row", tc.id, item, sort, tc.item, tc.sort)
		}
	}
	if n := count(`SELECT COUNT(*) FROM item_modifier_options`); n != 2 {
		t.Fatalf("item_modifier_options count = %d after 025, want 2 — options were cascade-deleted (parent table rebuilt under enforced FKs?)", n)
	}
	if n := count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'item_modifier_group_links'`); n != 3 {
		t.Fatalf("sync_admin_version triggers on item_modifier_group_links = %d, want 3 (ins/upd/del — see 023's header)", n)
	}

	// (c) replay: rewind only the ledger row (rows and table stay, exactly
	// what a renumbered/pre-merge build leaves behind on a device) and apply
	// the same file again — no error, no duplicated backfill, ledger has it
	// exactly once.
	exec(`DELETE FROM schema_migrations WHERE version = ?`, modifierGroupLinksMigrationVersion)
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("replaying 025 against a database that already has its table and rows: %v", err)
	}
	if n := count(`SELECT COUNT(*) FROM item_modifier_group_links`); n != 3 {
		t.Fatalf("link count after replay = %d, want 3 (backfill INSERT duplicated rows or failed)", n)
	}
	if n := count(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, modifierGroupLinksMigrationVersion); n != 1 {
		t.Fatalf("schema_migrations has version %d %d time(s), want 1", modifierGroupLinksMigrationVersion, n)
	}
	if n := count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'item_modifier_group_links'`); n != 3 {
		t.Fatalf("triggers after replay = %d, want 3", n)
	}
}

// TestMigration025_IsOnDisk guards the number this file's sibling test
// hardcodes: version 025 must be THIS file, so a concurrent card that lands
// a 025 of its own (exactly what happened to this card's first draft, which
// was numbered 024 until ut-docs#2031's migration took that slot on main)
// is caught here rather than by a confusing checksum mismatch on a till.
func TestMigration025_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, modifierGroupLinksMigrationVersion)
	if m.Name != "025_modifier_group_links.sql" {
		t.Fatalf("migration %d on disk is %q, want 025_modifier_group_links.sql", modifierGroupLinksMigrationVersion, m.Name)
	}
}
