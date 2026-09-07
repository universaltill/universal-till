package data

import (
	"testing"
)

// TestSchemaTablesAreClassified is the schema-drift guard ut-docs#1586 asked
// for: every real table in the migrated schema must be in exactly one of
// adminTables or nonAdminTables (sync_admin_repo.go). Without this, a new
// shop-wide-looking table can be added to a migration and sit unsynced
// indefinitely with nothing to catch it — exactly what happened to
// tables/kitchen_stations before ut-docs#1546 found it by hand.
//
// This also catches the opposite drift: a name in either list that no
// longer names a real table (a rename or drop the lists weren't updated
// for) would silently stop meaning anything.
func TestSchemaTablesAreClassified(t *testing.T) {
	d := openMigratedDB(t, "schema-drift.db")

	rows, err := d.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	defer rows.Close()

	realTables := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		realTables[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	if len(realTables) == 0 {
		t.Fatal("sqlite_master returned no tables — migrations did not run, this test would pass vacuously")
	}

	classified := map[string]string{} // table name -> which list it came from
	for _, at := range adminTables {
		classified[at.name] = "adminTables"
	}
	for name := range nonAdminTables {
		if prior, ok := classified[name]; ok {
			t.Errorf("table %q is in both adminTables and nonAdminTables (also %s) — pick one", name, prior)
			continue
		}
		classified[name] = "nonAdminTables"
	}

	for name := range realTables {
		if _, ok := classified[name]; !ok {
			t.Errorf("table %q exists in the schema but is classified in neither adminTables nor nonAdminTables "+
				"(internal/data/sync_admin_repo.go) — add it to one, with a reason if excluded", name)
		}
	}

	for name := range classified {
		if !realTables[name] {
			t.Errorf("%q is classified in sync_admin_repo.go but is not a real table in the migrated schema "+
				"(renamed or dropped?) — remove or fix the stale entry", name)
		}
	}
}
