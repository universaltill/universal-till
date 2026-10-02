package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3340 review blocker: age_verifications.sale_id is a FOREIGN KEY to
// sales with no ON DELETE action (056_age_verifications.sql), and the WIP
// version left the table out of resetArchiveTables — so the first reset
// after any recorded ID check failed outright on `DELETE FROM sales` with
// "FOREIGN KEY constraint failed". This pins the full round trip: reset
// succeeds and archives every verification (accepted AND refused) under the
// batch, restore brings them back still pointing at the right sales.
func TestResetThenRestoreRoundTrip_AgeVerifications(t *testing.T) {
	d, x, count := resetTestDB(t, "restore_age_verifications.db")
	seedFullSale(t, x) // includes av1: accepted, sale s1, item i1
	// A second, REFUSED check on a different item, recorded with the same
	// sale (the till writes refusals in the sale's own transaction too).
	x(`INSERT INTO items (id, name, base_price, age_restricted) VALUES ('i2','Cider',300,1)`)
	x(`INSERT INTO age_verifications (id, sale_id, item_id, item_name, outcome, cashier_id, created_at)
	   VALUES ('av2','s1','i2','Cider 500ml','refused','u1','2026-01-01T00:00:01Z')`)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	if _, _, err := repo.ResetTransactionHistory(ctx, "", ""); err != nil {
		t.Fatalf("reset with recorded age verifications must not fail: %v", err)
	}
	batches, err := repo.ListResetBatches(ctx)
	if err != nil || len(batches) != 1 {
		t.Fatalf("ListResetBatches: %+v err=%v", batches, err)
	}
	batchID := batches[0].ID

	if c := count("age_verifications"); c != 0 {
		t.Fatalf("age_verifications not cleared by reset: %d row(s)", c)
	}
	if c := count("sales"); c != 0 {
		t.Fatalf("sales not cleared by reset: %d row(s)", c)
	}
	var archived int
	if err := d.DB.QueryRow(`SELECT count(*) FROM age_verifications_archive WHERE reset_batch_id = ?`, batchID).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if archived != 2 {
		t.Fatalf("age_verifications_archive holds %d row(s) for the batch, want 2", archived)
	}

	if _, err := repo.RestoreResetBatch(ctx, batchID, "", ""); err != nil {
		t.Fatalf("restore: %v", err)
	}
	type row struct{ saleID, itemID, itemName, outcome, cashierID, createdAt string }
	want := map[string]row{
		"av1": {"s1", "i1", "Widget", "accepted", "u1", "2026-01-01T00:00:00Z"},
		"av2": {"s1", "i2", "Cider 500ml", "refused", "u1", "2026-01-01T00:00:01Z"},
	}
	rows, err := d.DB.Query(`SELECT id, sale_id, item_id, item_name, outcome, cashier_id, created_at FROM age_verifications`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]row{}
	for rows.Next() {
		var id string
		var r row
		if err := rows.Scan(&id, &r.saleID, &r.itemID, &r.itemName, &r.outcome, &r.cashierID, &r.createdAt); err != nil {
			t.Fatal(err)
		}
		got[id] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("restored age_verifications = %+v, want %+v", got, want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("restored %s = %+v, want %+v", id, got[id], w)
		}
	}
	// The restored verification's sale_id must resolve against the live,
	// restored sale — not merely carry the same text.
	var linked int
	if err := d.DB.QueryRow(`SELECT count(*) FROM age_verifications av JOIN sales s ON s.id = av.sale_id`).Scan(&linked); err != nil || linked != 2 {
		t.Fatalf("restored verifications joined to live sales: %d err=%v, want 2", linked, err)
	}
	if c := count("age_verifications_archive"); c != 0 {
		t.Fatalf("age_verifications_archive should be empty after restore, got %d", c)
	}
}

