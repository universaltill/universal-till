package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// ut-docs#6071: migration 103 makes the item and category icon upload. It
// (1) marks each linked row whose local icon is set, where the update
// trigger would mark, and (2) sets every linked row's upload base icon to
// "" (the value the cloud holds: it never wrote the column), so the first
// upload seeds the cloud and the pull keeps the till icon until then.
func seed103(t *testing.T, path, master, primaryURL string) {
	t.Helper()
	d := openMigratedTo(t, path, 102)
	defer d.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO categories (id, name, icon) VALUES ('k-icon', 'Hot', 'lucide:coffee'), ('k-none', 'Cold', NULL), ('k-empty', 'Warm', ''), ('k-bad', 'Odd', 'lucide:tea'), ('k-nobase', 'Nb', 'lucide:tea')`)
	exec(`INSERT INTO items (id, sku, name, base_price, icon) VALUES
 ('i-icon', 'S1', 'Cola', 250, 'lucide:beer'),
 ('i-none', 'S2', 'Bread', 200, NULL),
 ('i-empty', 'S3', 'Milk', 100, ''),
 ('i-has', 'S4', 'Tea', 100, 'lucide:coffee'),
 ('i-bad', 'S5', 'Odd', 100, 'lucide:beer'),
 ('i-nobase', 'S6', 'Nb', 100, 'lucide:beer'),
 ('i-marked', 'S7', 'Marked', 100, 'lucide:beer')`)
	const old = `{"name":"x","price_minor":250}`
	exec(`INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at, base) VALUES
 ('item', 'c-icon', 'i-icon', 1, 'now', ?),
 ('item', 'c-none', 'i-none', 1, 'now', ?),
 ('item', 'c-empty', 'i-empty', 1, 'now', ?),
 ('item', 'c-has', 'i-has', 1, 'now', '{"name":"x","icon":"lucide:coffee"}'),
 ('item', 'c-bad', 'i-bad', 1, 'now', 'not json'),
 ('item', 'c-nobase', 'i-nobase', 1, 'now', NULL),
 ('item', 'c-marked', 'i-marked', 1, 'now', ?),
 ('item', 'c-gone', 'i-gone', 1, 'now', ?),
 ('category', 'k1', 'k-icon', 1, 'now', ?),
 ('category', 'k2', 'k-none', 1, 'now', ?),
 ('category', 'k3', 'k-empty', 1, 'now', ?),
 ('category', 'k4', 'k-bad', 1, 'now', 'not json'),
 ('category', 'k5', 'k-nobase', 1, 'now', NULL)`, old, old, old, old, old, old, old, old)
	exec(`INSERT INTO cloud_outbox (entity, local_id, created_at) VALUES ('item', 'i-marked', '2000-01-01T00:00:00Z')`)
	if master != "" {
		exec(`INSERT INTO settings (key, value) VALUES ('entitlement.catalog_master', ?)`, master)
	}
	if primaryURL != "" {
		exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url', ?)`, primaryURL)
	}
}

func marks103(t *testing.T, d *DB) map[string]bool {
	t.Helper()
	rows, err := d.DB.Query(`SELECT entity || ':' || local_id FROM cloud_outbox`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		out[k] = true
	}
	return out
}

func iconBase103(t *testing.T, d *DB, entity, cloudID string) (typ sql.NullString, val sql.NullString) {
	t.Helper()
	if err := d.DB.QueryRow(`SELECT json_type(base, '$.icon'), json_extract(base, '$.icon') FROM cloud_links WHERE entity = ? AND cloud_id = ?`, entity, cloudID).Scan(&typ, &val); err != nil {
		t.Fatal(err)
	}
	return
}

