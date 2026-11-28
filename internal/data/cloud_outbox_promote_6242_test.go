package data

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#6242: a replica holds a byte copy of the main till's cloud_outbox
// and push hashes from the join. The main uploads those edits itself, so
// promotion drops the open marks and the two hashes and keeps the sealed
// (in-flight) rows. "2020-01-01..." is far outside the 60-day fixed-date
// window and is not a value any real-clock check reads.

const inheritedOld = "2020-01-01T00:00:00Z" // fixed-date:ok inherited-mark age, outside the window

func outboxRow(t *testing.T, d *sql.DB, entity, id string, sealed bool) (changeID, body string, seq int64, ok bool) {
	t.Helper()
	cond := "sealed_at IS NULL"
	if sealed {
		cond = "sealed_at IS NOT NULL"
	}
	var c, b sql.NullString
	var s sql.NullInt64
	err := d.QueryRow(`SELECT change_id, body, seal_seq FROM cloud_outbox WHERE entity = ? AND local_id = ? AND `+cond, entity, id).Scan(&c, &b, &s)
	if err == sql.ErrNoRows {
		return "", "", 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return c.String, b.String, s.Int64, true
}

func settingVal(t *testing.T, d *sql.DB, k string) (string, bool) {
	t.Helper()
	var v string
	err := d.QueryRow(`SELECT value FROM settings WHERE key = ?`, k).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return v, true
}

func TestPromoteReplica_DropsInheritedOpenMarks_6242(t *testing.T) {
	d := openMigratedDB(t, "promote-drop.db")
	x := d.DB
	seedPromoteReplica(t, x, "cloud")
	clearOutbox(t, x)
	for _, m := range [][2]string{{"item", "i1"}, {"category", "c1"}, {"tax_code", "t1"}} {
		obExec(t, x, `INSERT INTO cloud_outbox (entity, local_id, created_at) VALUES (?, ?, ?)`, m[0], m[1], inheritedOld)
	}
	if err := NewSettingsRepo(x).PromoteReplica(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantMarks(t, x)
}

func TestPromoteReplica_KeepsInheritedSealedMarks_6242(t *testing.T) {
	d := openMigratedDB(t, "promote-sealed.db")
	x := d.DB
	seedPromoteReplica(t, x, "cloud")
	clearOutbox(t, x)
	obExec(t, x, `INSERT INTO cloud_outbox (entity, local_id, created_at, change_id, body, sealed_at, seal_seq) VALUES ('item', 'i1', ?, 'ch-1', '{"k":1}', ?, 7)`, inheritedOld, inheritedOld)
	obExec(t, x, `INSERT INTO cloud_outbox (entity, local_id, created_at) VALUES ('item', 'i1', ?)`, inheritedOld)
	if err := NewSettingsRepo(x).PromoteReplica(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, b, s, ok := outboxRow(t, x, "item", "i1", true)
	if !ok || c != "ch-1" || b != `{"k":1}` || s != 7 {
		t.Fatalf("sealed row = %q %q %d %v, want it untouched", c, b, s, ok)
	}
	wantMarks(t, x)
}

func TestPromoteReplica_IconMarkReopenedAfterDrop_6242(t *testing.T) {
	d := openMigratedDB(t, "promote-reopen.db")
	x := d.DB
	seedPromoteReplica(t, x, "cloud")
	linkItem(t, x, "i1", "lucide:beer", `{"icon":"","price_minor":100}`)
	obExec(t, x, `UPDATE items SET base_price = 120 WHERE id = 'i1'`)
	clearOutbox(t, x)
	obExec(t, x, `INSERT INTO cloud_outbox (entity, local_id, created_at) VALUES ('item', 'i1', ?)`, inheritedOld)
	if err := NewSettingsRepo(x).PromoteReplica(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantMarks(t, x, "item:i1")
	var created string
	if err := x.QueryRow(`SELECT created_at FROM cloud_outbox WHERE entity='item' AND local_id='i1' AND sealed_at IS NULL`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created == inheritedOld {
		t.Fatal("the open mark is the inherited one; want a fresh icon mark")
	}
}

func TestPromoteReplica_DropsPushHashes_6242(t *testing.T) {
	d := openMigratedDB(t, "promote-hash.db")
	x := d.DB
	seedPromoteReplica(t, x, "cloud")
	for k, v := range map[string]string{"cloudsync.snapshot_hash": "a", "cloudsync.order_tracking_hash": "b", "cloudsync.next_steps": "n"} {
		obExec(t, x, `INSERT INTO settings (key, value) VALUES (?, ?)`, k, v)
	}
	if err := NewSettingsRepo(x).PromoteReplica(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cloudsync.snapshot_hash", "cloudsync.order_tracking_hash"} {
		if _, ok := settingVal(t, x, k); ok {
			t.Fatalf("%s must be dropped", k)
		}
	}
	if v, ok := settingVal(t, x, "cloudsync.next_steps"); !ok || v != "n" {
		t.Fatal("cloudsync.next_steps must be kept")
	}
}

func TestPromoteReplica_KeepsLinksAndPullCursor_6242(t *testing.T) {
	d := openMigratedDB(t, "promote-links.db")
	x := d.DB
	seedPromoteReplica(t, x, "cloud")
	linkItem(t, x, "i1", "", `{"price_minor":100,"icon":""}`)
	linkCategory(t, x, "c1", "", `{"name":"x","icon":""}`)
	keys := []string{"cloudsync.catalog.cursor", "cloudsync.catalog.gen", "cloudsync.catalog.resync_gen"}
	for i, k := range keys {
		obExec(t, x, `INSERT INTO settings (key, value) VALUES (?, ?)`, k, string(rune('3'+i)))
	}
	snap := func() string {
		var s sql.NullString
		if err := x.QueryRow(`SELECT group_concat(entity||':'||cloud_id||':'||local_id||':'||version||':'||COALESCE(base,'NULL'), ';') FROM (SELECT * FROM cloud_links ORDER BY entity, local_id)`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		for _, k := range keys {
			v, _ := settingVal(t, x, k)
			s.String += "|" + v
		}
		return s.String
	}
	before := snap()
	if err := NewSettingsRepo(x).PromoteReplica(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := snap(); after != before {
		t.Fatalf("links/cursor changed:\n before %s\n after  %s", before, after)
	}
}

func TestPromoteReplica_NonReplicaKeepsOwnMarks_6242(t *testing.T) {
	d := openMigratedDB(t, "promote-nonreplica.db")
	x := d.DB
	obExec(t, x, `INSERT INTO settings (key, value) VALUES ('entitlement.catalog_master', 'cloud')`)
	clearOutbox(t, x)
	obExec(t, x, `INSERT INTO cloud_outbox (entity, local_id, created_at) VALUES ('item', 'i1', ?)`, inheritedOld)
	obExec(t, x, `INSERT INTO settings (key, value) VALUES ('cloudsync.snapshot_hash', 'a')`)
	obExec(t, x, `INSERT INTO settings (key, value) VALUES ('cloudsync.order_tracking_hash', 'b')`)
	if err := NewSettingsRepo(x).PromoteReplica(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantMarks(t, x, "item:i1")
	for _, k := range []string{"cloudsync.snapshot_hash", "cloudsync.order_tracking_hash"} {
		if _, ok := settingVal(t, x, k); !ok {
			t.Fatalf("%s must be kept on a till that was not a replica", k)
		}
	}
}

func TestPromoteReplica_DropFailureRollsBack_6242(t *testing.T) {
	d := openMigratedDB(t, "promote-dropfail.db")
	x := d.DB
	seedPromoteReplica(t, x, "cloud")
	clearOutbox(t, x)
	obExec(t, x, `INSERT INTO cloud_outbox (entity, local_id, created_at) VALUES ('item', 'i1', ?)`, inheritedOld)
	obExec(t, x, `INSERT INTO settings (key, value) VALUES ('cloudsync.snapshot_hash', 'a')`)
	obExec(t, x, `INSERT INTO settings (key, value) VALUES ('cloudsync.order_tracking_hash', 'b')`)
	obExec(t, x, `CREATE TRIGGER t6242 BEFORE DELETE ON cloud_outbox BEGIN SELECT RAISE(ABORT, 'boom'); END`)

	err := NewSettingsRepo(x).PromoteReplica(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the drop failure", err)
	}
	if v, _ := settingVal(t, x, "sync.primary_url"); v != "http://main.local" {
		t.Fatal("a failed promotion must leave sync.primary_url")
	}
	wantMarks(t, x, "item:i1")
	for _, k := range []string{"cloudsync.snapshot_hash", "cloudsync.order_tracking_hash"} {
		if _, ok := settingVal(t, x, k); !ok {
			t.Fatalf("%s must survive a rolled-back promotion", k)
		}
	}
}

func TestPromoteDropSettingsKeysAreRestoreDropKeys_6242(t *testing.T) {
	have := map[string]bool{}
	for _, k := range db.RestoreDropKeys {
		have[k] = true
	}
	if len(promoteDropSettingsKeys) == 0 {
		t.Fatal("promoteDropSettingsKeys is empty")
	}
	for _, k := range promoteDropSettingsKeys {
		if !have[k] {
			t.Errorf("%q is not in db.RestoreDropKeys", k)
		}
	}
}
