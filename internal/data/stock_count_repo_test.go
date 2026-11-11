package data

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// Till-side stock-take (ut-docs#5852): ApplyStockCounts and
// ListStockTakeRows. Items are invented.

func countQty(t *testing.T, d *db.DB, itemID, variantID, loc string) float64 {
	t.Helper()
	var q float64
	var err error
	if variantID != "" {
		err = d.QueryRow(`SELECT quantity FROM inventory WHERE variant_id = ? AND item_id IS NULL AND location_id = ?`, variantID, loc).Scan(&q)
	} else {
		err = d.QueryRow(`SELECT quantity FROM inventory WHERE item_id = ? AND variant_id IS NULL AND location_id = ?`, itemID, loc).Scan(&q)
	}
	if err != nil {
		t.Fatalf("read qty: %v", err)
	}
	return q
}

func countMovements(t *testing.T, d *db.DB, typ string) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM stock_movements WHERE type = ?`, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestApplyStockCounts_AllOrNothing(t *testing.T) {
	d, repo := openB8InvDB(t)
	seedB8Item(t, d, "sc-a", "sku-a", "Apples", 0, 1)
	seedB8Inventory(t, d, "sc-a", "loc_main", 10)
	seedB8Item(t, d, "sc-b", "sku-b", "Pears", 0, 1)
	seedB8Inventory(t, d, "sc-b", "loc_main", 5)
	at := time.Now().Add(-time.Hour)
	_, err := repo.ApplyStockCounts(context.Background(), []StockCountInput{
		{ItemID: "sc-a", LocationID: "loc_main", Target: 7, CountedAt: at},
		{ItemID: "sc-b", LocationID: "loc_main", Target: -1, CountedAt: at},
	})
	if err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("err = %v, want a line 1 error", err)
	}
	if q := countQty(t, d, "sc-a", "", "loc_main"); q != 10 {
		t.Fatalf("qty a = %v, want unchanged 10", q)
	}
	if n := countMovements(t, d, StockCountMovementType); n != 0 {
		t.Fatalf("%d count movements written, want 0", n)
	}
}

func TestApplyStockCounts_KeepsSalesAfterStart(t *testing.T) {
	d, repo := openB8InvDB(t)
	seedB8Item(t, d, "sc-a", "sku-a", "Apples", 0, 1)
	seedB8Inventory(t, d, "sc-a", "loc_main", 20)
	start := time.Now().Add(-time.Hour).UTC()
	// A sale of 3 after Start: stock is now 17 though the shelf held 20.
	mustExec(t, d, `INSERT INTO stock_movements(id,item_id,variant_id,location_id,type,quantity,created_at) VALUES('mv-sale',?,NULL,'loc_main','sale',-3,?)`,
		"sc-a", start.Add(10*time.Minute).Format(time.RFC3339))
	mustExec(t, d, `UPDATE inventory SET quantity = 17 WHERE item_id = 'sc-a'`)
	res, err := repo.ApplyStockCounts(context.Background(), []StockCountInput{
		{ItemID: "sc-a", LocationID: "loc_main", Target: 18, CountedAt: start},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := countQty(t, d, "sc-a", "", "loc_main"); got != 15 {
		t.Fatalf("qty = %v, want counted 18 minus the later sale of 3 = 15", got)
	}
	if len(res) != 1 || res[0].Delta != -2 {
		t.Fatalf("results = %+v, want one delta of -2", res)
	}
}

func TestApplyStockCounts_RepeatIsNoop(t *testing.T) {
	d, repo := openB8InvDB(t)
	seedB8Item(t, d, "sc-a", "sku-a", "Apples", 0, 1)
	seedB8Inventory(t, d, "sc-a", "loc_main", 10)
	in := []StockCountInput{{ItemID: "sc-a", LocationID: "loc_main", Target: 4, CountedAt: time.Now().Add(-time.Hour)}}
	if _, err := repo.ApplyStockCounts(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	res, err := repo.ApplyStockCounts(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Delta != 0 || countMovements(t, d, StockCountMovementType) != 1 {
		t.Fatalf("repeat wrote a second movement: %+v", res)
	}
	if q := countQty(t, d, "sc-a", "", "loc_main"); q != 4 {
		t.Fatalf("qty = %v, want 4", q)
	}
}

func TestApplyStockCounts_ZeroDeltaWritesNothing(t *testing.T) {
	d, repo := openB8InvDB(t)
	seedB8Item(t, d, "sc-a", "sku-a", "Apples", 0, 1)
	seedB8Inventory(t, d, "sc-a", "loc_main", 10)
	res, err := repo.ApplyStockCounts(context.Background(), []StockCountInput{
		{ItemID: "sc-a", LocationID: "loc_main", Target: 10, CountedAt: time.Now().Add(-time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Delta != 0 || countMovements(t, d, StockCountMovementType) != 0 {
		t.Fatalf("zero-delta line wrote a movement: %+v", res)
	}
}

func TestApplyStockCounts_ActorAndReason(t *testing.T) {
	d, repo := openB8InvDB(t)
	seedB8Item(t, d, "sc-a", "sku-a", "Apples", 0, 1)
	seedB8Inventory(t, d, "sc-a", "loc_main", 10)
	if _, err := repo.ApplyStockCounts(context.Background(), []StockCountInput{
		{ItemID: "sc-a", LocationID: "loc_main", Target: 6, CountedAt: time.Now().Add(-time.Minute), Reason: "year end", ActorID: "system"},
	}); err != nil {
		t.Fatal(err)
	}
	var actor, payload string
	if err := d.QueryRow(`SELECT COALESCE(actor_id,''), data_json FROM audit_log WHERE action = 'count'`).Scan(&actor, &payload); err != nil {
		t.Fatal(err)
	}
	if actor != "system" || !strings.Contains(payload, `"reason":"year end"`) || !strings.Contains(payload, `"quantity":-4`) {
		t.Fatalf("actor=%q payload=%s", actor, payload)
	}
}

func TestApplyStockCounts_CreatesInventoryRowForUnstocked(t *testing.T) {
	d, repo := openB8InvDB(t)
	seedB8Item(t, d, "sc-a", "sku-a", "Apples", 0, 1)
	if _, err := repo.ApplyStockCounts(context.Background(), []StockCountInput{
		{ItemID: "sc-a", LocationID: "loc_main", Target: 9, CountedAt: time.Now().Add(-time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if q := countQty(t, d, "sc-a", "", "loc_main"); q != 9 {
		t.Fatalf("qty = %v, want 9", q)
	}
}

func TestListStockTakeRows(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()
	// Unstocked at the location -> qty 0.
	seedB8Item(t, d, "r-plain", "sku-plain", "Apples", 0, 1)
	// Stocked only at another location: not used for loc_main.
	mustExec(t, d, `INSERT INTO stock_locations(id,name) VALUES('loc_cellar','Cellar')`)
	seedB8Item(t, d, "r-other", "sku-other", "Bananas", 0, 1)
	seedB8Inventory(t, d, "r-other", "loc_cellar", 40)
	// Untracked, inactive: excluded.
	seedB8Item(t, d, "r-untracked", "sku-u", "Untracked", 0, 1)
	mustExec(t, d, `UPDATE items SET stock_untracked = 1 WHERE id = 'r-untracked'`)
	seedB8Item(t, d, "r-inactive", "sku-i", "Inactive", 0, 0)
	// Variant item: variant rows only, inactive variant excluded.
	seedB8Variant(t, d, "r-var", "r-var-v1", "loc_main", 12)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, name, price, is_active) VALUES ('r-var-v2','r-var','500ml',200,0)`)

	rows, err := repo.ListStockTakeRows(ctx, "loc_main")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]StockTakeRow{}
	for _, r := range rows {
		got[r.ItemID+"|"+r.VariantID] = r
	}
	if r, ok := got["r-plain|"]; !ok || r.Quantity != 0 {
		t.Fatalf("plain row = %+v ok=%v, want qty 0", r, ok)
	}
	if r, ok := got["r-other|"]; !ok || r.Quantity != 0 {
		t.Fatalf("other-location item row = %+v ok=%v, want qty 0 at loc_main", r, ok)
	}
	if r, ok := got["r-var|r-var-v1"]; !ok || r.Quantity != 12 || r.VariantName == "" {
		t.Fatalf("variant row = %+v ok=%v", r, ok)
	}
	for _, k := range []string{"r-untracked|", "r-inactive|", "r-var|", "r-var|r-var-v2"} {
		if _, ok := got[k]; ok {
			t.Fatalf("row %q must not be listed", k)
		}
	}
	back, err := repo.ListStockTakeRows(ctx, "loc_cellar")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range back {
		if r.ItemID == "r-other" && r.Quantity != 40 {
			t.Fatalf("cellar qty = %v, want 40", r.Quantity)
		}
	}
}
