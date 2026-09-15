package db

import "testing"

// heldSalesUpdatedAtMigrationVersion is 030_held_sales_updated_at.sql
// (ADR-0093, ut-docs#1920). If the file is ever renumbered, this constant
// moves with it.
const heldSalesUpdatedAtMigrationVersion = 30

// TestMigration030_AppliesCleanly pins ADR-0093 Decision 1's schema half:
// held_sales gains a NOT NULL updated_at, every PRE-EXISTING row is
// backfilled from its created_at (so the predicate guard has a real value
// to compare on day one, never ” or NULL), and the migration is safe to
// replay -- same convention 029's own test pins for a plain ADD COLUMN.
func TestMigration030_AppliesCleanly(t *testing.T) {
	d, err := Open(t.TempDir() + "/held-sales-updated-at.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	var applied int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, heldSalesUpdatedAtMigrationVersion).Scan(&applied); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if applied != 1 {
		t.Fatalf("migration %d not recorded as applied on a fresh DB — has it been renumbered?", heldSalesUpdatedAtMigrationVersion)
	}

	var notNull int
	if err := d.DB.QueryRow(`SELECT "notnull" FROM pragma_table_info('held_sales') WHERE name = 'updated_at'`).Scan(&notNull); err != nil {
		t.Fatalf("held_sales.updated_at column was not created: %v", err)
	}
	if notNull != 1 {
		t.Fatalf("held_sales.updated_at must be NOT NULL (notnull=%d)", notNull)
	}
	// The archive twin gets the column in the same migration, so the reset
	// archive round-trip (reset_archive_repo.go's held_sales cols string,
	// pinned by reset_test.go) can carry it -- the 055 table_id / 056
	// tracking_token / 013 display_no convention for every live-table ALTER.
	var archiveNotNull int
	if err := d.DB.QueryRow(`SELECT "notnull" FROM pragma_table_info('held_sales_archive') WHERE name = 'updated_at'`).Scan(&archiveNotNull); err != nil {
		t.Fatalf("held_sales_archive.updated_at column was not created: %v", err)
	}
	// primary_synced (ADR-0093 Amendment A): NOT NULL, constant default 0 on
	// both the live table and its archive twin -- a pre-existing row was never
	// confirmed on a primary, so 0 is the correct value with no backfill.
	for _, table := range []string{"held_sales", "held_sales_archive"} {
		var syncedNotNull int
		var syncedDefault string
		if err := d.DB.QueryRow(`SELECT "notnull", dflt_value FROM pragma_table_info(?) WHERE name = 'primary_synced'`, table).Scan(&syncedNotNull, &syncedDefault); err != nil {
			t.Fatalf("%s.primary_synced column was not created: %v", table, err)
		}
		if syncedNotNull != 1 || syncedDefault != "0" {
			t.Fatalf("%s.primary_synced must be NOT NULL DEFAULT 0, got notnull=%d default=%q", table, syncedNotNull, syncedDefault)
		}
	}

	// Backfill: rewind the ledger row, seed a pre-migration-shaped row (no
	// updated_at named at all, exactly what an existing row on a device
	// looks like), and re-apply. db.go skips the ADD COLUMN itself when the
	// column already exists (ut-docs#1412), so what this actually exercises
	// on replay is the backfill UPDATE running over a row whose updated_at
	// still holds the column's placeholder default -- the same statement a
	// real upgrade runs right after its ADD COLUMN.
	m := loadMigrationVersion(t, heldSalesUpdatedAtMigrationVersion)
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version = ?`, heldSalesUpdatedAtMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, created_at) VALUES ('h-old', 'Table 4', 1200, 3, '{}', '2026-09-01 10:00:00')`); err != nil {
		t.Fatalf("seed pre-migration row: %v", err)
	}
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("replaying migration %d against an already-migrated database: %v", heldSalesUpdatedAtMigrationVersion, err)
	}
	var updatedAt string
	if err := d.DB.QueryRow(`SELECT updated_at FROM held_sales WHERE id = 'h-old'`).Scan(&updatedAt); err != nil {
		t.Fatalf("read back updated_at: %v", err)
	}
	if updatedAt != "2026-09-01 10:00:00" {
		t.Fatalf("a pre-existing row's updated_at must be backfilled from created_at, got %q", updatedAt)
	}
	var synced int
	if err := d.DB.QueryRow(`SELECT primary_synced FROM held_sales WHERE id = 'h-old'`).Scan(&synced); err != nil || synced != 0 {
		t.Fatalf("a pre-existing row was never confirmed on a primary: primary_synced must be 0, got %d err=%v", synced, err)
	}
}

// TestMigration030_IsOnDisk guards the number this file's sibling test
// hardcodes — same guard 025/028/029 already carry.
func TestMigration030_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, heldSalesUpdatedAtMigrationVersion)
	if m.Name != "030_held_sales_updated_at.sql" {
		t.Fatalf("migration %d on disk is %q, want 030_held_sales_updated_at.sql", heldSalesUpdatedAtMigrationVersion, m.Name)
	}
}
