package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// ut-docs#6755: migration 117 seeds the built-in external card tender.
func TestMigration117_CardExternalSeeded(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m117.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var typ string
	var active int
	var plugin sql.NullString
	if err := d.DB.QueryRow(`SELECT type, is_active, plugin_id FROM payment_methods WHERE id='card_external'`).Scan(&typ, &active, &plugin); err != nil {
		t.Fatalf("card_external row: %v", err)
	}
	if typ != "card" || active != 1 || plugin.Valid {
		t.Fatalf("got type=%q active=%d plugin=%v", typ, active, plugin)
	}
}
