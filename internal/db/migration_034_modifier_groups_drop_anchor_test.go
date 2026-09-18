package db

import (
	"path/filepath"
	"testing"
)

// modifierGroupsDropAnchorMigrationVersion is
// 034_modifier_groups_drop_anchor.sql (ADR-0101, ut-docs#2399). If the file
// is ever renumbered, this constant moves with it.
const modifierGroupsDropAnchorMigrationVersion = 34

// modifierChildTables are the four leaf children of item_modifier_groups
// that 034 rebuilds (ADR-0101 Context: nothing in the schema references
// any of them, which is what makes the rebuild safe under enforced FKs).
var modifierChildTables = []string{
	"item_modifier_options",
	"item_modifier_group_links",
	"category_modifier_group_links",
	"item_modifier_group_opt_outs",
}

// modifierAdminIndexes are the indexes DROP TABLE takes with each rebuilt
// table and 034 must recreate — one per child, exactly as 001/025/031
// named them.
var modifierAdminIndexes = map[string]string{
	"item_modifier_options":         "idx_item_modifier_options_group",
	"item_modifier_group_links":     "idx_item_modifier_group_links_group",
	"category_modifier_group_links": "idx_category_modifier_group_links_group",
	"item_modifier_group_opt_outs":  "idx_item_modifier_group_opt_outs_group",
}

type groupRow struct {
	id, name                                    string
	required, minSel, maxSel, sortOrder, active int
}

type optionRow struct {
	id, groupID, name        string
	price, sortOrder, active int
}

type linkRow struct {
	owner, groupID string
	sortOrder      int
}

type optOutRow struct{ item, groupID string }

// pre034Seed is a real shop's modifier data in the pre-034 shape: three
// groups each anchored via item_id (the column 034 removes), one inactive,
// with options (one inactive), item links (one group shared by two items,
// with per-item sort orders), category links and an opt-out.
var (
	seedGroups = []groupRow{
		{"g1", "Extras", 0, 0, 2, 3, 1},
		{"g2", "Milk", 1, 1, 1, 1, 1},
		{"g3", "Size", 1, 1, 1, 0, 0},
	}
	seedOptions = []optionRow{
		{"o1", "g1", "Extra shot", 50, 1, 1},
		{"o2", "g3", "Large", 100, 1, 0},
	}
	seedItemLinks = []linkRow{
		{"itm-a", "g1", 3},
		{"itm-a", "g2", 1},
		{"itm-b", "g1", 7},
		{"itm-b", "g3", 0},
	}
	seedCategoryLinks = []linkRow{
		{"cat-1", "g2", 0},
		{"cat-1", "g3", 4},
	}
	seedOptOuts = []optOutRow{{"itm-b", "g2"}}
)

