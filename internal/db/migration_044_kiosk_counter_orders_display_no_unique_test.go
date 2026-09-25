package db

import (
	"sort"
	"strings"
	"testing"
)

// kioskCounterOrdersDisplayNoUniqueMigrationVersion is
// 044_kiosk_counter_orders_display_no_unique.sql (ut-docs#2714). If the file
// is ever renumbered, this constant moves with it.
const kioskCounterOrdersDisplayNoUniqueMigrationVersion = 44

// TestMigration044_DedupesThenEnforcesUniqueDisplayNo pins ut-docs#2714's
// migration: duplicate C-numbers already on a till (from before the unique
// index) are renamed "<no>~<rowid>" -- the first row of each number keeps
// it -- then a unique index makes a duplicate insert fail. Replaying it
// (the ledger-rewinding tests, see 026's header) must be a no-op.
func TestMigration044_DedupesThenEnforcesUniqueDisplayNo(t *testing.T) {
	d, err := Open(t.TempDir() + "/kiosk-counter-orders-unique.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	var applied int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, kioskCounterOrdersDisplayNoUniqueMigrationVersion).Scan(&applied); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if applied != 1 {
		t.Fatalf("migration %d not recorded as applied on a fresh DB — has it been renumbered?", kioskCounterOrdersDisplayNoUniqueMigrationVersion)
	}

	// Rewind to the pre-044 state: no index, duplicates present.
	if _, err := d.DB.Exec(`DROP INDEX IF EXISTS idx_kiosk_counter_orders_display_no`); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{{"a", "C-1"}, {"b", "C-1"}, {"c", "C-2"}, {"d", "C-1"}, {"e", "C-T2-1"}} {
		if _, err := d.DB.Exec(`INSERT INTO kiosk_counter_orders (id, display_no, order_type, lines_json, status, created_at) VALUES (?, ?, '', '[]', 'held', '2026-09-25T10:00:00Z')`, row[0], row[1]); err != nil {
			t.Fatalf("seed duplicate %v: %v", row, err)
		}
	}
	m := loadMigrationVersion(t, kioskCounterOrdersDisplayNoUniqueMigrationVersion)
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version = ?`, kioskCounterOrdersDisplayNoUniqueMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("apply migration %d over duplicates: %v", kioskCounterOrdersDisplayNoUniqueMigrationVersion, err)
	}

	got := map[string]string{}
	rows, err := d.DB.Query(`SELECT id, display_no FROM kiosk_counter_orders`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, no string
		if err := rows.Scan(&id, &no); err != nil {
			t.Fatal(err)
		}
		got[id] = no
	}
	rows.Close()
	if got["a"] != "C-1" || got["c"] != "C-2" || got["e"] != "C-T2-1" {
		t.Fatalf("first row of each number must keep it: %v", got)
	}
	for _, id := range []string{"b", "d"} {
		if !strings.HasPrefix(got[id], "C-1~") {
			t.Fatalf("duplicate %s = %q, want C-1~<rowid>", id, got[id])
		}
	}
	vals := make([]string, 0, len(got))
	for _, v := range got {
		vals = append(vals, v)
	}
	sort.Strings(vals)
	for i := 1; i < len(vals); i++ {
		if vals[i] == vals[i-1] {
			t.Fatalf("display_no still duplicated after migration: %v", vals)
		}
	}

	if _, err := d.DB.Exec(`INSERT INTO kiosk_counter_orders (id, display_no, order_type, lines_json, status, created_at) VALUES ('dup', 'C-2', '', '[]', 'held', '2026-09-25T10:00:00Z')`); err == nil {
		t.Fatal("inserting a duplicate display_no must fail once the unique index exists")
	}

	// Replay safety: rewinding only the ledger row and re-applying is a no-op.
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version = ?`, kioskCounterOrdersDisplayNoUniqueMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("replaying migration %d: %v", kioskCounterOrdersDisplayNoUniqueMigrationVersion, err)
	}
	var a string
	if err := d.DB.QueryRow(`SELECT display_no FROM kiosk_counter_orders WHERE id = 'a'`).Scan(&a); err != nil || a != "C-1" {
		t.Fatalf("replay changed a unique row: %q, %v", a, err)
	}
}

// TestMigration044_IsOnDisk guards the number the sibling test hardcodes.
func TestMigration044_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, kioskCounterOrdersDisplayNoUniqueMigrationVersion)
	if m.Name != "044_kiosk_counter_orders_display_no_unique.sql" {
		t.Fatalf("migration %d on disk is %q, want 044_kiosk_counter_orders_display_no_unique.sql", kioskCounterOrdersDisplayNoUniqueMigrationVersion, m.Name)
	}
}
