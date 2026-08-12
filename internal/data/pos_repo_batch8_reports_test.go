package data

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// Coverage batch 8: the owner-intelligence/report group in POSRepo —
// SalesByDay, SlowItems, DeadStock, TaxSummary, MarginByItem, DayTotal,
// SeasonalUpcoming, SalesByWeekday/SalesByHour (busyBuckets), and
// ItemDailySellRates. Real DB, real migrations, exact minor-unit assertions.

// b8OpenDB opens a fresh migrated DB in a temp dir.
func b8OpenDB(t *testing.T, name string) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// b8At renders a time in the RFC3339 format production writes to created_at.
func b8At(tm time.Time) string { return tm.UTC().Format("2006-01-02T15:04:05Z") }

// b8Sale inserts a sale row. subtotal mirrors total (discounts irrelevant here).
func b8Sale(t *testing.T, d *db.DB, id, createdAt, status, saleType string, taxTotal, total int64) {
	t.Helper()
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
VALUES (?, ?, ?, ?, 'GBP', ?, 0, ?, ?, ?)`, id, "R-"+id, status, saleType, total, taxTotal, total, createdAt)
}

// b8Line inserts a sale line. Exactly one of itemID/variantID must be
// non-empty (schema CHECK); the empty one persists as NULL.
func b8Line(t *testing.T, d *db.DB, saleID string, lineNo int, itemID, variantID, name string, qty float64, rateBP, taxAmt, before, after int64) {
	t.Helper()
	var iid, vid any
	if itemID != "" {
		iid = itemID
	}
	if variantID != "" {
		vid = variantID
	}
	mustExec(t, d, `INSERT INTO sale_lines (id, sale_id, line_no, item_id, variant_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		fmt.Sprintf("%s-l%d", saleID, lineNo), saleID, lineNo, iid, vid, name, qty, after, rateBP, taxAmt, before, after)
}

