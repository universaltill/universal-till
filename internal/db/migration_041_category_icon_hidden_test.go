package db

import "testing"

// categoryIconHiddenMigrationVersion is 041_categories_icon_sell_screen_hidden.sql
// (manage-shop catalog contract §3.2). If the file is ever renumbered (a
// concurrent lane may claim 041 — whichever merges second moves), this
// constant moves with it.
const categoryIconHiddenMigrationVersion = 41

// TestMigration041_CategoryIconAndHiddenColumns pins the schema half of the
// save_category directive: categories gains a nullable icon and a NOT NULL
// sell_screen_hidden defaulting to 0 (shown), an existing row reads back
// NULL/0, and a replay against an already-migrated DB is safe.
func TestMigration041_CategoryIconAndHiddenColumns(t *testing.T) {
	d, err := Open(t.TempDir() + "/category-icon.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	var applied int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, categoryIconHiddenMigrationVersion).Scan(&applied); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if applied != 1 {
		t.Fatalf("migration %d not recorded as applied on a fresh DB — has it been renumbered?", categoryIconHiddenMigrationVersion)
	}
	var iconNotNull, hiddenNotNull int
	var hiddenDefault string
	if err := d.DB.QueryRow(`SELECT "notnull" FROM pragma_table_info('categories') WHERE name = 'icon'`).Scan(&iconNotNull); err != nil {
		t.Fatalf("categories.icon column was not created: %v", err)
	}
	if iconNotNull != 0 {
		t.Fatalf("categories.icon must be nullable, notnull=%d", iconNotNull)
	}
	if err := d.DB.QueryRow(`SELECT "notnull", dflt_value FROM pragma_table_info('categories') WHERE name = 'sell_screen_hidden'`).Scan(&hiddenNotNull, &hiddenDefault); err != nil {
		t.Fatalf("categories.sell_screen_hidden column was not created: %v", err)
	}
	if hiddenNotNull != 1 || hiddenDefault != "0" {
		t.Fatalf("categories.sell_screen_hidden must be NOT NULL DEFAULT 0, notnull=%d default=%q", hiddenNotNull, hiddenDefault)
	}

	if _, err := d.DB.Exec(`INSERT INTO categories (id, name) VALUES ('c-old', 'Old')`); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	m := loadMigrationVersion(t, categoryIconHiddenMigrationVersion)
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version = ?`, categoryIconHiddenMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("replaying migration %d: %v", categoryIconHiddenMigrationVersion, err)
	}
	var iconNull bool
	var hidden int
	if err := d.DB.QueryRow(`SELECT icon IS NULL, sell_screen_hidden FROM categories WHERE id = 'c-old'`).Scan(&iconNull, &hidden); err != nil || !iconNull || hidden != 0 {
		t.Fatalf("existing row must read back icon NULL, hidden 0: iconNull=%v hidden=%d err=%v", iconNull, hidden, err)
	}
}
