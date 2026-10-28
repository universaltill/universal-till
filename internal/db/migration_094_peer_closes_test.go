package db

import "testing"

// 094 adds peer_closes and its change-log trigger (ut-docs#5074 M7): safe on
// a till with data, and the trigger logs only with the copy on, on the main
// till.
func TestMigration094_PeerCloses(t *testing.T) {
	d, path := openAtPreMigrationSchema(t, 94, "peer-closes.db")
	if _, err := d.DB.Exec(`INSERT INTO report_archive (id, kind, period, content_json) VALUES ('ra1', 'eod', 'p', '{}')`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d = openMigratedTo(t, path, 94)
	ins := func(id, period string) {
		t.Helper()
		if _, err := d.DB.Exec(`INSERT INTO peer_closes (till_id, id, kind, period, content_json, created_at, received_at) VALUES ('t2', ?, 'eod', ?, '{}', 'x', 'x')`, id, period); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		var n int
		_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales_mirror_log`).Scan(&n)
		return n
	}
	ins("c1", "p1")
	if count() != 0 {
		t.Fatal("logged with the copy off")
	}
	if _, err := d.DB.Exec(`INSERT INTO settings (key, value) VALUES ('sales_mirror.enabled', '1')`); err != nil {
		t.Fatal(err)
	}
	ins("c2", "p2")
	var origin string
	_ = d.DB.QueryRow(`SELECT origin_till_id FROM sales_mirror_log WHERE ref_id = 'c2'`).Scan(&origin)
	if count() != 1 || origin != "t2" {
		t.Fatalf("on: %d rows, origin %q", count(), origin)
	}
	if _, err := d.DB.Exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url', 'https://192.0.2.1:8090')`); err != nil {
		t.Fatal(err)
	}
	ins("c3", "p3")
	if count() != 1 {
		t.Fatal("a replica logged a peer close")
	}
	if _, err := d.DB.Exec(`INSERT INTO peer_closes (till_id, id, kind, period, content_json, created_at, received_at) VALUES ('t2', 'c9', 'eod', 'p1', '{}', 'x', 'x')`); err == nil {
		t.Fatal("a second close for the same till, kind and period was accepted")
	}
}