func b8Item(t *testing.T, d *db.DB, id string, basePrice int64, costPrice any, active int) {
	t.Helper()
	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price, cost_price, is_active) VALUES (?, ?, ?, ?, ?, ?)`,
		id, "SKU-"+id, "Name "+id, basePrice, costPrice, active)
}

func b8Stock(t *testing.T, d *db.DB, invID, itemID, locID string, qty float64) {
	t.Helper()
	mustExec(t, d, `INSERT INTO inventory (id, item_id, location_id, quantity) VALUES (?, ?, ?, ?)`,
		invID, itemID, locID, qty)
}

func TestPOSRepo_SalesByDay_AggregatesFiltersOrders(t *testing.T) {
	d := b8OpenDB(t, "salesbyday.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	tNew := time.Now().Add(-2 * time.Hour)
	tOld := time.Now().Add(-72 * time.Hour)
	dayNew := tNew.UTC().Format("2006-01-02") // query groups by date() of the stored UTC string
	dayOld := tOld.UTC().Format("2006-01-02")

	// Two completed sales on the recent day: totals 1000+250, tax 100+25.
	b8Sale(t, d, "s1", b8At(tNew), "completed", "sale", 100, 1000)
	b8Sale(t, d, "s2", b8At(tNew), "completed", "sale", 25, 250)
	// One on the older day.
	b8Sale(t, d, "s3", b8At(tOld), "completed", "sale", 70, 700)
	// Voided same day: must not count.
	b8Sale(t, d, "s4", b8At(tNew), "voided", "sale", 999, 9999)
	// Outside the 7-day window: must not appear.
	b8Sale(t, d, "s5", b8At(time.Now().Add(-9*24*time.Hour)), "completed", "sale", 0, 55555)

	rows, err := repo.SalesByDay(ctx, winFrom(7), winTo())
	if err != nil {
		t.Fatalf("SalesByDay: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 day buckets, got %d: %+v", len(rows), rows)
	}
	// Ordered day DESC: newest first.
	if rows[0].Day != dayNew || rows[1].Day != dayOld {
		t.Fatalf("wrong day order: got [%s, %s], want [%s, %s]", rows[0].Day, rows[1].Day, dayNew, dayOld)
	}
	if rows[0].Count != 2 || rows[0].Total != 1250 || rows[0].TaxTotal != 125 {
		t.Fatalf("new day = %+v, want count 2 total 1250 tax 125", rows[0])
	}
	if rows[1].Count != 1 || rows[1].Total != 700 || rows[1].TaxTotal != 70 {
		t.Fatalf("old day = %+v, want count 1 total 700 tax 70", rows[1])
	}
}

func TestPOSRepo_RefundsByWindow_SumsReturnsFiltersOthers(t *testing.T) {
	d := b8OpenDB(t, "refundsbywindow.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	now := b8At(time.Now().Add(-2 * time.Hour))
	old := b8At(time.Now().Add(-9 * 24 * time.Hour))

	// Two completed returns inside the 7-day window: count 2, total 300+150.
	b8Sale(t, d, "r1", now, "completed", "return", 0, 300)
	b8Sale(t, d, "r2", now, "completed", "return", 0, 150)
	// A completed sale (not a return): must not count.
	b8Sale(t, d, "s1", now, "completed", "sale", 0, 9999)
	// A voided return: excluded by status.
	b8Sale(t, d, "r3", now, "voided", "return", 0, 8888)
	// A completed return outside the 7-day window: must not count.
	b8Sale(t, d, "r4", old, "completed", "return", 0, 7777)

	total, count, err := repo.RefundsByWindow(ctx, winFrom(7), winTo())
	if err != nil {
		t.Fatalf("RefundsByWindow: %v", err)
	}
	if total != 450 || count != 2 {
		t.Fatalf("RefundsByWindow = (total=%d, count=%d), want (450, 2)", total, count)
	}
}

func TestPOSRepo_SlowItems_AscendingWithFiltersAndLimit(t *testing.T) {
	d := b8OpenDB(t, "slowitems.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "itm", 100, nil, 1) // shared FK target; grouping is by name_snapshot
	now := b8At(time.Now().Add(-2 * time.Hour))

	b8Sale(t, d, "s1", now, "completed", "sale", 0, 5400)
	b8Line(t, d, "s1", 1, "itm", "", "Widget", 1, 0, 0, 100, 100)
	b8Line(t, d, "s1", 2, "itm", "", "Gadget", 2, 0, 0, 5000, 5000)
	b8Line(t, d, "s1", 3, "itm", "", "Gizmo", 1, 0, 0, 300, 300)
	// Return-type completed sale: SlowItems is sale-only, must not appear.
	b8Sale(t, d, "s2", now, "completed", "return", 0, 40)
	b8Line(t, d, "s2", 1, "itm", "", "ReturnOnly", 1, 0, 0, 40, 40)
	// Voided: excluded by status.
	b8Sale(t, d, "s3", now, "voided", "sale", 0, 10)
	b8Line(t, d, "s3", 1, "itm", "", "VoidOnly", 1, 0, 0, 10, 10)
	// Outside the 30-day window.
	b8Sale(t, d, "s4", b8At(time.Now().Add(-40*24*time.Hour)), "completed", "sale", 0, 5)
	b8Line(t, d, "s4", 1, "itm", "", "OldOnly", 1, 0, 0, 5, 5)

	rows, err := repo.SlowItems(ctx, winFrom(30), winTo(), 2)
	if err != nil {
		t.Fatalf("SlowItems: %v", err)
	}
	// Ascending by revenue, LIMIT 2 drops the best seller (Gadget).
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (limit), got %d: %+v", len(rows), rows)
	}
	if rows[0].Name != "Widget" || rows[0].Qty != 1 || rows[0].Revenue != 100 {
		t.Fatalf("worst seller = %+v, want Widget qty 1 revenue 100", rows[0])
	}
	if rows[1].Name != "Gizmo" || rows[1].Qty != 1 || rows[1].Revenue != 300 {
		t.Fatalf("second slowest = %+v, want Gizmo qty 1 revenue 300", rows[1])
	}
}

func TestPOSRepo_DeadStock_ValueMathAndExclusions(t *testing.T) {
	d := b8OpenDB(t, "deadstock.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	// 001_init.sql seeds a demo catalog with stock; clear inventory so only
	// this test's holdings count (same convention as batch 7's settings test).
	mustExec(t, d, `DELETE FROM inventory`)
	mustExec(t, d, `INSERT INTO stock_locations (id, name) VALUES ('loc1', 'Shop'), ('loc2', 'Back room')`)

	// Dead across two locations: qty 3+2=5 × 500 = 2500.
	b8Item(t, d, "dead1", 500, nil, 1)
	b8Stock(t, d, "inv1", "dead1", "loc1", 3)
	b8Stock(t, d, "inv2", "dead1", "loc2", 2)
	// Dead, highest value: 1 × 10000.
	b8Item(t, d, "dead2", 10000, nil, 1)
	b8Stock(t, d, "inv3", "dead2", "loc1", 1)
	// Sold directly in the window: not dead.
	b8Item(t, d, "sold", 200, nil, 1)
	b8Stock(t, d, "inv4", "sold", "loc1", 4)
	// Sold via a VARIANT line in the window: must resolve to the parent item.
	b8Item(t, d, "variantsold", 300, nil, 1)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, name, price) VALUES ('v1', 'variantsold', 'Large', 350)`)
	b8Stock(t, d, "inv5", "variantsold", "loc1", 2)
	// Inactive item with stock: excluded.
	b8Item(t, d, "inactive", 400, nil, 0)
	b8Stock(t, d, "inv6", "inactive", "loc1", 5)
	// Active but zero stock: excluded.
	b8Item(t, d, "nostock", 600, nil, 1)
	b8Stock(t, d, "inv7", "nostock", "loc1", 0)
	// Only sale is OUTSIDE the window: still dead. 1 × 100.
	b8Item(t, d, "oldsale", 100, nil, 1)
	b8Stock(t, d, "inv8", "oldsale", "loc1", 1)
	// Only sale in-window is VOIDED: still dead. 1 × 700.
	b8Item(t, d, "voidsold", 700, nil, 1)
	b8Stock(t, d, "inv9", "voidsold", "loc1", 1)

	now := b8At(time.Now().Add(-2 * time.Hour))
	b8Sale(t, d, "s1", now, "completed", "sale", 0, 200)
	b8Line(t, d, "s1", 1, "sold", "", "Sold thing", 1, 0, 0, 200, 200)
	b8Sale(t, d, "s2", now, "completed", "sale", 0, 350)
	b8Line(t, d, "s2", 1, "", "v1", "Variant thing L", 1, 0, 0, 350, 350)
	b8Sale(t, d, "s3", b8At(time.Now().Add(-40*24*time.Hour)), "completed", "sale", 0, 100)
	b8Line(t, d, "s3", 1, "oldsale", "", "Old thing", 1, 0, 0, 100, 100)
	b8Sale(t, d, "s4", now, "voided", "sale", 0, 700)
	b8Line(t, d, "s4", 1, "voidsold", "", "Void thing", 1, 0, 0, 700, 700)

	rows, err := repo.DeadStock(ctx, winFrom(30), winTo(), 10)
	if err != nil {
		t.Fatalf("DeadStock: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("expected 4 dead items, got %d: %+v", len(rows), rows)
	}
	// Ordered by tied-up value DESC.
	wantOrder := []struct {
		name  string
		sku   string
		qty   float64
		value int64
	}{
		{"Name dead2", "SKU-dead2", 1, 10000},
		{"Name dead1", "SKU-dead1", 5, 2500},
		{"Name voidsold", "SKU-voidsold", 1, 700},
		{"Name oldsale", "SKU-oldsale", 1, 100},
	}
	for i, w := range wantOrder {
		got := rows[i]
		if got.Name != w.name || got.SKU != w.sku || got.Qty != w.qty || got.StockValue != w.value {
			t.Fatalf("row %d = %+v, want %+v", i, got, w)
		}
	}

	// LIMIT applies after the value ordering.
	top, err := repo.DeadStock(ctx, winFrom(30), winTo(), 1)
	if err != nil {
		t.Fatalf("DeadStock(limit 1): %v", err)
	}
	if len(top) != 1 || top[0].Name != "Name dead2" {
		t.Fatalf("limit 1 must return only the most tied-up item, got %+v", top)
	}
}