func readGroups(t *testing.T, d *DB) []groupRow {
	t.Helper()
	rows, err := d.DB.Query(`SELECT id, name, required, min_select, max_select, sort_order, is_active FROM item_modifier_groups ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []groupRow
	for rows.Next() {
		var g groupRow
		if err := rows.Scan(&g.id, &g.name, &g.required, &g.minSel, &g.maxSel, &g.sortOrder, &g.active); err != nil {
			t.Fatal(err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func readOptions(t *testing.T, d *DB) []optionRow {
	t.Helper()
	rows, err := d.DB.Query(`SELECT id, group_id, name, price_delta_minor, sort_order, is_active FROM item_modifier_options ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []optionRow
	for rows.Next() {
		var o optionRow
		if err := rows.Scan(&o.id, &o.groupID, &o.name, &o.price, &o.sortOrder, &o.active); err != nil {
			t.Fatal(err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func readLinks(t *testing.T, d *DB, table, ownerCol string) []linkRow {
	t.Helper()
	rows, err := d.DB.Query(`SELECT ` + ownerCol + `, group_id, sort_order FROM ` + table + ` ORDER BY ` + ownerCol + `, group_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []linkRow
	for rows.Next() {
		var l linkRow
		if err := rows.Scan(&l.owner, &l.groupID, &l.sortOrder); err != nil {
			t.Fatal(err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func readOptOuts(t *testing.T, d *DB) []optOutRow {
	t.Helper()
	rows, err := d.DB.Query(`SELECT item_id, group_id FROM item_modifier_group_opt_outs ORDER BY item_id, group_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []optOutRow
	for rows.Next() {
		var o optOutRow
		if err := rows.Scan(&o.item, &o.groupID); err != nil {
			t.Fatal(err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// assertSeedSurvived compares every modifier row in d against the seed,
// field by field.
func assertSeedSurvived(t *testing.T, d *DB, when string) {
	t.Helper()
	if got := readGroups(t, d); len(got) != len(seedGroups) {
		t.Fatalf("%s: groups = %+v, want %+v", when, got, seedGroups)
	} else {
		for i := range seedGroups {
			if got[i] != seedGroups[i] {
				t.Fatalf("%s: group[%d] = %+v, want %+v", when, i, got[i], seedGroups[i])
			}
		}
	}
	if got := readOptions(t, d); len(got) != len(seedOptions) {
		t.Fatalf("%s: options = %+v, want %+v", when, got, seedOptions)
	} else {
		for i := range seedOptions {
			if got[i] != seedOptions[i] {
				t.Fatalf("%s: option[%d] = %+v, want %+v", when, i, got[i], seedOptions[i])
			}
		}
	}
	if got := readLinks(t, d, "item_modifier_group_links", "item_id"); len(got) != len(seedItemLinks) {
		t.Fatalf("%s: item links = %+v, want %+v", when, got, seedItemLinks)
	} else {
		for i := range seedItemLinks {
			if got[i] != seedItemLinks[i] {
				t.Fatalf("%s: item link[%d] = %+v, want %+v", when, i, got[i], seedItemLinks[i])
			}
		}
	}
	if got := readLinks(t, d, "category_modifier_group_links", "category_id"); len(got) != len(seedCategoryLinks) {
		t.Fatalf("%s: category links = %+v, want %+v", when, got, seedCategoryLinks)
	} else {
		for i := range seedCategoryLinks {
			if got[i] != seedCategoryLinks[i] {
				t.Fatalf("%s: category link[%d] = %+v, want %+v", when, i, got[i], seedCategoryLinks[i])
			}
		}
	}
	if got := readOptOuts(t, d); len(got) != len(seedOptOuts) || got[0] != seedOptOuts[0] {
		t.Fatalf("%s: opt-outs = %+v, want %+v", when, got, seedOptOuts)
	}
}

// assertPost034Shape pins the structural contract of ADR-0101 Decision 1
// on any database that has run 034: no anchor column, every child's
// group_id FK re-pointed at item_modifier_groups (not the _new name, and
// still ON DELETE CASCADE), every index and every one of the fifteen
// sync_admin_version triggers back in place, and no FK violation anywhere.
func assertPost034Shape(t *testing.T, d *DB) {
	t.Helper()
	if n := columnCount(t, d, "item_modifier_groups", "item_id"); n != 0 {
		t.Fatalf("item_modifier_groups.item_id still exists (%d) — 034 must drop the anchor column", n)
	}
	for _, col := range []string{"id", "name", "required", "min_select", "max_select", "sort_order", "is_active"} {
		if n := columnCount(t, d, "item_modifier_groups", col); n != 1 {
			t.Fatalf("item_modifier_groups.%s count = %d, want 1", col, n)
		}
	}
	var leftover int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%\_new' ESCAPE '\'`).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Fatalf("%d *_new object(s) left behind by the rebuild", leftover)
	}
	for _, child := range modifierChildTables {
		var parent, onDelete string
		err := d.DB.QueryRow(`SELECT "table", on_delete FROM pragma_foreign_key_list(?) WHERE "from" = 'group_id'`, child).Scan(&parent, &onDelete)
		if err != nil {
			t.Fatalf("%s: no group_id foreign key: %v", child, err)
		}
		if parent != "item_modifier_groups" || onDelete != "CASCADE" {
			t.Fatalf("%s.group_id REFERENCES %s ON DELETE %s, want item_modifier_groups / CASCADE", child, parent, onDelete)
		}
		var idx int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ? AND tbl_name = ?`, modifierAdminIndexes[child], child).Scan(&idx); err != nil {
			t.Fatal(err)
		}
		if idx != 1 {
			t.Fatalf("%s: index %s missing after the rebuild", child, modifierAdminIndexes[child])
		}
	}
	var oldIdx int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_item_modifier_groups_item'`).Scan(&oldIdx); err != nil {
		t.Fatal(err)
	}
	if oldIdx != 0 {
		t.Fatalf("idx_item_modifier_groups_item still exists — it indexed the dropped anchor column")
	}
	for _, table := range append([]string{"item_modifier_groups"}, modifierChildTables...) {
		for _, ev := range []string{"ins", "upd", "del"} {
			name := "trg_sync_admin_version_" + table + "_" + ev
			var n int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = ? AND tbl_name = ?`, name, table).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("trigger %s on %s: count = %d, want 1 (023's three-per-table contract)", name, table, n)
			}
		}
	}
	rows, err := d.DB.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	violations := 0
	for rows.Next() {
		violations++
	}
	if violations != 0 {
		t.Fatalf("PRAGMA foreign_key_check reports %d violation(s) after 034", violations)
	}
}

// openMigratedTo builds a database at exactly maxVersion through the real
// runner — the schema a till that installed that release actually has.
func openMigratedTo(t *testing.T, path string, maxVersion int) *DB {
	t.Helper()
	d, err := openRaw(path)
	if err != nil {
		t.Fatalf("openRaw: %v", err)
	}
	if err := d.migrateUpTo(maxVersion); err != nil {
		t.Fatalf("migrateUpTo(%d): %v", maxVersion, err)
	}
	var current int
	if err := d.DB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != maxVersion {
		t.Fatalf("ledger watermark = %d after migrateUpTo(%d)", current, maxVersion)
	}
	return d
}

// TestMigration034_UpgradesRealPre034Database is the upgrade path every
// v0.19.x till takes on its first boot after this change (ADR-0101
// Decision 1): a database built by migrations 001..033 — item_modifier_
// groups.item_id NOT NULL, four children pointing at it — seeded with a
// real shop's modifier data, then opened normally so 034 runs through the
// real runner inside its own transaction with foreign_keys=ON. Every row
// must survive byte-for-byte (the parent rebuild must not cascade its
// children away — ADR-0090's "hard constraint", correctly sequenced), the
// anchor column must be gone, and the schema must be whole again.
func TestMigration034_UpgradesRealPre034Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre034.db")
	old := openMigratedTo(t, path, modifierGroupsDropAnchorMigrationVersion-1)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := old.DB.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if n := columnCount(t, old, "item_modifier_groups", "item_id"); n != 1 {
		t.Fatalf("pre-034 schema must still carry item_id (count=%d) — is the seam building the wrong version?", n)
	}
	exec(`INSERT INTO categories (id, name) VALUES ('cat-1', 'Drinks')`)
	exec(`INSERT INTO items (id, sku, name, base_price, category_id) VALUES ('itm-a','A','Flat White',320,'cat-1'), ('itm-b','B','Latte',350,'cat-1')`)
	anchors := map[string]string{"g1": "itm-a", "g2": "itm-a", "g3": "itm-b"}
	for _, g := range seedGroups {
		exec(`INSERT INTO item_modifier_groups (id, item_id, name, required, min_select, max_select, sort_order, is_active) VALUES (?,?,?,?,?,?,?,?)`,
			g.id, anchors[g.id], g.name, g.required, g.minSel, g.maxSel, g.sortOrder, g.active)
	}
	for _, o := range seedOptions {
		exec(`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order, is_active) VALUES (?,?,?,?,?,?)`,
			o.id, o.groupID, o.name, o.price, o.sortOrder, o.active)
	}
	for _, l := range seedItemLinks {
		exec(`INSERT INTO item_modifier_group_links (item_id, group_id, sort_order) VALUES (?,?,?)`, l.owner, l.groupID, l.sortOrder)
	}
	for _, l := range seedCategoryLinks {
		exec(`INSERT INTO category_modifier_group_links (category_id, group_id, sort_order) VALUES (?,?,?)`, l.owner, l.groupID, l.sortOrder)
	}
	for _, o := range seedOptOuts {
		exec(`INSERT INTO item_modifier_group_opt_outs (item_id, group_id) VALUES (?,?)`, o.item, o.groupID)
	}
	var genBefore int64
	if err := old.DB.QueryRow(`SELECT generation FROM sync_admin_version WHERE id = 1`).Scan(&genBefore); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// The real upgrade: Open runs 034 (and only 034) against the file.
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (upgrade through 034): %v", err)
	}
	defer d.Close()
	var applied int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, modifierGroupsDropAnchorMigrationVersion).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("034 not recorded as applied after the upgrade")
	}
	assertSeedSurvived(t, d, "after upgrade")
	assertPostUpgradeShapeAndReplay(t, d, path)
}

// assertPostUpgradeShapeAndReplay is the second half of the upgrade test,
// shared with the fresh-install test below: shape, replay safety, then the
// cascade contract.
func assertPostUpgradeShapeAndReplay(t *testing.T, d *DB, path string) {
	t.Helper()
	assertPost034Shape(t, d)

	// The sync triggers work on the rebuilt tables (a satellite must see a
	// generation bump for a change to any of the five tables).
	var gen1, gen2 int64
	if err := d.DB.QueryRow(`SELECT generation FROM sync_admin_version WHERE id = 1`).Scan(&gen1); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE item_modifier_groups SET name = name WHERE id = 'g1'`); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.QueryRow(`SELECT generation FROM sync_admin_version WHERE id = 1`).Scan(&gen2); err != nil {
		t.Fatal(err)
	}
	if gen2 != gen1+1 {
		t.Fatalf("sync_admin_version generation %d -> %d after an item_modifier_groups UPDATE, want +1 (trigger not recreated?)", gen1, gen2)
	}

	// Replay (openAtPreMigrationSchema's contract for every migration):
	// rewind only the ledger row and reopen, so 034 runs a second time
	// against an ALREADY anchor-free table. The copy SELECTs never name
	// item_id, so this is a no-op rebuild — every row must still be there.
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= ?`, modifierGroupsDropAnchorMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	replayed, err := Open(path)
	if err != nil {
		t.Fatalf("re-open (replay 034 against an already-migrated file): %v", err)
	}
	defer replayed.Close()
	assertSeedSurvived(t, replayed, "after replay")
	assertPost034Shape(t, replayed)

	// Cascade: deleting a group takes its options, item links, category
	// links and opt-outs with it — and nothing else. g2 has a category
	// link, an item link and an opt-out; g1 has an option and two item
	// links; g3 keeps everything.
	if _, err := replayed.DB.Exec(`DELETE FROM item_modifier_groups WHERE id = 'g2'`); err != nil {
		t.Fatalf("delete g2: %v", err)
	}
	if _, err := replayed.DB.Exec(`DELETE FROM item_modifier_groups WHERE id = 'g1'`); err != nil {
		t.Fatalf("delete g1: %v", err)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := replayed.DB.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	for _, tc := range []struct {
		q    string
		want int
	}{
		{`SELECT COUNT(*) FROM item_modifier_options WHERE group_id IN ('g1','g2')`, 0},
		{`SELECT COUNT(*) FROM item_modifier_group_links WHERE group_id IN ('g1','g2')`, 0},
		{`SELECT COUNT(*) FROM category_modifier_group_links WHERE group_id IN ('g1','g2')`, 0},
		{`SELECT COUNT(*) FROM item_modifier_group_opt_outs WHERE group_id IN ('g1','g2')`, 0},
		{`SELECT COUNT(*) FROM item_modifier_groups`, 1},
		{`SELECT COUNT(*) FROM item_modifier_options WHERE group_id = 'g3'`, 1},
		{`SELECT COUNT(*) FROM item_modifier_group_links WHERE group_id = 'g3'`, 1},
		{`SELECT COUNT(*) FROM category_modifier_group_links WHERE group_id = 'g3'`, 1},
		{`SELECT COUNT(*) FROM items`, 2},
		{`SELECT COUNT(*) FROM categories`, 1},
	} {
		if got := count(tc.q); got != tc.want {
			t.Fatalf("%s = %d, want %d (cascade after DeleteGroup)", tc.q, got, tc.want)
		}
	}
	// And an item delete no longer reaches any group row at all (ADR-0101
	// Decision 2: "Deleting an item now only ever removes that item's own
	// link/opt-out rows").
	if _, err := replayed.DB.Exec(`DELETE FROM items WHERE id = 'itm-b'`); err != nil {
		t.Fatalf("delete itm-b: %v", err)
	}
	if got := count(`SELECT COUNT(*) FROM item_modifier_groups WHERE id = 'g3'`); got != 1 {
		t.Fatalf("g3 was deleted with itm-b — an item delete must never cascade into a group row any more")
	}
	if got := count(`SELECT COUNT(*) FROM item_modifier_group_links WHERE item_id = 'itm-b'`); got != 0 {
		t.Fatalf("itm-b's link rows survived its deletion (%d)", got)
	}
}

// TestMigration034_FreshInstallHasSameShape: a brand-new database (every
// migration in order, 034 rebuilding empty tables) ends in exactly the
// same shape the upgrade path produces, and the same seed written the NEW
// way (no item_id) survives a replay identically.
func TestMigration034_FreshInstallHasSameShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh034.db")
	d, err := Open(path)
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
	exec(`INSERT INTO categories (id, name) VALUES ('cat-1', 'Drinks')`)
	exec(`INSERT INTO items (id, sku, name, base_price, category_id) VALUES ('itm-a','A','Flat White',320,'cat-1'), ('itm-b','B','Latte',350,'cat-1')`)
	for _, g := range seedGroups {
		exec(`INSERT INTO item_modifier_groups (id, name, required, min_select, max_select, sort_order, is_active) VALUES (?,?,?,?,?,?,?)`,
			g.id, g.name, g.required, g.minSel, g.maxSel, g.sortOrder, g.active)
	}
	for _, o := range seedOptions {
		exec(`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order, is_active) VALUES (?,?,?,?,?,?)`,
			o.id, o.groupID, o.name, o.price, o.sortOrder, o.active)
	}
	for _, l := range seedItemLinks {
		exec(`INSERT INTO item_modifier_group_links (item_id, group_id, sort_order) VALUES (?,?,?)`, l.owner, l.groupID, l.sortOrder)
	}
	for _, l := range seedCategoryLinks {
		exec(`INSERT INTO category_modifier_group_links (category_id, group_id, sort_order) VALUES (?,?,?)`, l.owner, l.groupID, l.sortOrder)
	}
	for _, o := range seedOptOuts {
		exec(`INSERT INTO item_modifier_group_opt_outs (item_id, group_id) VALUES (?,?)`, o.item, o.groupID)
	}
	assertSeedSurvived(t, d, "fresh install")
	assertPostUpgradeShapeAndReplay(t, d, path)
}

// A group row no longer needs an item at all — the whole point of
// ADR-0101 Decision 2 at the schema level.
func TestMigration034_GroupRowNeedsNoItem(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "standalone.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB.Exec(`INSERT INTO item_modifier_groups (id, name) VALUES ('g-alone', 'Sauces')`); err != nil {
		t.Fatalf("a group with no item must be insertable after 034: %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM item_modifier_groups WHERE id = 'g-alone'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("standalone group row count = %d err=%v", n, err)
	}
	// The CHECK constraint from 001 is carried across unchanged.
	if _, err := d.DB.Exec(`INSERT INTO item_modifier_groups (id, name, min_select, max_select) VALUES ('g-bad', 'Bad', 2, 1)`); err == nil {
		t.Fatalf("CHECK (min_select >= 0 AND max_select >= min_select) must survive the rebuild")
	}
}