func TestMigration103_SeedsIconBasesAndMarks(t *testing.T) {
	loadMigrationVersion(t, 103) // fails loudly if renumbered off disk
	path := filepath.Join(t.TempDir(), "outbox-icon.db")
	seed103(t, path, "cloud", "")
	d := openMigratedTo(t, path, 103)
	defer d.Close()

	// Bases: an absent icon key becomes "" (the cloud's value), a present
	// one is left alone, a damaged or NULL base too.
	for _, c := range [][2]string{{"item", "c-icon"}, {"item", "c-none"}, {"item", "c-empty"}, {"item", "c-marked"}, {"category", "k1"}, {"category", "k2"}, {"category", "k3"}} {
		typ, val := iconBase103(t, d, c[0], c[1])
		if typ.String != "text" || val.String != "" {
			t.Fatalf("%s %s base icon = %v %q, want the empty string", c[0], c[1], typ, val.String)
		}
	}
	if _, v := iconBase103(t, d, "item", "c-has"); v.String != "lucide:coffee" {
		t.Fatalf("c-has base icon = %q, a present icon must stay", v.String)
	}
	var b sql.NullString
	if err := d.DB.QueryRow(`SELECT base FROM cloud_links WHERE cloud_id = 'c-bad'`).Scan(&b); err != nil || b.String != "not json" {
		t.Fatalf("a damaged base changed: %v %v", b, err)
	}
	for _, id := range []string{"c-nobase", "k5"} {
		if err := d.DB.QueryRow(`SELECT base FROM cloud_links WHERE cloud_id = ?`, id).Scan(&b); err != nil || b.Valid {
			t.Fatalf("%s: a NULL base was filled: %v %v", id, b, err)
		}
	}

	// Marks: exactly the linked rows with a base that gained the "" icon
	// and a non-empty local icon (an existing mark is not duplicated).
	want := map[string]bool{"item:i-icon": true, "item:i-marked": true, "category:k-icon": true}
	got := marks103(t, d)
	if len(got) != len(want) {
		t.Fatalf("marks = %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("marks = %v, want %v", got, want)
		}
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM cloud_outbox WHERE local_id = 'i-marked'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("i-marked marks = %d, %v; want its one existing mark", n, err)
	}

	// A rerun is a no-op (the bases have the key, so no row is marked
	// again, and a consumed mark is not resurrected).
	if _, err := d.DB.Exec(`DELETE FROM cloud_outbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(loadMigrationVersion(t, 103).SQL); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if got := marks103(t, d); len(got) != 0 {
		t.Fatalf("re-run marked %v", got)
	}

	// The update triggers watch icon now (an item and a category).
	exec := func(q string) {
		t.Helper()
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`UPDATE items SET icon = 'lucide:wine' WHERE id = 'i-none'`)
	exec(`UPDATE categories SET icon = 'lucide:wine' WHERE id = 'k-none'`)
	got = marks103(t, d)
	if !got["item:i-none"] || !got["category:k-none"] || len(got) != 2 {
		t.Fatalf("trigger marks = %v, want the item and the category icon edit", got)
	}
}

// Outside cloud/adopting mode, on a replica (a sync.primary_url), and in
// till-master mode no mark is written (the trigger would not mark either);
// the bases are still seeded.
func TestMigration103_NoMarksWhereTheTriggerWouldNotMark(t *testing.T) {
	for name, c := range map[string]struct{ master, primary string }{
		"till master":   {"till", ""},
		"unset":         {"", ""},
		"replica":       {"cloud", "http://main.local"},
		"adopting main": {"adopting", ""},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "outbox-icon.db")
			seed103(t, path, c.master, c.primary)
			if _, err := func() (sql.Result, error) {
				d := openMigratedTo(t, path, 102)
				defer d.Close()
				return d.DB.Exec(`DELETE FROM cloud_outbox`)
			}(); err != nil {
				t.Fatal(err)
			}
			d := openMigratedTo(t, path, 103)
			defer d.Close()
			got := marks103(t, d)
			wantMarks := c.master == "adopting" // the one mode here where the trigger marks
			if wantMarks != (len(got) == 3) || (!wantMarks && len(got) != 0) {
				t.Fatalf("marks = %v (wantMarks %v)", got, wantMarks)
			}
			if typ, val := iconBase103(t, d, "item", "c-icon"); typ.String != "text" || val.String != "" {
				t.Fatalf("base icon = %v %q, want it seeded whatever the mode", typ, val.String)
			}
		})
	}
}
