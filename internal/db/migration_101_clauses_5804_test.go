package db

import (
	"path/filepath"
	"testing"
)

// Tester (ut-docs#5804): each guard clause of migration 101 on its own keeps
// a row typed 'cash'. In TestMigration101_PaymentMethodUnknownPlaceholder
// every kept row is excluded by two or more clauses at once, so dropping the
// id, plugin_id or sort_order clause alone went unnoticed (mutation-tested).
// Here each kept row matches every clause but one.
func TestMigration101_EachGuardClauseIsLoadBearing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pm-clauses.db")
	d := openMigratedTo(t, path, 100)
	if _, err := d.DB.Exec(`
INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES
 ('plugmade', 'plugmade', 'cash', 1, 0,   'com.x.p'),
 ('lanmade',  'lanmade',  'cash', 1, 101, NULL),
 ('automade', 'automade', 'cash', 1, 0,   NULL);
UPDATE payment_methods SET name = 'cash', sort_order = 0, plugin_id = NULL WHERE id = 'cash';`); err != nil {
		t.Fatal(err)
	}
	d.Close()

	d = openMigratedTo(t, path, 101)
	defer d.Close()
	for id, want := range map[string]string{
		"cash":     "cash",    // only the id clause keeps it
		"plugmade": "cash",    // only plugin_id IS NULL keeps it
		"lanmade":  "cash",    // only sort_order = 0 keeps it
		"automade": "unknown", // control: matches every clause
	} {
		var got string
		if err := d.DB.QueryRow(`SELECT type FROM payment_methods WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if got != want {
			t.Errorf("%s: type=%q, want %q", id, got, want)
		}
	}
}
