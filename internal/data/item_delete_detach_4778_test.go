package data_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#4778 (owner rule 2026-10-22): an item that was never sold must be
// deletable. Live voids, comps, wastes and ID checks are detached from it
// (item_name kept, so every report keeps its totals); only an archived reset
// that could still be restored, or real sales/stock history, refuses.

func seedAgeCheck(x func(string, ...any), id, itemID string) {
	x(`INSERT INTO age_verifications (id, item_id, item_name, outcome, created_at)
	   VALUES (?, ?, 'Item', 'refused', '2026-10-21T21:42:44Z')`, id, itemID)
}

func TestDeleteUnusedItem_DetachesEachKind_4778(t *testing.T) {
	cases := []struct {
		name string
		seed func(x func(string, ...any))
		want data.ItemDeleteResult
		note string
	}{
		{"comp", func(x func(string, ...any)) { seedShrinkage(x, "se-1", "comp", "k-1") }, data.ItemDeleteResult{Comps: 1}, "item deleted with 1 comp"},
		{"waste", func(x func(string, ...any)) { seedShrinkage(x, "se-1", "waste", "k-1") }, data.ItemDeleteResult{Wastes: 1}, "item deleted with 1 waste"},
		{"id check", func(x func(string, ...any)) { seedAgeCheck(x, "av-1", "k-1") }, data.ItemDeleteResult{IDChecks: 1}, "item deleted with 1 ID check"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, x, _ := resetTestDB(t, "delete-item-detach-kind.db")
			seedDeletableItem(x, "k-1")
			c.seed(x)
			res, err := data.NewPOSRepo(d.DB).DeleteUnusedItem(context.Background(), "k-1")
			if err != nil {
				t.Fatalf("delete: %v", err)
			}
			if res.Voids != c.want.Voids || res.Comps != c.want.Comps || res.Wastes != c.want.Wastes || res.IDChecks != c.want.IDChecks {
				t.Fatalf("counts = %+v, want %+v", res, c.want)
			}
			if got := res.DetachedNote(); got != c.note {
				t.Fatalf("note = %q, want %q", got, c.note)
			}
			if itemExists(t, d.DB, "k-1") {
				t.Fatal("item still exists")
			}
			var n int
			if err := d.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM shrinkage_events WHERE item_id IS NULL AND item_name = 'Item')
			  + (SELECT COUNT(*) FROM age_verifications WHERE item_id IS NULL AND item_name = 'Item')`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("detached rows kept with their name = %d (%v), want 1", n, err)
			}
		})
	}
}

func TestDeleteUnusedItem_MixedDetachNote_4778(t *testing.T) {
	d, x, _ := resetTestDB(t, "delete-item-detach-mixed.db")
	seedDeletableItem(x, "m-1")
	seedShrinkage(x, "se-1", "void", "m-1")
	seedShrinkage(x, "se-2", "void", "m-1")
	seedShrinkage(x, "se-3", "comp", "m-1")
	seedShrinkage(x, "se-4", "waste", "m-1")
	seedAgeCheck(x, "av-1", "m-1")
	res, err := data.NewPOSRepo(d.DB).DeleteUnusedItem(context.Background(), "m-1")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.Voids != 2 || res.Comps != 1 || res.Wastes != 1 || res.IDChecks != 1 {
		t.Fatalf("counts = %+v", res)
	}
	if got, want := res.DetachedNote(), "item deleted with 2 voids, 1 comp, 1 waste, 1 ID check"; got != want {
		t.Fatalf("note = %q, want %q", got, want)
	}
	audit := map[string]any{"name": res.Name}
	res.AddDetachedToAudit(audit)
	want := map[string]any{"name": "Item m-1", "voids": int64(2), "comps": int64(1), "wastes": int64(1), "id_checks": int64(1),
		"note": "item deleted with 2 voids, 1 comp, 1 waste, 1 ID check"}
	if !reflect.DeepEqual(audit, want) {
		t.Fatalf("audit = %v, want %v", audit, want)
	}
}

func TestItemDeleteResult_DetachedNoteForms_4778(t *testing.T) {
	for _, c := range []struct {
		r    data.ItemDeleteResult
		want string
	}{
		{data.ItemDeleteResult{}, ""},
		{data.ItemDeleteResult{Voids: 1}, "item deleted with 1 void"},
		{data.ItemDeleteResult{Voids: 3}, "item deleted with 3 voids"},
		{data.ItemDeleteResult{Comps: 2}, "item deleted with 2 comps"},
		{data.ItemDeleteResult{Wastes: 2}, "item deleted with 2 wastes"},
		{data.ItemDeleteResult{IDChecks: 2}, "item deleted with 2 ID checks"},
		{data.ItemDeleteResult{Voids: 2, Wastes: 1, IDChecks: 1}, "item deleted with 2 voids, 1 waste, 1 ID check"},
	} {
		if got := c.r.DetachedNote(); got != c.want {
			t.Errorf("%+v: note = %q, want %q", c.r, got, c.want)
		}
	}
	audit := map[string]any{}
	data.ItemDeleteResult{}.AddDetachedToAudit(audit)
	if len(audit) != 0 {
		t.Fatalf("empty result added %v to the audit", audit)
	}
}

// The Shrinkage report is grouped by reason_category and item_name, neither
// of which the detach touches: its totals must not move.
func TestDeleteUnusedItem_ShrinkageReportTotalsUnchanged_4778(t *testing.T) {
	d, x, _ := resetTestDB(t, "delete-item-detach-report.db")
	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	seedDeletableItem(x, "r-1")
	seedShrinkage(x, "se-1", "void", "r-1")
	seedShrinkage(x, "se-2", "comp", "r-1")
	seedShrinkage(x, "se-3", "waste", "r-1")
	seedShrinkage(x, "se-4", "waste", "r-1")
	from, to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	byReason, err := repo.ShrinkageByReason(ctx, from, to)
	if err != nil || len(byReason) != 3 {
		t.Fatalf("before: %v %v", byReason, err)
	}
	top, err := repo.ShrinkageTopItems(ctx, from, to, 10)
	if err != nil || len(top) != 1 {
		t.Fatalf("top before: %v %v", top, err)
	}
	if _, err := repo.DeleteUnusedItem(ctx, "r-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	byReason2, err := repo.ShrinkageByReason(ctx, from, to)
	if err != nil || !reflect.DeepEqual(byReason, byReason2) {
		t.Fatalf("by reason after = %v (%v), want %v", byReason2, err, byReason)
	}
	top2, err := repo.ShrinkageTopItems(ctx, from, to, 10)
	if err != nil || !reflect.DeepEqual(top, top2) {
		t.Fatalf("top items after = %v (%v), want %v", top2, err, top)
	}
}

func TestDeleteUnusedItemTx_ArchivedAgeCheckRefusesAndChangesNothing_4778(t *testing.T) {
	d, x, _ := resetTestDB(t, "delete-item-archived-age.db")
	seedDeletableItem(x, "age-1")
	seedShrinkage(x, "se-v", "void", "age-1")
	seedShrinkage(x, "se-c", "comp", "age-1")
	seedAgeCheck(x, "av-live", "age-1")
	x(`INSERT INTO reset_batches (id, created_at) VALUES ('rb-age', datetime('now'))`)
	x(`INSERT INTO age_verifications_archive (id, item_id, item_name, outcome, created_at, reset_batch_id)
	   VALUES ('av-1', 'age-1', 'Item', 'refused', '2026-10-21T21:42:44Z', 'rb-age')`)
	ctx := context.Background()
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = data.NewPOSRepo(d.DB).DeleteUnusedItemTx(ctx, tx, "age-1")
	if err := tx.Commit(); err != nil { // the caller deactivates and commits
		t.Fatal(err)
	}
	var inUse *data.ItemInUseError
	const msg = "its ID checks sit in an archived reset that can still be restored — deactivate it instead"
	if !errors.As(err, &inUse) || inUse.Reason != data.ItemUseArchivedAgeCheck || err.Error() != msg {
		t.Fatalf("err = %v, want %s %q", err, data.ItemUseArchivedAgeCheck, msg)
	}
	var shr, av int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM shrinkage_events WHERE item_id = 'age-1'`).Scan(&shr)
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM age_verifications WHERE item_id = 'age-1'`).Scan(&av)
	if shr != 2 || av != 1 || !itemExists(t, d.DB, "age-1") {
		t.Fatalf("attached shrinkage = %d, live ID checks = %d, item kept = %v; a refusal must change nothing", shr, av, itemExists(t, d.DB, "age-1"))
	}
}

func TestDeleteUnusedItem_ArchivedCompOrWasteStillRefuses_4778(t *testing.T) {
	for _, reason := range []string{"comp", "waste"} {
		d, x, _ := resetTestDB(t, "delete-item-archived-shr.db")
		seedDeletableItem(x, "as-1")
		seedShrinkage(x, "se-live", "comp", "as-1")
		x(`INSERT INTO reset_batches (id, created_at) VALUES ('rb-as', datetime('now'))`)
		x(`INSERT INTO shrinkage_events_archive (id, reason_category, item_id, item_name, quantity, unit_price_minor, extended_value_minor, created_at, reset_batch_id)
		   VALUES ('sea-1', ?, 'as-1', 'Item', 1, 1000, 1000, '2026-10-21T21:42:44Z', 'rb-as')`, reason)
		_, err := data.NewPOSRepo(d.DB).DeleteUnusedItem(context.Background(), "as-1")
		var inUse *data.ItemInUseError
		if !errors.As(err, &inUse) || inUse.Reason != data.ItemUseArchivedShrinkage {
			t.Fatalf("%s: err = %v, want archived_shrinkage", reason, err)
		}
		var n int
		_ = d.DB.QueryRow(`SELECT COUNT(*) FROM shrinkage_events WHERE item_id = 'as-1'`).Scan(&n)
		if n != 1 || !itemExists(t, d.DB, "as-1") {
			t.Fatalf("%s: a refusal changed something (attached=%d)", reason, n)
		}
	}
}

func TestDeleteUnusedItem_SoldItemWithCompStillRefusesAsSold_4778(t *testing.T) {
	d, x, _ := resetTestDB(t, "delete-item-sold-comp.db")
	seedDeletableItem(x, "sc-1")
	seedShrinkage(x, "se-sc", "comp", "sc-1")
	seedAgeCheck(x, "av-sc", "sc-1")
	x(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('s-sc', 'R-sc', 'completed', 'sale', 'GBP', 250, 0, 0, 250, datetime('now'))`)
	x(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('l-sc', 's-sc', 1, 'sc-1', 'Item', 1, 250, 0, 0, 0, 250, 250)`)
	_, err := data.NewPOSRepo(d.DB).DeleteUnusedItem(context.Background(), "sc-1")
	var inUse *data.ItemInUseError
	if !errors.As(err, &inUse) || inUse.Reason != data.ItemUseSold {
		t.Fatalf("err = %v, want sold", err)
	}
	var n int
	_ = d.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM shrinkage_events WHERE item_id = 'sc-1') + (SELECT COUNT(*) FROM age_verifications WHERE item_id = 'sc-1')`).Scan(&n)
	if n != 2 {
		t.Fatalf("attached rows = %d, want 2 (nothing detached)", n)
	}
}
