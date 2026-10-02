package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3452: "Clear transaction history" (ADR-0042) used to leave
// shrinkage_events in place, so a void/comp/waste rung up while training
// before go-live stayed in the Shrinkage & Loss report and pinned its item
// against Catalog cleanup / Remove sample data for good. Reset now moves the
// rows into shrinkage_events_archive (migration 063) like every other
// transactional table, and restore brings them back unchanged.
func TestResetThenRestoreRoundTrip_ShrinkageEvents(t *testing.T) {
	d, x, count := resetTestDB(t, "restore_shrinkage_events.db")
	seedFullSale(t, x) // items i1, register r1, user u1
	x(`INSERT INTO users (id, username, display_name, role) VALUES ('m1','manager1','Manager One','manager')`)
	x(`INSERT INTO shrinkage_events (id, reason_category, item_id, item_name, sku, quantity, unit_price_minor, extended_value_minor,
	     actor_id, approver_id, note, order_type, register_id, created_at)
	   VALUES ('se1','void','i1','Widget','W-1',2,100,200,'u1','m1','mis-ring','takeaway','r1','2026-01-01T00:00:00Z'),
	          ('se2','waste',NULL,'Deleted thing',NULL,0.5,300,150,NULL,NULL,NULL,'',NULL,'2026-01-01T00:00:01Z')`)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	_, batchID, err := repo.ResetTransactionHistory(ctx, "", "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if c := count("shrinkage_events"); c != 0 {
		t.Fatalf("shrinkage_events not cleared by reset: %d row(s)", c)
	}
	var archived int
	if err := d.DB.QueryRow(`SELECT count(*) FROM shrinkage_events_archive WHERE reset_batch_id = ?`, batchID).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if archived != 2 {
		t.Fatalf("shrinkage_events_archive holds %d row(s) for the batch, want 2", archived)
	}

	if _, err := repo.RestoreResetBatch(ctx, batchID, "", ""); err != nil {
		t.Fatalf("restore: %v", err)
	}
	const q = `SELECT id || '|' || reason_category || '|' || COALESCE(item_id,'-') || '|' || item_name || '|' || COALESCE(sku,'-') || '|' ||
	  quantity || '|' || unit_price_minor || '|' || extended_value_minor || '|' || COALESCE(actor_id,'-') || '|' ||
	  COALESCE(approver_id,'-') || '|' || COALESCE(note,'-') || '|' || order_type || '|' || COALESCE(register_id,'-') || '|' || created_at
	  FROM shrinkage_events ORDER BY id`
	rows, err := d.DB.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"se1|void|i1|Widget|W-1|2.0|100|200|u1|m1|mis-ring|takeaway|r1|2026-01-01T00:00:00Z",
		"se2|waste|-|Deleted thing|-|0.5|300|150|-|-|-||-|2026-01-01T00:00:01Z",
	}
	if len(got) != len(want) {
		t.Fatalf("restored shrinkage_events = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("restored row %d = %q, want %q", i, got[i], want[i])
		}
	}
	if c := count("shrinkage_events_archive"); c != 0 {
		t.Fatalf("shrinkage_events_archive should be empty after restore, got %d", c)
	}
}

