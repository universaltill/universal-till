package data

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// ut-docs#6240: promoting a replica re-runs the "promoting a field to
// cloud-mastered" recipe (contract cloud-catalog-changes.md 1.2.0) for the
// icons the cloud never received. A replica never marks, so migration 103
// left them unmarked.

func seedPromoteReplica(t *testing.T, db *sql.DB, mode string) {
	t.Helper()
	obExec(t, db, `INSERT INTO settings (key, value) VALUES ('sync.primary_url', 'http://main.local')`)
	obExec(t, db, `INSERT INTO settings (key, value) VALUES ('sync.till_id', 'replica-1')`)
	if mode != "" {
		obExec(t, db, `INSERT INTO settings (key, value) VALUES ('entitlement.catalog_master', ?)`, mode)
	}
}

// linkItem inserts a local item and its cloud link with the given base (a nil
// base is SQL NULL); callers clear the outbox the inserts may have marked.
func linkItem(t *testing.T, db *sql.DB, id, icon string, base any) {
	t.Helper()
	obExec(t, db, `INSERT INTO items (id, sku, name, base_price, icon) VALUES (?, ?, 'N', 100, ?)`, id, "S-"+id, icon)
	obExec(t, db, `INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at, base) VALUES ('item', ?, ?, 1, 'x', ?)`, "cl-"+id, id, base)
}

func linkCategory(t *testing.T, db *sql.DB, id, icon string, base any) {
	t.Helper()
	obExec(t, db, `INSERT INTO categories (id, name, icon) VALUES (?, 'C', ?)`, id, icon)
	obExec(t, db, `INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at, base) VALUES ('category', ?, ?, 1, 'x', ?)`, "cl-"+id, id, base)
}

func baseIconType(t *testing.T, db *sql.DB, entity, id string) string {
	t.Helper()
	var s sql.NullString
	if err := db.QueryRow(`SELECT json_type(base, '$.icon') FROM cloud_links WHERE entity = ? AND local_id = ?`, entity, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s.String
}

func TestPromoteReplica_MarksUnsentIcons_6240(t *testing.T) {
	d := openMigratedDB(t, "promote-marks.db")
	db := d.DB
	seedPromoteReplica(t, db, "cloud")
	linkItem(t, db, "i1", "lucide:beer", `{"icon":""}`)
	linkCategory(t, db, "c1", "lucide:coffee", `{"icon":""}`)
	clearOutbox(t, db)

	if err := NewSettingsRepo(db).PromoteReplica(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantMarks(t, db, "item:i1", "category:c1")
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'sync.primary_url'`).Scan(&n)
	if n != 0 {
		t.Fatal("sync.primary_url must be cleared")
	}
}

func TestPromoteReplica_IconMarkSkips_6240(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, db *sql.DB)
	}{
		{"linked, icon empty", func(t *testing.T, db *sql.DB) { linkItem(t, db, "i1", "", `{"icon":"lucide:beer"}`) }},
		{"unlinked, icon set", func(t *testing.T, db *sql.DB) {
			obExec(t, db, `INSERT INTO items (id, sku, name, base_price, icon) VALUES ('i1', 'S1', 'N', 100, 'lucide:beer')`)
		}},
		{"base equals local", func(t *testing.T, db *sql.DB) { linkItem(t, db, "i1", "lucide:beer", `{"icon":"lucide:beer"}`) }},
		{"NULL base", func(t *testing.T, db *sql.DB) { linkItem(t, db, "i1", "lucide:beer", nil) }},
		{"non-JSON base", func(t *testing.T, db *sql.DB) { linkItem(t, db, "i1", "lucide:beer", `not json`) }},
		{"row deleted", func(t *testing.T, db *sql.DB) {
			linkItem(t, db, "i1", "lucide:beer", `{"icon":""}`)
			obExec(t, db, `DELETE FROM items WHERE id = 'i1'`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openMigratedDB(t, "promote-skips.db")
			db := d.DB
			seedPromoteReplica(t, db, "cloud")
			tc.setup(t, db)
			clearOutbox(t, db)
			if err := NewSettingsRepo(db).PromoteReplica(context.Background()); err != nil {
				t.Fatal(err)
			}
			wantMarks(t, db)
		})
	}
}

func TestPromoteReplica_KeepsOneOpenMark_6240(t *testing.T) {
	d := openMigratedDB(t, "promote-onemark.db")
	db := d.DB
	seedPromoteReplica(t, db, "cloud")
	linkItem(t, db, "i1", "lucide:beer", `{"icon":""}`)
	clearOutbox(t, db)
	obExec(t, db, `INSERT INTO cloud_outbox (entity, local_id, created_at) VALUES ('item', 'i1', 'x')`)
	if err := NewSettingsRepo(db).PromoteReplica(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantMarks(t, db, "item:i1")
}

func TestPromoteReplica_SeedsMissingIconBaseKey_6240(t *testing.T) {
	d := openMigratedDB(t, "promote-seed.db")
	db := d.DB
	seedPromoteReplica(t, db, "cloud")
	linkItem(t, db, "i1", "lucide:beer", `{"name":"x"}`)
	clearOutbox(t, db)
	if err := NewSettingsRepo(db).PromoteReplica(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := baseIconType(t, db, "item", "i1"); got != "text" {
		t.Fatalf("base icon json_type = %q, want text", got)
	}
	wantMarks(t, db, "item:i1")
}

func TestPromoteReplica_NoMarksOutsideCloudMode_6240(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		suppress bool
		want     []string
	}{
		{"till", "till", false, nil},
		{"unset", "", false, nil},
		{"cloud suppressed", "cloud", true, nil},
		{"adopting", "adopting", false, []string{"item:i1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openMigratedDB(t, "promote-modes.db")
			db := d.DB
			seedPromoteReplica(t, db, tc.mode)
			linkItem(t, db, "i1", "lucide:beer", `{"icon":""}`)
			clearOutbox(t, db)
			if tc.suppress {
				obExec(t, db, `INSERT INTO cloud_outbox_suppress (one) VALUES (1)`)
			}
			if err := NewSettingsRepo(db).PromoteReplica(context.Background()); err != nil {
				t.Fatal(err)
			}
			wantMarks(t, db, tc.want...)
			var n int
			_ = db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'sync.primary_url'`).Scan(&n)
			if n != 0 {
				t.Fatal("identity must be cleared")
			}
		})
	}
}