// ut-docs#3340 review should-fix: an age-restricted item the cashier only
// ever REFUSED has an age_verifications row but no sale_lines/
// stock_movements row, so obsoleteItemsPredicate used to treat it as
// "never sold" and the DELETE FROM items hit the age_verifications.item_id
// FK, rolling back the whole cleanup (taking every genuinely obsolete item
// down with it). The refused item must be KEPT (an ID-check record is
// history) and the rest of the purge must still go through.
func TestCleanupObsoleteItems_KeepsItemWithOnlyARefusedAgeCheck(t *testing.T) {
	for _, includeActive := range []bool{false, true} {
		d, x, _ := resetTestDB(t, "cleanup-age-refused.db")
		x(`INSERT INTO users (id, username, display_name, role) VALUES ('u1','cashier1','Cashier One','cashier')`)
		x(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('s1','R1',0,0)`)
		x(`INSERT INTO items (id, name, base_price, is_active, age_restricted) VALUES ('refused','Old Cider',300,0,1)`)
		x(`INSERT INTO age_verifications (id, sale_id, item_id, item_name, outcome, cashier_id, created_at)
		   VALUES ('av1','s1','refused','Old Cider','refused','u1','2026-01-01T00:00:00Z')`)
		x(`INSERT INTO items (id, name, base_price, is_active) VALUES ('obs','Old Test Product',100,0)`)

		repo := data.NewPOSRepo(d.DB)
		ctx := context.Background()
		preview, err := repo.ListObsoleteItems(ctx, 100, includeActive)
		if err != nil {
			t.Fatalf("includeActive=%v ListObsoleteItems: %v", includeActive, err)
		}
		for _, it := range preview {
			if it.ID == "refused" {
				t.Fatalf("includeActive=%v: item with a recorded ID check must not be previewed as obsolete", includeActive)
			}
		}
		if _, err := repo.CleanupObsoleteItems(ctx, "", "", includeActive); err != nil {
			t.Fatalf("includeActive=%v cleanup must not fail on an age-check-referenced item: %v", includeActive, err)
		}
		has := func(id string) bool {
			var c int
			_ = d.DB.QueryRow(`SELECT count(*) FROM items WHERE id = ?`, id).Scan(&c)
			return c == 1
		}
		if !has("refused") {
			t.Fatalf("includeActive=%v: item referenced by an age verification was removed", includeActive)
		}
		if has("obs") {
			t.Fatalf("includeActive=%v: genuinely obsolete item not removed — the purge did not go through", includeActive)
		}
	}
}

// Same gap one step later: after a reset the only reference sits in
// age_verifications_archive. Deleting the item then would make the batch
// unrestorable (ErrArchiveReferencesRemoved), the same reason
// obsoleteItemsPredicate already checks sale_lines_archive.
func TestCleanupObsoleteItems_KeepsItemReferencedOnlyByArchivedAgeCheck(t *testing.T) {
	d, x, _ := resetTestDB(t, "cleanup-age-archive.db")
	x(`INSERT INTO users (id, username, display_name, role) VALUES ('u1','cashier1','Cashier One','cashier')`)
	x(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('s1','R1',0,0)`)
	x(`INSERT INTO items (id, name, base_price, is_active, age_restricted) VALUES ('refused','Old Cider',300,0,1)`)
	x(`INSERT INTO age_verifications (id, sale_id, item_id, item_name, outcome, cashier_id, created_at)
	   VALUES ('av1','s1','refused','Old Cider','refused','u1','2026-01-01T00:00:00Z')`)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	_, batchID, err := repo.ResetTransactionHistory(ctx, "", "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := repo.CleanupObsoleteItems(ctx, "", "", false); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	var c int
	if err := d.DB.QueryRow(`SELECT count(*) FROM items WHERE id = 'refused'`).Scan(&c); err != nil || c != 1 {
		t.Fatalf("item referenced only by an archived age verification was removed (count=%d err=%v)", c, err)
	}
	if _, err := repo.RestoreResetBatch(ctx, batchID, "", ""); err != nil {
		t.Fatalf("restore after cleanup: %v", err)
	}
}
