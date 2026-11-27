package db

import (
	"path/filepath"
	"testing"
)

// ut-docs#7098: migration 130 links each joined till's register to the device
// that owns it, from the latest till_enrolled audit row. The self register gets
// no row; a till with no device id links with "" (unknown, never auto-retired).
func TestMigration130BackfillsFromEnrolmentAudit(t *testing.T) {
	d := openMigratedTo(t, filepath.Join(t.TempDir(), "r.db"), 119)
	defer d.Close()
	for _, s := range []string{
		`INSERT INTO registers (id, name, is_active) VALUES ('reg-default','Main',1),('r1','phone',1),('r2','tablet',1),('r3','old',1),('r4','corrupt',1)`,
		`INSERT INTO settings (key, value) VALUES ('sync.till_register_id','reg-default')`,
		`INSERT INTO tills (id, name, device_id) VALUES ('tA','phone','dev-A'),('tB','tablet','dev-B'),('tC','old',NULL)`,
		`INSERT INTO audit_log (id, entity_type, entity_id, action, data_json, created_at) VALUES
		  ('a1','till','tA','till_enrolled','{"register_id":"r1"}','2026-01-01T00:00:00Z'),
		  ('a2','till','tB','till_enrolled','{"register_id":"r2"}','2026-01-01T00:00:00Z'),
		  ('a3','till','tC','till_enrolled','{"register_id":"r3"}','2026-01-01T00:00:00Z'),
		  ('a4','till','tD','till_enrolled','not json','2026-01-01T00:00:00Z'),
		  ('a5','till','tE','till_enrolled','{"register_id":"reg-default"}','2026-01-01T00:00:00Z'),
		  ('a6','till','tF','till_enrolled','{"register_id":"gone"}','2026-01-01T00:00:00Z')`,
	} {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("seed %.40s: %v", s, err)
		}
	}
	if err := d.migrateUpTo(130); err != nil {
		t.Fatalf("migrate 130: %v", err)
	}
	got := map[string]string{}
	rows, err := d.DB.Query(`SELECT register_id, device_id FROM register_devices`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r, dev string
		if err := rows.Scan(&r, &dev); err != nil {
			t.Fatal(err)
		}
		got[r] = dev
	}
	want := map[string]string{"r1": "dev-A", "r2": "dev-B", "r3": ""}
	if len(got) != len(want) {
		t.Fatalf("link rows = %v, want %v", got, want)
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Fatalf("link rows = %v, want %v", got, want)
		}
	}
	// one device = one register is enforced by the DB, not only by code
	if _, err := d.DB.Exec(`INSERT INTO register_devices (register_id, device_id) VALUES ('r4','dev-A')`); err == nil {
		t.Fatal("a second register for dev-A must be refused by the unique index")
	}
	// '' (unknown) may repeat
	if _, err := d.DB.Exec(`INSERT INTO register_devices (register_id, device_id) VALUES ('r4','')`); err != nil {
		t.Fatalf("an unknown device id may repeat: %v", err)
	}
}

// A phone that re-joined under three names leaves three tills rows with the
// same device id. The upgrade must boot, the NEWEST enrolment keeps the device
// link, the two older registers stay linked with "" (unknown): no row lost.
func TestMigration130ThreeTillsSharingOneDeviceBoots(t *testing.T) {
	d := openMigratedTo(t, filepath.Join(t.TempDir(), "r.db"), 119)
	defer d.Close()
	for _, s := range []string{
		`INSERT INTO registers (id, name, is_active) VALUES ('rOld','phone',1),('rMid','iphone',1),('rNew','phone2',1)`,
		`INSERT INTO tills (id, name, device_id) VALUES ('t1','phone','dev-P'),('t2','iphone','dev-P'),('t3','phone2','dev-P')`,
		`INSERT INTO audit_log (id, entity_type, entity_id, action, data_json, created_at) VALUES
		  ('a1','till','t1','till_enrolled','{"register_id":"rOld"}','2026-01-01T00:00:00Z'),
		  ('a2','till','t2','till_enrolled','{"register_id":"rMid"}','2026-02-01T00:00:00Z'),
		  ('a3','till','t3','till_enrolled','{"register_id":"rNew"}','2026-03-01T00:00:00Z')`,
	} {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("seed %.40s: %v", s, err)
		}
	}
	if err := d.migrateUpTo(131); err != nil {
		t.Fatalf("migration with duplicate device ids must boot: %v", err)
	}
	got := map[string]string{}
	rows, err := d.DB.Query(`SELECT register_id, device_id FROM register_devices`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r, dev string
		if err := rows.Scan(&r, &dev); err != nil {
			t.Fatal(err)
		}
		got[r] = dev
	}
	want := map[string]string{"rNew": "dev-P", "rMid": "", "rOld": ""}
	if len(got) != 3 || got["rNew"] != "dev-P" || got["rMid"] != "" || got["rOld"] != "" {
		t.Fatalf("link rows = %v, want %v", got, want)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM tills`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("tills rows = %d (%v), want 3 untouched", n, err)
	}
}
