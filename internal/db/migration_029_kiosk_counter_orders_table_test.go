package db

import "testing"

// kioskCounterOrdersTableMigrationVersion is
// 029_kiosk_counter_orders_table.sql (ut-docs#815). If the file is ever
// renumbered, this constant moves with it.
const kioskCounterOrdersTableMigrationVersion = 29

// TestMigration029_AppliesCleanly pins ut-docs#815's core acceptance
// criterion: the migration must apply against a fresh, already-migrated
// database (every migration through 028 has already run, same as every
// other migration test in this package) with no error, add
// kiosk_counter_orders.table_id, and be safe to replay — same convention
// 017/019/021's own tests already pin for a plain ADD COLUMN /
// CREATE INDEX IF NOT EXISTS migration.
func TestMigration029_AppliesCleanly(t *testing.T) {
	d, err := Open(t.TempDir() + "/kiosk-counter-orders-table.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	var applied int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, kioskCounterOrdersTableMigrationVersion).Scan(&applied); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if applied != 1 {
		t.Fatalf("migration %d not recorded as applied on a fresh DB — has it been renumbered?", kioskCounterOrdersTableMigrationVersion)
	}

	// Column exists and is nullable (no table assigned is the common case:
	// a plain kiosk-till counter order never involved a QR/table at all).
	rows, err := d.DB.Query(`SELECT name, "notnull" FROM pragma_table_info('kiosk_counter_orders') WHERE name = 'table_id'`)
	if err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		found = true
		var name string
		var notNull int
		if err := rows.Scan(&name, &notNull); err != nil {
			t.Fatal(err)
		}
		if notNull != 0 {
			t.Fatalf("kiosk_counter_orders.table_id must be nullable (NOT NULL=%d)", notNull)
		}
	}
	if !found {
		t.Fatal("kiosk_counter_orders.table_id column was not created")
	}

	// The FK actually works: an insert referencing a real table succeeds,
	// storing and reading back table_id.
	if _, err := d.DB.Exec(`INSERT INTO tables (id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at)
VALUES ('t1', 'T1', '', 4, 'rect', 100, 100, 1, datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO kiosk_counter_orders (id, display_no, order_type, lines_json, status, created_at, table_id)
VALUES ('co1', 'C-1', '', '[]', 'open', datetime('now'), 't1')`); err != nil {
		t.Fatalf("insert counter order with table_id: %v", err)
	}
	var gotTableID string
	if err := d.DB.QueryRow(`SELECT table_id FROM kiosk_counter_orders WHERE id = 'co1'`).Scan(&gotTableID); err != nil {
		t.Fatalf("read back table_id: %v", err)
	}
	if gotTableID != "t1" {
		t.Fatalf("table_id = %q, want t1", gotTableID)
	}

	// The index exists (idx_kiosk_counter_orders_table).
	var idxCount int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_kiosk_counter_orders_table'`).Scan(&idxCount); err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	if idxCount != 1 {
		t.Fatal("idx_kiosk_counter_orders_table index was not created")
	}

	// Replay safety (021/025's own convention): rewind the ledger row only
	// (what a renumbered/pre-merge build leaves on a device) and re-apply.
	m := loadMigrationVersion(t, kioskCounterOrdersTableMigrationVersion)
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version = ?`, kioskCounterOrdersTableMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("replaying migration %d against an already-migrated database: %v", kioskCounterOrdersTableMigrationVersion, err)
	}
}

// TestMigration029_IsOnDisk guards the number this file's sibling test
// hardcodes — same guard 025/028 already carry.
func TestMigration029_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, kioskCounterOrdersTableMigrationVersion)
	if m.Name != "029_kiosk_counter_orders_table.sql" {
		t.Fatalf("migration %d on disk is %q, want 029_kiosk_counter_orders_table.sql", kioskCounterOrdersTableMigrationVersion, m.Name)
	}
}