func TestPromoteReplica_MarkFailureRollsBack_6240(t *testing.T) {
	d := openMigratedDB(t, "promote-fail.db")
	db := d.DB
	seedPromoteReplica(t, db, "cloud")
	obExec(t, db, `INSERT INTO settings (key, value) VALUES ('till.name', 'Old Main')`)
	obExec(t, db, `INSERT INTO settings (key, value) VALUES ('lan_discovery.till_id', 'disc-1')`)
	linkItem(t, db, "i1", "lucide:beer", `{"icon":""}`)
	clearOutbox(t, db)
	obExec(t, db, `CREATE TRIGGER t6240 BEFORE INSERT ON cloud_outbox BEGIN SELECT RAISE(ABORT, 'boom'); END`)

	err := NewSettingsRepo(db).PromoteReplica(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the marking failure", err)
	}
	get := func(k string) string {
		var v string
		_ = db.QueryRow(`SELECT value FROM settings WHERE key = ?`, k).Scan(&v)
		return v
	}
	if get("sync.primary_url") != "http://main.local" || get("till.name") != "Old Main" || get("lan_discovery.till_id") != "disc-1" {
		t.Fatal("a failed promotion must leave the replica identity untouched")
	}
	wantMarks(t, db)
}

func TestMarkUnsentPromotedFields_Idempotent_6240(t *testing.T) {
	d := openMigratedDB(t, "promote-idem.db")
	db := d.DB
	obExec(t, db, `INSERT INTO settings (key, value) VALUES ('entitlement.catalog_master', 'cloud')`)
	linkItem(t, db, "i1", "lucide:beer", `{"icon":""}`)
	linkCategory(t, db, "c1", "lucide:coffee", `{"name":"x"}`)
	clearOutbox(t, db)
	repo := NewCloudOutboxRepo()
	n, err := repo.MarkUnsentPromotedFields(context.Background(), db, "2000-01-01T00:00:00Z")
	if err != nil || n != 2 {
		t.Fatalf("first run = %d, %v; want 2 marks", n, err)
	}
	if got := baseIconType(t, db, "category", "c1"); got != "text" {
		t.Fatalf("category base icon type = %q", got)
	}
	before := allBases(t, db)
	n, err = repo.MarkUnsentPromotedFields(context.Background(), db, "2000-01-01T00:00:00Z")
	if err != nil || n != 0 {
		t.Fatalf("second run = %d, %v; want 0", n, err)
	}
	if after := allBases(t, db); after != before {
		t.Fatalf("second run changed bases:\n before %s\n after  %s", before, after)
	}
}

func allBases(t *testing.T, db *sql.DB) string {
	t.Helper()
	var s sql.NullString
	if err := db.QueryRow(`SELECT group_concat(entity || ':' || local_id || '=' || COALESCE(base, 'NULL'), ';') FROM (SELECT * FROM cloud_links ORDER BY entity, local_id)`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s.String
}

// The sync.primary_url clause of the gate: called directly on a replica (the
// identity still set) in cloud mode, it marks nothing, like the outbox
// triggers. Base seeding is ungated, as in migration 103.
func TestMarkUnsentPromotedFields_NoMarksOnReplica_6240(t *testing.T) {
	d := openMigratedDB(t, "promote-replica.db")
	db := d.DB
	seedPromoteReplica(t, db, "cloud")
	linkItem(t, db, "i1", "lucide:beer", `{"name":"x"}`)
	clearOutbox(t, db)
	n, err := NewCloudOutboxRepo().MarkUnsentPromotedFields(context.Background(), db, "2000-01-01T00:00:00Z")
	if err != nil || n != 0 {
		t.Fatalf("on a replica = %d, %v; want 0 marks", n, err)
	}
	wantMarks(t, db)
	if got := baseIconType(t, db, "item", "i1"); got != "text" {
		t.Fatalf("item base icon type = %q, want seeded text", got)
	}
}
