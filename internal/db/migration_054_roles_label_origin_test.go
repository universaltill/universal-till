package db

import "testing"

// rolesLabelOriginMigrationVersion is 054_roles_label_origin.sql (ADR-0128
// §2, ut-docs#3165). Migration files are never renumbered once merged;
// TestMigration054_IsOnDisk pins this constant to that filename.
const rolesLabelOriginMigrationVersion = 54

func TestMigration054_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, rolesLabelOriginMigrationVersion)
	if m.Name != "054_roles_label_origin.sql" {
		t.Fatalf("migration %d on disk is %q, want 054_roles_label_origin.sql", rolesLabelOriginMigrationVersion, m.Name)
	}
}

// TestMigration054_LabelOriginAndBackfill: roles gains label (NOT NULL
// DEFAULT empty) and origin (NOT NULL DEFAULT 'builtin'). Built-in rows stay
// 'builtin'; a c_-keyed row that reached a till before it had the column
// (an older satellite upserting the bundle) is backfilled to 'cloud'. The
// LIKE escapes the underscore, so a role merely starting with "c" (e.g.
// "cashier", or a hypothetical "cx_lead") stays built-in. Replay is safe.
func TestMigration054_LabelOriginAndBackfill(t *testing.T) {
	d, path := openAtPreMigrationSchema(t, rolesLabelOriginMigrationVersion, "m054.db")
	for _, r := range []string{"c_01j9z3k4m5n6p7q8r9s0t1v2w3", "cx_lead"} {
		if _, err := d.DB.Exec(`INSERT INTO roles (role) VALUES (?)`, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (runs 054): %v", err)
	}
	defer d.Close()

	for _, c := range []struct{ col, dflt string }{{"label", "''"}, {"origin", "'builtin'"}} {
		var notNull int
		var dflt string
		if err := d.DB.QueryRow(`SELECT "notnull", dflt_value FROM pragma_table_info('roles') WHERE name = ?`, c.col).Scan(&notNull, &dflt); err != nil {
			t.Fatalf("roles.%s not created: %v", c.col, err)
		}
		if notNull != 1 || dflt != c.dflt {
			t.Fatalf("roles.%s notnull=%d default=%s, want NOT NULL DEFAULT %s", c.col, notNull, dflt, c.dflt)
		}
	}
	want := map[string]string{
		"cashier": "builtin", "manager": "builtin", "admin": "builtin", "super_admin": "builtin",
		"c_01j9z3k4m5n6p7q8r9s0t1v2w3": "cloud", "cx_lead": "builtin",
	}
	for role, origin := range want {
		var got, label string
		if err := d.DB.QueryRow(`SELECT origin, label FROM roles WHERE role = ?`, role).Scan(&got, &label); err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		if got != origin || label != "" {
			t.Errorf("%s origin=%q label=%q, want %q and ''", role, got, label, origin)
		}
	}

	// Replay against the migrated file: the runner skips the existing
	// columns and the UPDATE is idempotent.
	m := loadMigrationVersion(t, rolesLabelOriginMigrationVersion)
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version = ?`, rolesLabelOriginMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("replaying 054: %v", err)
	}
	if n := columnCount(t, d, "roles", "origin"); n != 1 {
		t.Fatalf("roles.origin count = %d after replay", n)
	}
}
