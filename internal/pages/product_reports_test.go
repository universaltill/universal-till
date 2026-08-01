package pages

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// Slow-sellers and dead-stock queries: an item that sold once is a slow
// seller (not dead); an item with stock and no sales is dead stock (value =
// qty × base_price); a sold-out never-seller doesn't appear in dead stock.
func TestSlowItemsAndDeadStock(t *testing.T) {
	f := filepath.Join(t.TempDir(), "rep.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	d := database.DB

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v (%s)", err, q)
		}
	}
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active) VALUES ('it-a','Seller','A',100,1)`)
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active) VALUES ('it-b','Shelf Warmer','B',250,1)`)
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active) VALUES ('it-c','Sold Out','C',300,1)`)
	mustExec(`INSERT INTO stock_locations (id, name) VALUES ('loc-1','Floor')`)
	mustExec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-a','it-a','loc-1',5)`)
	mustExec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-b','it-b','loc-1',4)`)
	mustExec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-c','it-c','loc-1',0)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
	          VALUES ('s1','R1','completed','sale',100,0,100, datetime('now','-2 days'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
	          VALUES ('l1','s1',1,'it-a','Seller',1,100,0,0,0,100,100)`)

	repo := data.NewPOSRepo(d)
	ctx := context.Background()

	// db.Open seeds the demo catalog, so assert on OUR rows, not exact lists.
	slow, err := repo.SlowItems(ctx, 30, 100)
	if err != nil {
		t.Fatalf("slow: %v", err)
	}
	foundSeller := false
	for _, s := range slow {
		if s.Name == "Seller" {
			foundSeller = true
		}
		if s.Name == "Shelf Warmer" || s.Name == "Sold Out" {
			t.Fatalf("never-sold item %q must not be a slow SELLER", s.Name)
		}
	}
	if !foundSeller {
		t.Fatalf("Seller missing from slow list: %+v", slow)
	}

	dead, err := repo.DeadStock(ctx, 30, 1000)
	if err != nil {
		t.Fatalf("dead: %v", err)
	}
	var warmer *data.DeadStockRow
	for i := range dead {
		switch dead[i].Name {
		case "Shelf Warmer":
			warmer = &dead[i]
		case "Seller":
			t.Fatal("an item that sold must not be dead stock")
		case "Sold Out":
			t.Fatal("zero-stock items must not be dead stock")
		}
	}
	if warmer == nil {
		t.Fatalf("Shelf Warmer missing from dead stock")
	}
	if warmer.StockValue != 1000 { // 4 × 250
		t.Fatalf("dead value = %d, want 1000", warmer.StockValue)
	}

	// Variant sales count toward the PARENT item: sell it-b via a variant →
	// it-b stops being dead stock and gains a sell rate.
	mustExec(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-b','it-b','B-V','Var','300',1)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
	          VALUES ('s2','R2','completed','sale',300,0,300, datetime('now','-1 days'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, variant_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
	          VALUES ('l2','s2',1,'v-b','Shelf Warmer Var',2,300,0,0,0,600,600)`)
	dead2, err := repo.DeadStock(ctx, 30, 1000)
	if err != nil {
		t.Fatalf("dead2: %v", err)
	}
	for _, r := range dead2 {
		if r.Name == "Shelf Warmer" {
			t.Fatal("item sold via a variant must not be dead stock")
		}
	}
	rates, err := repo.ItemDailySellRates(ctx, 30)
	if err != nil {
		t.Fatalf("rates: %v", err)
	}
	if rates["it-b"] <= 0 {
		t.Fatalf("variant sale must give the parent item a sell rate, got %v", rates["it-b"])
	}

	// Busy-times buckets: our one sale lands in exactly one weekday bucket
	// and one hour bucket, and totals carry through.
	wd, err := repo.SalesByWeekday(ctx, 30)
	if err != nil {
		t.Fatalf("weekday: %v", err)
	}
	hr, err := repo.SalesByHour(ctx, 30)
	if err != nil {
		t.Fatalf("hour: %v", err)
	}
	sumWd, sumHr := 0, 0
	for _, b := range wd {
		if b.Slot < 0 || b.Slot > 6 {
			t.Fatalf("weekday slot out of range: %d", b.Slot)
		}
		sumWd += b.Count
	}
	for _, b := range hr {
		if b.Slot < 0 || b.Slot > 23 {
			t.Fatalf("hour slot out of range: %d", b.Slot)
		}
		sumHr += b.Count
	}
	if sumWd != 2 || sumHr != 2 {
		t.Fatalf("bucket sums = %d/%d, want 2/2", sumWd, sumHr)
	}

	// Tax summary: both sales carry 0bp tax; net = 100+600, tax = 0, one band.
	bands, err := repo.TaxSummary(ctx, 30)
	if err != nil {
		t.Fatalf("tax: %v", err)
	}
	var zero *data.TaxBand
	for i := range bands {
		if bands[i].RateBP == 0 {
			zero = &bands[i]
		}
	}
	if zero == nil || zero.Net != 700 || zero.Tax != 0 {
		t.Fatalf("tax bands = %+v, want 0bp net 700 tax 0", bands)
	}

	// Margins: give the sold item a cost price → margin = revenue − qty×cost.
	if err := data.NewCatalogRepo(d).SetItemCostPrice(ctx, "it-a", 40); err != nil {
		t.Fatalf("set cost: %v", err)
	}
	if got, err := data.NewCatalogRepo(d).ItemCostPrice(ctx, "it-a"); err != nil || got != 40 {
		t.Fatalf("cost roundtrip = %d %v", got, err)
	}
	margins, err := repo.MarginByItem(ctx, 30, 100)
	if err != nil {
		t.Fatalf("margins: %v", err)
	}
	var sellerMargin *data.MarginRow
	for i := range margins {
		if margins[i].Name == "Seller" {
			sellerMargin = &margins[i]
		}
	}
	if sellerMargin == nil {
		t.Fatalf("Seller missing from margins: %+v", margins)
	}
	if sellerMargin.Revenue != 100 || sellerMargin.Cost != 40 || sellerMargin.Margin != 60 {
		t.Fatalf("margin = %+v, want 100/40/60", *sellerMargin)
	}

	// Variant label: composed name, the VARIANT's price, its barcode
	// (falls back to SKU when no barcode is attached).
	crepo := data.NewCatalogRepo(d)
	vl, ok, err := crepo.GetVariantLabel(ctx, "v-b")
	if err != nil || !ok {
		t.Fatalf("variant label: %v %v", ok, err)
	}
	if vl.Name != "Shelf Warmer Var" || vl.PriceMinor != 300 || vl.Code != "B-V" {
		t.Fatalf("variant label = %+v", vl)
	}
	mustExec(`INSERT INTO variant_barcodes (barcode, variant_id, is_primary) VALUES ('5099999999999','v-b',1)`)
	vl, _, _ = crepo.GetVariantLabel(ctx, "v-b")
	if vl.Code != "5099999999999" {
		t.Fatalf("variant label barcode = %q, want the primary barcode", vl.Code)
	}

	// Seasonal order-ahead: nothing without year-old data…
	if season, _, err := repo.SeasonalForecast(ctx, 28, 10); err != nil || len(season) != 0 {
		t.Fatalf("seasonal on fresh shop = %d rows (%v), want 0", len(season), err)
	}
	// …then a sale 358 days ago (inside next-28-days-last-year) drives a
	// suggestion: 20 sold then, 5 on hand → order 15.
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
	          VALUES ('sy','RY','completed','sale',2000,0,2000, datetime('now','-358 days'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
	          VALUES ('ly','sy',1,'it-a','Seller',20,100,0,0,0,2000,2000)`)
	mustExec(`UPDATE inventory SET quantity = 5 WHERE id = 'inv-a'`)
	season, _, err := repo.SeasonalForecast(ctx, 28, 10)
	if err != nil || len(season) != 1 {
		t.Fatalf("seasonal = %d rows (%v), want 1", len(season), err)
	}
	if season[0].Name != "Seller" || season[0].Expected != 20 || season[0].SuggestQty != 15 {
		t.Fatalf("seasonal row = %+v, want Seller 20 → order 15", season[0])
	}
}