func TestPOSRepo_TaxSummary_BandsReturnsSubtract(t *testing.T) {
	d := b8OpenDB(t, "taxsummary.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "itm", 100, nil, 1)
	now := b8At(time.Now().Add(-2 * time.Hour))

	// Sale: 20% band net 1000 tax 200; zero band net 500 tax 0.
	b8Sale(t, d, "s1", now, "completed", "sale", 200, 1700)
	b8Line(t, d, "s1", 1, "itm", "", "Taxed", 1, 2000, 200, 1000, 1200)
	b8Line(t, d, "s1", 2, "itm", "", "ZeroRated", 1, 0, 0, 500, 500)
	// Return in the 20% band: subtracts net 300 tax 60.
	b8Sale(t, d, "s2", now, "completed", "return", 60, 360)
	b8Line(t, d, "s2", 1, "itm", "", "Taxed", 1, 2000, 60, 300, 360)
	// Voided: ignored entirely.
	b8Sale(t, d, "s3", now, "voided", "sale", 999, 999)
	b8Line(t, d, "s3", 1, "itm", "", "Voided", 1, 2000, 999, 999, 999)
	// Outside the 30-day window: ignored.
	b8Sale(t, d, "s4", b8At(time.Now().Add(-40*24*time.Hour)), "completed", "sale", 111, 666)
	b8Line(t, d, "s4", 1, "itm", "", "Ancient", 1, 2000, 111, 555, 666)

	bands, err := repo.TaxSummary(ctx, winFrom(30), winTo())
	if err != nil {
		t.Fatalf("TaxSummary: %v", err)
	}
	if len(bands) != 2 {
		t.Fatalf("expected 2 tax bands, got %d: %+v", len(bands), bands)
	}
	// Ordered by rate DESC: 20% band first, net 1000-300, tax 200-60.
	if bands[0].RateBP != 2000 || bands[0].Net != 700 || bands[0].Tax != 140 {
		t.Fatalf("20%% band = %+v, want rate 2000 net 700 tax 140", bands[0])
	}
	if bands[1].RateBP != 0 || bands[1].Net != 500 || bands[1].Tax != 0 {
		t.Fatalf("zero band = %+v, want rate 0 net 500 tax 0", bands[1])
	}
}

func TestPOSRepo_MarginByItem_CostSourcesOrderingLimit(t *testing.T) {
	d := b8OpenDB(t, "margins.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "coffee", 300, int64(100), 1) // item-level cost
	b8Item(t, d, "nocost", 300, nil, 1)        // no cost anywhere: excluded
	b8Item(t, d, "shirt", 900, int64(200), 1)
	// Variant with its OWN cost — must win over the parent item's.
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, name, price, cost_price) VALUES ('v-shirt', 'shirt', 'L', 1000, 250)`)

	now := b8At(time.Now().Add(-2 * time.Hour))
	b8Sale(t, d, "s1", now, "completed", "sale", 0, 1900)
	// Coffee: qty 3, revenue 900, cost 3×100=300, margin 600.
	b8Line(t, d, "s1", 1, "coffee", "", "Coffee", 3, 0, 0, 900, 900)
	// Shirt via variant: qty 2, revenue 1000, cost 2×250=500, margin 500.
	b8Line(t, d, "s1", 2, "", "v-shirt", "Shirt L", 2, 0, 0, 1000, 1000)
	// No cost price: excluded even though revenue is the highest.
	b8Line(t, d, "s1", 3, "nocost", "", "Mystery", 1, 0, 0, 99999, 99999)
	// Return-type completed sale of coffee: MarginByItem is sale-only, ignored.
	b8Sale(t, d, "s2", now, "completed", "return", 0, 300)
	b8Line(t, d, "s2", 1, "coffee", "", "Coffee", 1, 0, 0, 300, 300)
	// Outside the window.
	b8Sale(t, d, "s3", b8At(time.Now().Add(-40*24*time.Hour)), "completed", "sale", 0, 300)
	b8Line(t, d, "s3", 1, "coffee", "", "Coffee", 1, 0, 0, 300, 300)

	rows, err := repo.MarginByItem(ctx, winFrom(30), winTo(), 10)
	if err != nil {
		t.Fatalf("MarginByItem: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 margin rows, got %d: %+v", len(rows), rows)
	}
	// Ordered by margin DESC.
	if rows[0].Name != "Coffee" || rows[0].Revenue != 900 || rows[0].Cost != 300 || rows[0].Margin != 600 {
		t.Fatalf("coffee = %+v, want revenue 900 cost 300 margin 600", rows[0])
	}
	if rows[1].Name != "Shirt L" || rows[1].Revenue != 1000 || rows[1].Cost != 500 || rows[1].Margin != 500 {
		t.Fatalf("shirt = %+v, want revenue 1000 cost 500 (variant cost) margin 500", rows[1])
	}

	top, err := repo.MarginByItem(ctx, winFrom(30), winTo(), 1)
	if err != nil {
		t.Fatalf("MarginByItem(limit 1): %v", err)
	}
	if len(top) != 1 || top[0].Name != "Coffee" {
		t.Fatalf("limit 1 must keep the best margin only, got %+v", top)
	}
}

func TestPOSRepo_DayTotal_LocalCalendarDays(t *testing.T) {
	d := b8OpenDB(t, "daytotal.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	today := b8At(time.Now())
	yesterday := b8At(time.Now().AddDate(0, 0, -1))

	b8Sale(t, d, "t1", today, "completed", "sale", 0, 300)
	b8Sale(t, d, "t2", today, "completed", "sale", 0, 200)
	b8Sale(t, d, "y1", yesterday, "completed", "sale", 0, 700)
	// Yesterday's return and voided sale: both excluded.
	b8Sale(t, d, "y2", yesterday, "completed", "return", 0, 400)
	b8Sale(t, d, "y3", yesterday, "voided", "sale", 0, 999)

	total, count, err := repo.DayTotal(ctx, 0)
	if err != nil {
		t.Fatalf("DayTotal(0): %v", err)
	}
	if total != 500 || count != 2 {
		t.Fatalf("today = (%d, %d), want (500, 2)", total, count)
	}
	total, count, err = repo.DayTotal(ctx, 1)
	if err != nil {
		t.Fatalf("DayTotal(1): %v", err)
	}
	if total != 700 || count != 1 {
		t.Fatalf("yesterday = (%d, %d), want (700, 1)", total, count)
	}
	// A day with no sales reports zeros, not an error.
	total, count, err = repo.DayTotal(ctx, 5)
	if err != nil || total != 0 || count != 0 {
		t.Fatalf("empty day = (%d, %d, %v), want (0, 0, nil)", total, count, err)
	}
}

// Single-year parity: with exactly one year of history, solar-window items
// keep the old SeasonalUpcoming numbers (Expected == last year's units).
// One deliberate semantic change: sales in the lunar k=1 window [-354,-326)
// are now caught too ("tooRecent" below) — that's the ut-docs#84 lunar fix.
func TestPOSRepo_SeasonalForecast_YearAgoWindowAndSuggestions(t *testing.T) {
	d := b8OpenDB(t, "seasonal.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	mustExec(t, d, `INSERT INTO stock_locations (id, name) VALUES ('loc1', 'Shop')`)
	b8Item(t, d, "pumpkin", 250, nil, 1)
	b8Stock(t, d, "inv1", "pumpkin", "loc1", 2)
	b8Item(t, d, "covered", 250, nil, 1)
	b8Stock(t, d, "inv2", "covered", "loc1", 10)
	b8Item(t, d, "scarf", 800, nil, 1) // sold via variant, no stock on hand
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, name, price) VALUES ('v2', 'scarf', 'Red', 800)`)
	b8Item(t, d, "berry", 300, nil, 1) // fractional (weighed) quantities
	b8Stock(t, d, "inv3", "berry", "loc1", 1)
	b8Item(t, d, "middle", 100, nil, 1)  // pins the days<=0 default at 28, not less
	b8Item(t, d, "retired", 100, nil, 0) // inactive: excluded
	b8Item(t, d, "tooOld", 100, nil, 1)
	b8Item(t, d, "tooRecent", 100, nil, 1)

	// Solar window with days=28 is [now-365d, now-337d). -364d and -360d are
	// inside; -366d is before BOTH windows (lunar starts -354d) and stays
	// out. -330d is outside solar but inside lunar [-354d, -326d) → now in.
	at := func(daysAgo int) string { return b8At(time.Now().AddDate(0, 0, -daysAgo)) }
	seed := func(saleID, itemID, variantID, name string, qty float64, when string) {
		b8Sale(t, d, saleID, when, "completed", "sale", 0, 100)
		b8Line(t, d, saleID, 1, itemID, variantID, name, qty, 0, 0, 100, 100)
	}
	seed("s1", "pumpkin", "", "Pumpkin", 5, at(364))
	seed("s2", "covered", "", "Covered", 2, at(360))
	seed("s3", "", "v2", "Scarf Red", 4, at(364))
	seed("s4", "retired", "", "Retired", 9, at(364))
	seed("s5", "tooOld", "", "Too old", 9, at(366))
	seed("s6", "tooRecent", "", "Too recent", 9, at(330))
	// Weighed stock: 2.5 sold last year, 1 on hand -> need 1.5, and only
	// Ceil (per the doc comment) suggests 2; Floor would say 1.
	seed("s7", "berry", "", "Berry", 2.5, at(362))
	// Inside the default 28-day window [-365d,-337d) but OUTSIDE a 14-day
	// one [-365d,-351d): only visible when days<=0 really defaults to 28.
	seed("s8", "middle", "", "Middle", 1, at(340))

	rows, _, err := repo.SeasonalForecast(ctx, 28, 10)
	if err != nil {
		t.Fatalf("SeasonalForecast: %v", err)
	}
	if len(rows) != 6 {
		t.Fatalf("expected 6 seasonal items, got %d: %+v", len(rows), rows)
	}
	// Ordered by expected units DESC. tooRecent (-330d, lunar-only window)
	// leads: the old code missed it entirely.
	if rows[0].Name != "Name tooRecent" || rows[0].Expected != 9 || !rows[0].Lunar || rows[0].SuggestQty != 9 {
		t.Fatalf("tooRecent = %+v, want expected 9 Lunar=true suggest 9", rows[0])
	}
	if rows[1].Name != "Name pumpkin" || rows[1].Expected != 5 || rows[1].OnHand != 2 || rows[1].SuggestQty != 3 || rows[1].Lunar {
		t.Fatalf("pumpkin = %+v, want expected 5 onHand 2 suggest 3", rows[1])
	}
	if rows[2].Name != "Name scarf" || rows[2].Expected != 4 || rows[2].OnHand != 0 || rows[2].SuggestQty != 4 {
		t.Fatalf("scarf (variant path) = %+v, want expected 4 onHand 0 suggest 4", rows[2])
	}
	// Fractional need rounds UP: ceil(2.5 - 1) = 2 (Floor would give 1).
	if rows[3].Name != "Name berry" || rows[3].Expected != 2.5 || rows[3].OnHand != 1 || rows[3].SuggestQty != 2 {
		t.Fatalf("berry = %+v, want expected 2.5 onHand 1 suggest 2", rows[3])
	}
	// Fully covered: suggestion clamps to 0, never negative.
	if rows[4].Name != "Name covered" || rows[4].Expected != 2 || rows[4].OnHand != 10 || rows[4].SuggestQty != 0 {
		t.Fatalf("covered = %+v, want expected 2 onHand 10 suggest 0", rows[4])
	}
	// middle (-340d) sits in BOTH windows: max(), not sum — still 1.
	if rows[5].Name != "Name middle" || rows[5].Expected != 1 || rows[5].SuggestQty != 1 {
		t.Fatalf("middle = %+v, want expected 1 suggest 1", rows[5])
	}

	// days <= 0 falls back to the 28-day default: identical result,
	// INCLUDING "middle" (sold -340d), which a shorter default would drop.
	def, _, err := repo.SeasonalForecast(ctx, 0, 10)
	if err != nil {
		t.Fatalf("SeasonalForecast(0): %v", err)
	}
	if len(def) != 6 || def[0].Name != rows[0].Name || def[0].SuggestQty != rows[0].SuggestQty || def[5].Name != "Name middle" {
		t.Fatalf("days<=0 must default to 28 (incl. the -340d sale): got %+v", def)
	}

	// LIMIT trims from the bottom of the expected ordering.
	top, _, err := repo.SeasonalForecast(ctx, 28, 1)
	if err != nil {
		t.Fatalf("SeasonalForecast(limit 1): %v", err)
	}
	if len(top) != 1 || top[0].Name != "Name tooRecent" {
		t.Fatalf("limit 1 must keep the biggest seasonal seller, got %+v", top)
	}
}

