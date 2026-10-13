package db

import "testing"

// defaultRegisterNameMigrationVersion is
// 072_default_register_follows_till_name.sql (ut-docs#4022).
const defaultRegisterNameMigrationVersion = 72

func TestMigration072_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, defaultRegisterNameMigrationVersion)
	if m.Name != "072_default_register_follows_till_name.sql" {
		t.Fatalf("migration %d on disk is %q", defaultRegisterNameMigrationVersion, m.Name)
	}
}

// TestMigration072_RenamesSeededDefaultRegister: an installed till's
// seeded "Default Register" takes the till's name, else the shop's; a
// placeholder shop name, a manager-chosen name, or a name another register
// holds is left alone.
func TestMigration072_RenamesSeededDefaultRegister(t *testing.T) {
	cases := []struct {
		name      string
		tillName  string // "" = no till.name row
		storeName string // 001 seeds "My Store"
		regName   string
		other     string // another register's name, "" = none
		want      string
	}{
		{"till name", "Counter", "Corner Cafe", "Default Register", "", "Counter"},
		{"shop name when till unnamed", "", "Corner Cafe", "Default Register", "", "Corner Cafe"},
		{"blank till name falls to shop", "  ", "Corner Cafe", "Default Register", "", "Corner Cafe"},
		{"placeholder shop name kept", "", "My Store", "Default Register", "", "Default Register"},
		{"manager-chosen name kept", "Counter", "Corner Cafe", "Front desk", "", "Front desk"},
		{"till name taken falls to shop", "Counter", "Corner Cafe", "Default Register", "Counter", "Corner Cafe"},
		{"both taken kept", "Counter", "Counter", "Default Register", "Counter", "Default Register"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, path := openAtPreMigrationSchema(t, defaultRegisterNameMigrationVersion, "m072.db")
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := d.DB.Exec(q, args...); err != nil {
					t.Fatalf("exec %q: %v", q, err)
				}
			}
			if tc.tillName != "" {
				exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('till.name', ?)`, tc.tillName)
			}
			exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('store.name', ?)`, tc.storeName)
			exec(`INSERT INTO registers (id, name, is_active) VALUES ('reg-default', ?, 1)`, tc.regName)
			if tc.other != "" {
				exec(`INSERT INTO registers (id, name, is_active) VALUES ('reg-other', ?, 1)`, tc.other)
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			d, err := Open(path)
			if err != nil {
				t.Fatalf("Open (runs 072): %v", err)
			}
			defer d.Close()
			var got string
			if err := d.DB.QueryRow(`SELECT name FROM registers WHERE id = 'reg-default'`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("reg-default name = %q, want %q", got, tc.want)
			}
		})
	}
}
