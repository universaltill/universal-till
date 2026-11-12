package data

import (
	"context"
	"database/sql"
	"testing"
)

// openMarks returns the open marks as entity:local_id.
func openMarkKeys(t *testing.T, db *sql.DB) []string {
	t.Helper()
	ms, err := NewCloudOutboxRepo().OpenMarks(context.Background(), db, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range ms {
		out = append(out, m.Entity+":"+m.LocalID)
	}
	return out
}

func obExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func clearOutbox(t *testing.T, db *sql.DB) {
	t.Helper()
	obExec(t, db, `DELETE FROM cloud_outbox`)
}

func wantMarks(t *testing.T, db *sql.DB, want ...string) {
	t.Helper()
	got := openMarkKeys(t, db)
	if len(got) != len(want) {
		t.Fatalf("open marks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("open marks = %v, want %v", got, want)
		}
	}
}

// ADR-0162 §5 (ut-docs#4464): on a cloud-master main till every write to
// the uploadable mastered set leaves an outbox mark in the same
// transaction (migration 084's triggers): items (and their item-level
// barcodes), categories, tax codes. Repeated edits share one open mark;
// columns outside the upload contract, another mode, an additional till
// and a suppressed (pull) transaction leave none.
func TestCloudOutboxTriggers(t *testing.T) {
	d := openMigratedDB(t, "cloud-outbox.db")
	db := d.DB
	ctx := context.Background()
	setMode := func(v string) {
		obExec(t, db, `INSERT INTO settings (key, value) VALUES ('entitlement.catalog_master', ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, v)
	}

	// Till-master (the default): nothing is marked.
	obExec(t, db, `INSERT INTO categories (id, name) VALUES ('c1', 'Drinks')`)
	wantMarks(t, db)

	for _, mode := range []string{"cloud", "adopting"} {
		setMode(mode)
		clearOutbox(t, db)
		obExec(t, db, `UPDATE categories SET name = 'Drinks ' || ? WHERE id = 'c1'`, mode)
		wantMarks(t, db, "category:c1")
	}

	clearOutbox(t, db)
	obExec(t, db, `INSERT INTO items (id, sku, name, base_price) VALUES ('i1', 'S1', 'Latte', 250)`)
	obExec(t, db, `UPDATE items SET base_price = 260 WHERE id = 'i1'`)
	obExec(t, db, `UPDATE items SET name = 'Latte L' WHERE id = 'i1'`)
	obExec(t, db, `INSERT INTO tax_codes (id, name, rate_basis_points) VALUES ('t9', 'Nine', 900)`)
	wantMarks(t, db, "item:i1", "tax_code:t9") // one open mark per row, however many edits

	// A column outside the upload contract marks nothing.
	clearOutbox(t, db)
	obExec(t, db, `UPDATE items SET color = '#ff0000', updated_at = datetime('now') WHERE id = 'i1'`)
	obExec(t, db, `UPDATE categories SET color = '#00ff00' WHERE id = 'c1'`)
	wantMarks(t, db)

	// A barcode edit marks its item; so does a delete.
	obExec(t, db, `INSERT INTO item_barcodes (barcode, item_id, is_primary) VALUES ('5000', 'i1', 1)`)
	wantMarks(t, db, "item:i1")
	clearOutbox(t, db)
	obExec(t, db, `DELETE FROM item_barcodes WHERE barcode = '5000'`)
	wantMarks(t, db, "item:i1")

	// A sealed (in-flight) mark is never reopened: a later edit opens a
	// second mark.
	outbox := NewCloudOutboxRepo()
	ms, _ := outbox.OpenMarks(ctx, db, 0, 10)
	if ok, err := outbox.Seal(ctx, db, ms[0].ID, "chg-1", "{}", "2026-10-16T00:00:00Z"); err != nil || !ok {
		t.Fatalf("seal: %v %v", ok, err)
	}
	obExec(t, db, `UPDATE items SET is_active = 0 WHERE id = 'i1'`)
	wantMarks(t, db, "item:i1")
	if open, sealed, _ := outbox.Counts(ctx, db); open != 1 || sealed != 1 {
		t.Fatalf("counts = %d open, %d sealed; want 1, 1", open, sealed)
	}
	if has, _ := outbox.HasMark(ctx, db, "item", "i1"); !has {
		t.Fatal("HasMark = false")
	}

	// A suppressed transaction (the pull's) marks nothing; the flag never
	// outlives it.
	clearOutbox(t, db)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.Suppress(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE items SET base_price = 999 WHERE id = 'i1'`); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Unsuppress(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	wantMarks(t, db)
	obExec(t, db, `UPDATE items SET base_price = 1000 WHERE id = 'i1'`)
	wantMarks(t, db, "item:i1")

	// An additional till (sync.primary_url set) never marks: its edits are
	// written through to the main till, which marks them.
	clearOutbox(t, db)
	obExec(t, db, `INSERT INTO settings (key, value) VALUES ('sync.primary_url', 'https://main.local:8443')`)
	obExec(t, db, `UPDATE items SET base_price = 1 WHERE id = 'i1'`)
	wantMarks(t, db)
	obExec(t, db, `UPDATE settings SET value = '  ' WHERE key = 'sync.primary_url'`)
	obExec(t, db, `UPDATE items SET base_price = 2 WHERE id = 'i1'`)
	wantMarks(t, db, "item:i1")

	// Deleting the item marks it too (the uploader then sends a delete).
	clearOutbox(t, db)
	obExec(t, db, `DELETE FROM items WHERE id = 'i1'`)
	wantMarks(t, db, "item:i1")

	// EnsureOpenMark never duplicates an open mark.
	if err := outbox.EnsureOpenMark(ctx, db, "item", "i1", "2026-10-16T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	wantMarks(t, db, "item:i1")
}

// cloud_links' base and echo (migration 084) survive Upsert, and the
// item-barcode link lookup matches only that item's item-level records.
func TestCloudLinkBaseAndEcho(t *testing.T) {
	d := openMigratedDB(t, "cloud-link-state.db")
	db := d.DB
	ctx := context.Background()
	links := NewCloudLinkRepo()
	for _, l := range []CloudLink{
		{Entity: "item", CloudID: "c1", LocalID: "i1", Version: 3},
		{Entity: "item_barcode", CloudID: "b1", LocalID: "item:i1:5000", Version: 3},
		{Entity: "item_barcode", CloudID: "b2", LocalID: "item:i10:5001", Version: 3},
		{Entity: "item_barcode", CloudID: "b3", LocalID: "variant:v1:5002", Version: 3},
	} {
		if err := links.Upsert(ctx, db, l); err != nil {
			t.Fatal(err)
		}
	}
	if err := links.SetBase(ctx, db, "item", "c1", `{"name":"Latte"}`); err != nil {
		t.Fatal(err)
	}
	if err := links.SetEcho(ctx, db, "item", "c1", 7); err != nil {
		t.Fatal(err)
	}
	if err := links.Upsert(ctx, db, CloudLink{Entity: "item", CloudID: "c1", LocalID: "i1", Version: 8}); err != nil {
		t.Fatal(err)
	}
	st, ok, err := links.StateByLocal(ctx, db, "item", "i1")
	if err != nil || !ok || st.Base != `{"name":"Latte"}` || st.EchoVersion != 7 || st.Version != 8 || st.CloudID != "c1" {
		t.Fatalf("state = %+v %v %v", st, ok, err)
	}
	bls, err := links.ItemBarcodeLinks(ctx, db, "item_barcode", "i1")
	if err != nil || len(bls) != 1 || bls[0].CloudID != "b1" {
		t.Fatalf("item barcode links = %+v %v, want only b1", bls, err)
	}
	if err := links.ClearEchoes(ctx, db); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := links.State(ctx, db, "item", "c1"); st.EchoVersion != 0 {
		t.Fatalf("echo after ClearEchoes = %d", st.EchoVersion)
	}
	if err := links.SetBase(ctx, db, "item", "c1", ""); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := links.State(ctx, db, "item", "c1"); st.Base != "" {
		t.Fatalf("base after clear = %q", st.Base)
	}
}

// The in-flight check sees only sealed marks; the base-less listing only
// pulled links (version > 0) of the asked kind with no base, in cloud_id
// order after the cursor.
func TestCloudOutboxInFlightAndBaselessLinks(t *testing.T) {
	d := openMigratedDB(t, "cloud-outbox-inflight.db")
	db := d.DB
	ctx := context.Background()
	ob, links := NewCloudOutboxRepo(), NewCloudLinkRepo()
	if err := ob.EnsureOpenMark(ctx, db, "item", "i1", "2026-10-17T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if got, err := ob.HasSealedMark(ctx, db, "item", "i1"); err != nil || got {
		t.Fatalf("open mark counted as in flight: %v %v", got, err)
	}
	m, _, err := ob.OpenMarkFor(ctx, db, "item", "i1")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := ob.Seal(ctx, db, m.ID, "c1", "{}", "2026-10-17T00:00:00Z"); err != nil || !ok {
		t.Fatalf("seal: %v %v", ok, err)
	}
	if got, err := ob.HasSealedMark(ctx, db, "item", "i1"); err != nil || !got {
		t.Fatalf("sealed mark not in flight: %v %v", got, err)
	}
	if got, _ := ob.HasSealedMark(ctx, db, "category", "i1"); got {
		t.Fatal("another kind's mark counted")
	}
	for _, l := range []CloudLink{
		{Entity: "item", CloudID: "a", LocalID: "la", Version: 3},
		{Entity: "item", CloudID: "b", LocalID: "lb", Version: 0}, // a till create
		{Entity: "item", CloudID: "c", LocalID: "lc", Version: 4},
		{Entity: "item", CloudID: "d", LocalID: "ld", Version: 5},
		{Entity: "category", CloudID: "e", LocalID: "le", Version: 1},
	} {
		if err := links.Upsert(ctx, db, l); err != nil {
			t.Fatal(err)
		}
	}
	if err := links.SetBase(ctx, db, "item", "c", `{"name":"x"}`); err != nil {
		t.Fatal(err)
	}
	got, err := links.LinksWithoutBase(ctx, db, "item", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].CloudID != "a" || got[1].CloudID != "d" {
		t.Fatalf("base-less items = %+v, want a and d", got)
	}
	if got, _ = links.LinksWithoutBase(ctx, db, "item", "a", 10); len(got) != 1 || got[0].CloudID != "d" {
		t.Fatalf("after a = %+v, want d", got)
	}
}

// ut-docs#6088: cloud_outbox_held keeps a row's edits that the cloud's change
// upload cannot take yet. HoldMark keeps the earliest created_at, so a held
// edit still ages from its first edit (ADR-0162 §5's age bound).
func TestHoldMarkKeepsEarliestCreatedAt(t *testing.T) {
	d := openMigratedDB(t, "outbox-hold.db")
	db, ctx, r := d.DB, context.Background(), NewCloudOutboxRepo()
	if err := r.HoldMark(ctx, db, "item", "i1", "2026-11-02T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := r.HoldMark(ctx, db, "item", "i1", "2026-11-05T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT created_at FROM cloud_outbox_held WHERE entity = 'item' AND local_id = 'i1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "2026-11-02T10:00:00Z" {
		t.Fatalf("created_at = %q, want the earliest", got)
	}
	if n, _ := r.CountHeld(ctx, db); n != 1 {
		t.Fatalf("CountHeld = %d, want 1", n)
	}
	if ok, _ := r.HasHeld(ctx, db, "item", "i1"); !ok {
		t.Fatal("HasHeld = false")
	}
	if ok, _ := r.HasMark(ctx, db, "item", "i1"); ok {
		t.Fatal("a held row is not an outbox mark (HasMark must stay false)")
	}
	if err := r.DeleteHeld(ctx, db, "item", "i1"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.HasHeld(ctx, db, "item", "i1"); ok {
		t.Fatal("HasHeld after DeleteHeld = true")
	}
}

// Releasing held rows opens a mark for each (oldest first), merges into a
// row's already-open mark keeping the earlier created_at, leaves a sealed
// mark alone, and empties the held table.
func TestReleaseHeldMergesIntoOpenMark(t *testing.T) {
	d := openMigratedDB(t, "outbox-release.db")
	db, ctx, r := d.DB, context.Background(), NewCloudOutboxRepo()
	obExec(t, db, `INSERT INTO cloud_outbox (entity, local_id, created_at) VALUES ('item', 'open-newer', '2026-11-09T00:00:00Z')`)
	obExec(t, db, `INSERT INTO cloud_outbox (entity, local_id, created_at) VALUES ('item', 'open-older', '2026-11-01T00:00:00Z')`)
	obExec(t, db, `INSERT INTO cloud_outbox (entity, local_id, created_at, change_id, body, sealed_at, seal_seq) VALUES ('item', 'sealed', '2026-11-08T00:00:00Z', 'c1', '{}', '2026-11-08T00:00:00Z', 1)`)
	for _, h := range [][2]string{{"open-newer", "2026-11-03T00:00:00Z"}, {"open-older", "2026-11-05T00:00:00Z"}, {"sealed", "2026-11-04T00:00:00Z"}, {"fresh-b", "2026-11-07T00:00:00Z"}, {"fresh-a", "2026-11-06T00:00:00Z"}} {
		if err := r.HoldMark(ctx, db, "item", h[0], h[1]); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := r.ReleaseHeld(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("ReleaseHeld = %d, want 5", n)
	}
	if c, _ := r.CountHeld(ctx, db); c != 0 {
		t.Fatalf("held rows left = %d", c)
	}
	created := map[string]string{}
	rows, err := db.Query(`SELECT local_id, created_at FROM cloud_outbox WHERE sealed_at IS NULL ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var order []string
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			t.Fatal(err)
		}
		created[id] = at
		order = append(order, id)
	}
	want := map[string]string{
		"open-newer": "2026-11-03T00:00:00Z", // the held edit is older: it wins
		"open-older": "2026-11-01T00:00:00Z", // the open mark is older: it stays
		"fresh-a":    "2026-11-06T00:00:00Z",
		"fresh-b":    "2026-11-07T00:00:00Z",
		"sealed":     "2026-11-04T00:00:00Z", // no open mark: one opens beside the sealed one
	}
	if len(created) != len(want) {
		t.Fatalf("open marks = %v, want %v", created, want)
	}
	for id, at := range want {
		if created[id] != at {
			t.Errorf("open mark %s created_at = %q, want %q", id, created[id], at)
		}
	}
	// Newly opened marks are in created_at order (the oldest edit uploads first).
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	if !(pos["sealed"] < pos["fresh-a"] && pos["fresh-a"] < pos["fresh-b"]) {
		t.Errorf("new open marks not in created_at order: %v", order)
	}
}

// Reseal swaps a sealed mark's change_id and body in place, keeping its
// seal_seq (the reference order) and sealed_at; an open mark is not resealed.
func TestResealKeepsSealSeq(t *testing.T) {
	d := openMigratedDB(t, "outbox-reseal.db")
	db, ctx, r := d.DB, context.Background(), NewCloudOutboxRepo()
	obExec(t, db, `INSERT INTO cloud_outbox (entity, local_id, created_at, change_id, body, sealed_at, seal_seq) VALUES ('item', 'a', '2026-11-08T00:00:00Z', 'c1', '{"old":1}', '2026-11-08T01:00:00Z', 7)`)
	obExec(t, db, `INSERT INTO cloud_outbox (entity, local_id, created_at) VALUES ('item', 'b', '2026-11-08T00:00:00Z')`)
	var sealedID, openID int64
	if err := db.QueryRow(`SELECT id FROM cloud_outbox WHERE local_id = 'a'`).Scan(&sealedID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM cloud_outbox WHERE local_id = 'b'`).Scan(&openID); err != nil {
		t.Fatal(err)
	}
	ok, err := r.Reseal(ctx, db, sealedID, "c2", `{"new":1}`)
	if err != nil || !ok {
		t.Fatalf("Reseal = %v, %v", ok, err)
	}
	var cid, body, at string
	var seq int64
	if err := db.QueryRow(`SELECT change_id, body, sealed_at, seal_seq FROM cloud_outbox WHERE id = ?`, sealedID).Scan(&cid, &body, &at, &seq); err != nil {
		t.Fatal(err)
	}
	if cid != "c2" || body != `{"new":1}` || at != "2026-11-08T01:00:00Z" || seq != 7 {
		t.Fatalf("resealed = %q %q %q %d", cid, body, at, seq)
	}
	if ok, err := r.Reseal(ctx, db, openID, "c3", `{}`); err != nil || ok {
		t.Fatalf("Reseal of an open mark = %v, %v; want false", ok, err)
	}
}