func TestPOSRepo_SalesByWeekdayAndHour_BucketsLocalTime(t *testing.T) {
	d := b8OpenDB(t, "busy.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	// Two sales a couple of hours ago, one ~27h ago (guaranteed different
	// local hour AND different local weekday from t1).
	t1 := time.Now().Add(-2 * time.Hour)
	t2 := time.Now().Add(-27 * time.Hour)
	b8Sale(t, d, "s1", b8At(t1), "completed", "sale", 0, 400)
	b8Sale(t, d, "s2", b8At(t1), "completed", "sale", 0, 600)
	b8Sale(t, d, "s3", b8At(t2), "completed", "sale", 0, 500)
	// Excluded: return-type, voided, and out-of-window rows.
	b8Sale(t, d, "s4", b8At(t1), "completed", "return", 0, 77777)
	b8Sale(t, d, "s5", b8At(t1), "voided", "sale", 0, 88888)
	b8Sale(t, d, "s6", b8At(time.Now().Add(-40*24*time.Hour)), "completed", "sale", 0, 99999)

	type agg struct {
		count int
		total int64
	}
	// Expected buckets computed by SQLite itself from the same stored
	// strings, through the same strftime(...,'localtime') conversion the
	// production query uses — this pins the %w weekday numbering (0=Sun,
	// not ISO %u) and SQLite-vs-Go local-clock agreement in EVERY
	// timezone, including CI's UTC. Honest residual (independent review,
	// batch 8): under TZ=UTC a production query that dropped 'localtime'
	// is behaviorally invisible — local time IS UTC there — so that one
	// regression is only catchable on non-UTC machines.
	ctrlSlot := func(format string, tm time.Time, goSlot int) int {
		var slot int
		if err := d.DB.QueryRow(`SELECT CAST(strftime(?, ?, 'localtime') AS INTEGER)`,
			format, b8At(tm)).Scan(&slot); err != nil {
			t.Fatalf("control slot query: %v", err)
		}
		if slot != goSlot {
			t.Fatalf("sqlite localtime %s=%d disagrees with Go local %d for %s — driver drift", format, slot, goSlot, b8At(tm))
		}
		return slot
	}
	expect := func(format string, goSlotOf func(time.Time) int) map[int]agg {
		exp := map[int]agg{}
		add := func(tm time.Time, total int64) {
			s := ctrlSlot(format, tm, goSlotOf(tm))
			a := exp[s]
			a.count++
			a.total += total
			exp[s] = a
		}
		add(t1, 400)
		add(t1, 600)
		add(t2, 500)
		return exp
	}
	check := func(name string, got []BusySlot, exp map[int]agg) {
		t.Helper()
		if len(got) != len(exp) {
			t.Fatalf("%s: expected %d buckets, got %d: %+v", name, len(exp), len(got), got)
		}
		for i, b := range got {
			w, ok := exp[b.Slot]
			if !ok || b.Count != w.count || b.Total != w.total {
				t.Fatalf("%s bucket %d = %+v, want %+v (known slot %v)", name, i, b, w, ok)
			}
			if i > 0 && got[i-1].Slot >= b.Slot {
				t.Fatalf("%s buckets must be ordered by slot ascending: %+v", name, got)
			}
		}
	}

	byHour, err := repo.SalesByHour(ctx, winFrom(7), winTo())
	if err != nil {
		t.Fatalf("SalesByHour: %v", err)
	}
	check("SalesByHour", byHour, expect("%H", func(tm time.Time) int { return tm.Local().Hour() }))

	byWeekday, err := repo.SalesByWeekday(ctx, winFrom(7), winTo())
	if err != nil {
		t.Fatalf("SalesByWeekday: %v", err)
	}
	check("SalesByWeekday", byWeekday, expect("%w", func(tm time.Time) int { return int(tm.Local().Weekday()) }))
}

func TestPOSRepo_ItemDailySellRates_NetOfReturnsPerDay(t *testing.T) {
	d := b8OpenDB(t, "sellrates.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "apple", 50, nil, 1)
	b8Item(t, d, "banana", 30, nil, 1)
	b8Item(t, d, "cherry", 20, nil, 1)
	b8Item(t, d, "pear", 80, nil, 1)
	b8Item(t, d, "melon", 200, nil, 1)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, name, price) VALUES ('v-pear', 'pear', 'Conference', 80)`)

	now := b8At(time.Now().Add(-2 * time.Hour))
	seed := func(saleID, status, saleType, itemID, variantID string, qty float64, when string) {
		b8Sale(t, d, saleID, when, status, saleType, 0, 100)
		b8Line(t, d, saleID, 1, itemID, variantID, "n-"+saleID, qty, 0, 0, 100, 100)
	}
	// Apple: 10 + 4 sold in-window → 14 / 7 days = 2.0.
	seed("s1", "completed", "sale", "apple", "", 10, now)
	seed("s2", "completed", "sale", "apple", "", 4, now)
	// Banana: 7 sold, 7 returned → net 0 → absent.
	seed("s3", "completed", "sale", "banana", "", 7, now)
	seed("s4", "completed", "return", "banana", "", 7, now)
	// Cherry: 3 sold, 1 returned → net 2 → 2/7.
	seed("s5", "completed", "sale", "cherry", "", 3, now)
	seed("s6", "completed", "return", "cherry", "", 1, now)
	// Pear: sold via variant → resolves to the parent item id. 7/7 = 1.0.
	seed("s7", "completed", "sale", "", "v-pear", 7, now)
	// Melon: only outside the window → absent.
	seed("s8", "completed", "sale", "melon", "", 9, b8At(time.Now().Add(-10*24*time.Hour)))
	// Voided apple sale: never counted.
	seed("s9", "voided", "sale", "apple", "", 100, now)

	// A single shared "to" (not two independent winTo() calls) keeps the
	// window's span exactly 7.0*24h — ItemDailySellRates now divides by the
	// window's own real span rather than a passed-in int, so two
	// independently-timed "now" instants would drift the divisor by a few
	// microseconds and turn the exact-value assertions below flaky.
	sevenDayTo := winTo()
	sevenDayFrom := sevenDayTo.Add(-7 * 24 * time.Hour)
	rates, err := repo.ItemDailySellRates(ctx, sevenDayFrom, sevenDayTo)
	if err != nil {
		t.Fatalf("ItemDailySellRates: %v", err)
	}
	if len(rates) != 3 {
		t.Fatalf("expected 3 items with movement, got %#v", rates)
	}
	if rates["apple"] != 2.0 {
		t.Fatalf("apple rate = %v, want 2.0", rates["apple"])
	}
	if rates["cherry"] != 2.0/7.0 {
		t.Fatalf("cherry rate = %v, want %v", rates["cherry"], 2.0/7.0)
	}
	if rates["pear"] != 1.0 {
		t.Fatalf("pear (variant path) rate = %v, want 1.0", rates["pear"])
	}
	if _, ok := rates["banana"]; ok {
		t.Fatalf("banana netted to zero and must be absent: %#v", rates)
	}
	if _, ok := rates["melon"]; ok {
		t.Fatalf("melon sold outside the window and must be absent: %#v", rates)
	}

	// The divisor is the window's own span, not a hardcoded count: an
	// explicit 28-day window (apple's 14 units sold ARE all inside it, same
	// "now" seed) divides by 28, not 7. A single shared "to" (see the
	// sevenDayTo comment above) keeps the span exactly 28.0*24h.
	twentyEightDayTo := winTo()
	twentyEightDayFrom := twentyEightDayTo.Add(-28 * 24 * time.Hour)
	def, err := repo.ItemDailySellRates(ctx, twentyEightDayFrom, twentyEightDayTo)
	if err != nil {
		t.Fatalf("ItemDailySellRates(28-day window): %v", err)
	}
	if def["apple"] != 14.0/28.0 {
		t.Fatalf("apple rate with a 28-day window = %v, want 0.5", def["apple"])
	}

	// A non-positive span (to <= from) has no meaningful daily rate: empty
	// map, no error, no divide-by-zero.
	sameInstant := winTo()
	zero, err := repo.ItemDailySellRates(ctx, sameInstant, sameInstant)
	if err != nil {
		t.Fatalf("ItemDailySellRates(zero-width window): %v", err)
	}
	if len(zero) != 0 {
		t.Fatalf("zero-width window must return an empty map, got %#v", zero)
	}
}

func TestPOSRepo_Reports_EmptyDB(t *testing.T) {
	d := b8OpenDB(t, "empty.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	// A fresh DB is NOT empty: 001_init.sql seeds a demo catalog with stock
	// (but no sales). Clear inventory so DeadStock's empty path is real.
	mustExec(t, d, `DELETE FROM inventory`)

	if rows, err := repo.SalesByDay(ctx, winFrom(7), winTo()); err != nil || len(rows) != 0 {
		t.Fatalf("SalesByDay empty = (%v, %v)", rows, err)
	}
	if rows, err := repo.SlowItems(ctx, winFrom(7), winTo(), 5); err != nil || len(rows) != 0 {
		t.Fatalf("SlowItems empty = (%v, %v)", rows, err)
	}
	if rows, err := repo.DeadStock(ctx, winFrom(7), winTo(), 5); err != nil || len(rows) != 0 {
		t.Fatalf("DeadStock empty = (%v, %v)", rows, err)
	}
	if rows, err := repo.TaxSummary(ctx, winFrom(7), winTo()); err != nil || len(rows) != 0 {
		t.Fatalf("TaxSummary empty = (%v, %v)", rows, err)
	}
	if rows, err := repo.MarginByItem(ctx, winFrom(7), winTo(), 5); err != nil || len(rows) != 0 {
		t.Fatalf("MarginByItem empty = (%v, %v)", rows, err)
	}
	if total, count, err := repo.DayTotal(ctx, 0); err != nil || total != 0 || count != 0 {
		t.Fatalf("DayTotal empty = (%d, %d, %v)", total, count, err)
	}
	if rows, cats, err := repo.SeasonalForecast(ctx, 28, 5); err != nil || len(rows) != 0 || len(cats) != 0 {
		t.Fatalf("SeasonalForecast empty = (%v, %v, %v)", rows, cats, err)
	}
	if rows, err := repo.SalesByWeekday(ctx, winFrom(7), winTo()); err != nil || len(rows) != 0 {
		t.Fatalf("SalesByWeekday empty = (%v, %v)", rows, err)
	}
	if rows, err := repo.SalesByHour(ctx, winFrom(7), winTo()); err != nil || len(rows) != 0 {
		t.Fatalf("SalesByHour empty = (%v, %v)", rows, err)
	}
	if rates, err := repo.ItemDailySellRates(ctx, winFrom(7), winTo()); err != nil || len(rates) != 0 {
		t.Fatalf("ItemDailySellRates empty = (%#v, %v)", rates, err)
	}
	if total, count, err := repo.RefundsByWindow(ctx, winFrom(7), winTo()); err != nil || total != 0 || count != 0 {
		t.Fatalf("RefundsByWindow empty = (%d, %d, %v)", total, count, err)
	}

	// A closed DB must surface an error from every report, never a silent
	// empty result (batch-7 convention; also exercises the query-error wraps).
	_ = d.Close()
	if _, err := repo.SalesByDay(ctx, winFrom(7), winTo()); err == nil {
		t.Fatal("SalesByDay on a closed DB must error")
	}
	if _, err := repo.SlowItems(ctx, winFrom(7), winTo(), 5); err == nil {
		t.Fatal("SlowItems on a closed DB must error")
	}
	if _, err := repo.DeadStock(ctx, winFrom(7), winTo(), 5); err == nil {
		t.Fatal("DeadStock on a closed DB must error")
	}
	if _, err := repo.TaxSummary(ctx, winFrom(7), winTo()); err == nil {
		t.Fatal("TaxSummary on a closed DB must error")
	}
	if _, err := repo.MarginByItem(ctx, winFrom(7), winTo(), 5); err == nil {
		t.Fatal("MarginByItem on a closed DB must error")
	}
	if _, _, err := repo.DayTotal(ctx, 0); err == nil {
		t.Fatal("DayTotal on a closed DB must error")
	}
	if _, _, err := repo.SeasonalForecast(ctx, 28, 5); err == nil {
		t.Fatal("SeasonalForecast on a closed DB must error")
	}
	if _, err := repo.SalesByWeekday(ctx, winFrom(7), winTo()); err == nil {
		t.Fatal("SalesByWeekday on a closed DB must error")
	}
	if _, err := repo.SalesByHour(ctx, winFrom(7), winTo()); err == nil {
		t.Fatal("SalesByHour on a closed DB must error")
	}
	if _, err := repo.ItemDailySellRates(ctx, winFrom(7), winTo()); err == nil {
		t.Fatal("ItemDailySellRates on a closed DB must error")
	}
	if _, _, err := repo.RefundsByWindow(ctx, winFrom(7), winTo()); err == nil {
		t.Fatal("RefundsByWindow on a closed DB must error")
	}
}
