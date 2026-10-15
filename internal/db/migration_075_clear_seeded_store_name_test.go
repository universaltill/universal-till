package db

import "testing"

// clearSeededStoreNameMigrationVersion is
// 075_clear_seeded_store_name.sql (ut-docs#4273).
const clearSeededStoreNameMigrationVersion = 75

func TestMigration075_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, clearSeededStoreNameMigrationVersion)
	if m.Name != "075_clear_seeded_store_name.sql" {
		t.Fatalf("migration %d on disk is %q", clearSeededStoreNameMigrationVersion, m.Name)
	}
}

// TestMigration075_ClearsOnlyTheExactSeed: migration 001's "My Store" seed
// becomes blank (the row stays — shop_name_notice shows only for a stored
// key); a name a person typed, even one that differs from the seed only in
// case or spacing, is left exactly as it was.
func TestMigration075_ClearsOnlyTheExactSeed(t *testing.T) {
	cases := []struct {
		name, stored, want string
	}{
		{"exact seed cleared", "My Store", ""},
		{"real name kept", "Corner Cafe", "Corner Cafe"},
		{"different case kept", "my store", "my store"},
		{"padded kept", " My Store ", " My Store "},
		{"cloud placeholder kept", "Universal Till store", "Universal Till store"},
		{"blank stays blank", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, path := openAtPreMigrationSchema(t, clearSeededStoreNameMigrationVersion, "m075.db")
			if _, err := d.DB.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('store.name', ?)`, tc.stored); err != nil {
				t.Fatal(err)
			}
			if _, err := d.DB.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('till.name', 'My Store')`); err != nil {
				t.Fatal(err)
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			d, err := Open(path)
			if err != nil {
				t.Fatalf("Open (runs 075): %v", err)
			}
			defer d.Close()
			var got string
			if err := d.DB.QueryRow(`SELECT value FROM settings WHERE key = 'store.name'`).Scan(&got); err != nil {
				t.Fatalf("store.name row must survive: %v", err)
			}
			if got != tc.want {
				t.Fatalf("store.name = %q, want %q", got, tc.want)
			}
			// Only store.name: another key holding the same text is untouched.
			if err := d.DB.QueryRow(`SELECT value FROM settings WHERE key = 'till.name'`).Scan(&got); err != nil || got != "My Store" {
				t.Fatalf("till.name = %q (err %v), want it untouched", got, err)
			}
		})
	}
}

// TestFreshInstall_StoreNameIsBlank: a brand-new till ends the migration run
// with no placeholder value — 001 seeds "My Store" and 075 clears it.
func TestFreshInstall_StoreNameIsBlank(t *testing.T) {
	d, err := Open(t.TempDir() + "/fresh.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var got string
	if err := d.DB.QueryRow(`SELECT value FROM settings WHERE key = 'store.name'`).Scan(&got); err != nil {
		t.Fatalf("store.name row: %v", err)
	}
	if got != "" {
		t.Fatalf("fresh store.name = %q, want blank", got)
	}
}