// A void/comp/waste needs no sale, so one recorded after a reset sits alone
// in the live table. Restoring on top would merge the batch into it, which
// ADR-0042 §2 forbids — the same reason worker_allocations is checked.
func TestRestoreRefusedWhenShrinkageRecordedSinceReset(t *testing.T) {
	d, x, _ := resetTestDB(t, "restore_shrinkage_since.db")
	seedFullSale(t, x)
	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	_, batchID, err := repo.ResetTransactionHistory(ctx, "", "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	x(`INSERT INTO shrinkage_events (id, reason_category, item_id, item_name, quantity, unit_price_minor, extended_value_minor, created_at)
	   VALUES ('se-after','comp','i1','Widget',1,100,100,'2026-02-01T00:00:00Z')`)
	if _, err := repo.RestoreResetBatch(ctx, batchID, "", ""); !errors.Is(err, data.ErrShopHasTradedSinceReset) {
		t.Fatalf("restore with a post-reset shrinkage event = %v, want ErrShopHasTradedSinceReset", err)
	}
}

// After a reset the item's only reference is in shrinkage_events_archive.
// Deleting it then would make the batch unrestorable
// (ErrArchiveReferencesRemoved), so cleanup and single-item delete keep it
// — the same rule sale_lines_archive already follows. Once the batch is
// purged (Delete permanently), the item is free to go: the training void
// no longer pins it for good.
func TestCleanup_ArchivedShrinkageKeepsItemUntilBatchPurged(t *testing.T) {
	for _, includeActive := range []bool{false, true} {
		d, x, _ := resetTestDB(t, "cleanup-shrinkage-archive.db")
		x(`INSERT INTO items (id, name, base_price, is_active) VALUES ('voided','Training Item',300,0)`)
		x(`INSERT INTO shrinkage_events (id, reason_category, item_id, item_name, quantity, unit_price_minor, extended_value_minor, created_at)
		   VALUES ('se1','void','voided','Training Item',1,300,300,'2026-01-01T00:00:00Z')`)

		repo := data.NewPOSRepo(d.DB)
		ctx := context.Background()
		_, batchID, err := repo.ResetTransactionHistory(ctx, "", "")
		if err != nil {
			t.Fatalf("includeActive=%v reset: %v", includeActive, err)
		}
		has := func() bool {
			var c int
			_ = d.DB.QueryRow(`SELECT count(*) FROM items WHERE id = 'voided'`).Scan(&c)
			return c == 1
		}

		if _, err := repo.CleanupObsoleteItems(ctx, "", "", includeActive); err != nil {
			t.Fatalf("includeActive=%v cleanup: %v", includeActive, err)
		}
		if !has() {
			t.Fatalf("includeActive=%v: item referenced only by an archived shrinkage event was removed", includeActive)
		}
		var inUse *data.ItemInUseError
		if _, err := repo.DeleteUnusedItem(ctx, "voided"); !errors.As(err, &inUse) || inUse.Reason != data.ItemUseStockHistory {
			t.Fatalf("includeActive=%v DeleteUnusedItem = %v, want ItemInUseError{%s}", includeActive, err, data.ItemUseStockHistory)
		}

		// sales_count = 0: purgeable straight away (no retention window).
		if err := repo.DeleteResetBatch(ctx, batchID, "", ""); err != nil {
			t.Fatalf("includeActive=%v purge batch: %v", includeActive, err)
		}
		if _, err := repo.CleanupObsoleteItems(ctx, "", "", includeActive); err != nil {
			t.Fatalf("includeActive=%v cleanup after purge: %v", includeActive, err)
		}
		if has() {
			t.Fatalf("includeActive=%v: item still kept after its archived shrinkage batch was purged", includeActive)
		}
	}
}

// The restore path's FK backstop (review nit, ut-docs#3452): if the item an
// archived shrinkage event names is deleted behind the predicates' back,
// restore refuses cleanly with ErrArchiveReferencesRemoved and leaves the
// archive intact, rather than surfacing a raw FOREIGN KEY failure.
func TestRestoreRefusesWhenArchivedShrinkageItemRemoved(t *testing.T) {
	d, x, count := resetTestDB(t, "restore_shrinkage_item_removed.db")
	x(`INSERT INTO items (id, name, base_price) VALUES ('gone','Gone Item',100)`)
	x(`INSERT INTO shrinkage_events (id, reason_category, item_id, item_name, quantity, unit_price_minor, extended_value_minor, created_at)
	   VALUES ('se1','waste','gone','Gone Item',1,100,100,'2026-01-01T00:00:00Z')`)
	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	_, batchID, err := repo.ResetTransactionHistory(ctx, "", "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	x(`DELETE FROM items WHERE id = 'gone'`)
	if _, err := repo.RestoreResetBatch(ctx, batchID, "", ""); !errors.Is(err, data.ErrArchiveReferencesRemoved) {
		t.Fatalf("restore = %v, want ErrArchiveReferencesRemoved", err)
	}
	if c := count("shrinkage_events"); c != 0 {
		t.Fatalf("refused restore left %d live shrinkage row(s)", c)
	}
	if c := count("shrinkage_events_archive"); c != 1 {
		t.Fatalf("archive must survive a refused restore, got %d row(s)", c)
	}
}
