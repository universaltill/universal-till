package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// The inventory page predicts days-of-stock-left from the last 28 days of
// sales: a fast seller with little stock gets the ⚠ running-out flag; an
// item with no sales history shows no prediction.
func TestInventoryPredictsDaysLeft(t *testing.T) {
	chdirRoot(t)
	// Real migrations: the query spans sales + sale_lines + inventory.
	f := filepath.Join(t.TempDir(), "inv.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	d := database.DB

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v (%s)", err, q)
		}
	}
	// Two items: "Fast Cola" sells 2/day with 6 in stock (≈3 days → warn);
	// "Dusty Vase" never sells (no prediction).
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active) VALUES ('it-cola','Fast Cola','COLA',100,1)`)
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active) VALUES ('it-vase','Dusty Vase','VASE',500,1)`)
	mustExec(`INSERT INTO stock_locations (id, name) VALUES ('loc-1','Shop floor')`)
	mustExec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-1','it-cola','loc-1',6)`)
	mustExec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-2','it-vase','loc-1',9)`)
	// 56 units over the window (28 days × 2/day), spread over recent days.
	for i := 0; i < 14; i++ {
		saleID := "s-" + string(rune('a'+i))
		mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
		          VALUES (?, ?, 'completed', 'sale', 400, 0, 400, datetime('now', ?))`,
			saleID, "R-"+saleID, "-"+string(rune('0'+i%9))+" days")
		mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		          VALUES (?, ?, 1, 'it-cola', 'Fast Cola', 4, 100, 0, 0, 0, 400, 400)`, "l-"+saleID, saleID)
	}

	// Sanity: repo rate ≈ 2/day.
	rates, err := data.NewPOSRepo(d).ItemDailySellRates(context.Background(), pagesWinFrom(28), pagesWinTo())
	if err != nil {
		t.Fatalf("rates: %v", err)
	}
	if r := rates["it-cola"]; r < 1.9 || r > 2.1 {
		t.Fatalf("cola rate = %v, want ≈2", r)
	}

	state := common.LoadState(context.Background(), settings.NewStore(d), &config.Config{Theme: "default"})
	dp := &common.Deps{Cfg: &config.Config{Theme: "default"}, Db: d, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(d)}
	mux := http.NewServeMux()
	registerInventoryPage(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/inventory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Days left") {
		t.Fatal("days-left column missing")
	}
	if !strings.Contains(body, "days-warn") {
		t.Fatal("fast seller with 3 days of stock should carry the warning")
	}
	if !strings.Contains(body, "predicted to run out") {
		t.Fatal("running-out chip missing from the header")
	}
	// Reorder suggestion: 2/day × 14-day cover − 6 on hand ≈ 22.
	if !strings.Contains(body, "order ~") || !strings.Contains(body, "order ~ 22") {
		t.Fatal("reorder suggestion missing or wrong quantity")
	}
	// The no-history item renders an em-dash, not a bogus prediction.
	if strings.Count(body, "days-warn") > 1 {
		t.Fatal("only the fast seller should warn")
	}
	// ut-docs#1493: the return-form's hidden offline-flag input must
	// actually render (a template edit that broke it would still return
	// 200 for this test's assertions above, since none of them touch the
	// return-form section) — this is what makes the offline signal reach
	// CreateReturn's handler at all for a real form submit.
	if !strings.Contains(body, `id="offline-flag" name="offline" value="0"`) {
		t.Fatal("return-form is missing its hidden offline-flag input (ut-docs#1493)")
	}
}

// ut-docs#2089's own regression test: a multi-variant item whose COMBINED
// stock is healthy relative to its COMBINED sell rate must not have every
// variant individually flagged as running out against the item's total
// rate. Reproduces the bug report's own worked example almost exactly — a
// T-Shirt selling 3/day total (1/day per variant) across Small/Medium/
// Large, each holding 20 units (60 total = ~20 days' cover, healthy). The
// pre-fix bug applied the item's combined 3/day rate against each
// variant's own 20-unit qty individually: floor(20/3)=6 <= the 7-day warn
// default → every variant misreported as running out.
func TestInventoryVariantSellRateIsPerVariantNotItemCombined(t *testing.T) {
	chdirRoot(t)
	f := filepath.Join(t.TempDir(), "variant-inv.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	d := database.DB

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v (%s)", err, q)
		}
	}
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active) VALUES ('it-tshirt','T-Shirt','TS',2000,1)`)
	mustExec(`INSERT INTO stock_locations (id, name) VALUES ('loc-1','Shop floor')`)
	for _, v := range []struct{ id, name string }{{"v-s", "Small"}, {"v-m", "Medium"}, {"v-l", "Large"}} {
		mustExec(`INSERT INTO item_variants (id, item_id, name, price, is_active) VALUES (?, 'it-tshirt', ?, 2000, 1)`, v.id, v.name)
		mustExec(`INSERT INTO inventory (id, variant_id, location_id, quantity) VALUES (?, ?, 'loc-1', 20)`, "inv-"+v.id, v.id)
		// One sale of 28 units within the 28-day window → exactly 1.0/day
		// for this variant alone (28/28 days).
		saleID := "s-" + v.id
		mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
		          VALUES (?, ?, 'completed', 'sale', 5600, 0, 5600, datetime('now', '-1 hours'))`, saleID, "R-"+v.id)
		mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, variant_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		          VALUES (?, ?, 1, ?, ?, 28, 2000, 0, 0, 0, 5600, 5600)`, "l-"+v.id, saleID, v.id, v.name)
	}

	// Sanity: each variant's own rate is 1.0/day, not the item's combined 3.0.
	repo := data.NewPOSRepo(d)
	variantRates, err := repo.VariantDailySellRates(context.Background(), pagesWinFrom(28), pagesWinTo())
	if err != nil {
		t.Fatalf("variant rates: %v", err)
	}
	for _, id := range []string{"v-s", "v-m", "v-l"} {
		if r := variantRates[id]; r < 0.9 || r > 1.1 {
			t.Fatalf("%s rate = %v, want ≈1.0 (its own rate, not the item's combined 3.0)", id, r)
		}
	}

	state := common.LoadState(context.Background(), settings.NewStore(d), &config.Config{Theme: "default"})
	dp := &common.Deps{Cfg: &config.Config{Theme: "default"}, Db: d, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(d)}
	mux := http.NewServeMux()
	registerInventoryPage(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/inventory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory: %d", rec.Code)
	}
	body := rec.Body.String()
	// None of the three variants should carry the running-out warning: each
	// holds 20 units at its own true 1/day rate → 20 days' cover, well past
	// the 7-day default warn window.
	if strings.Count(body, "days-warn") != 0 {
		t.Fatalf("no variant should warn at its own true per-variant rate; body: %s", body)
	}
	if strings.Contains(body, "predicted to run out") {
		t.Fatal("header must not show a running-out chip when every variant is individually healthy")
	}
}

// A flat 7-day warning window is wrong once an item's real reorder lead
// time is longer than that: warning only 7 days out for an item that takes
// 10 days to restock guarantees a stockout. lead_time_days makes the warn
// window (and the reorder-suggestion target) track the item's own lead
// time instead of the flat default.
func TestInventoryLeadTimeAwareWarnAndReorder(t *testing.T) {
	chdirRoot(t)
	f := filepath.Join(t.TempDir(), "inv.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	d := database.DB

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v (%s)", err, q)
		}
	}
	// "Slow Ship" sells 2/day (same rate shape as the cola fixture above) with
	// 16 in stock → DaysLeft=8, and a 10-day lead time: the old flat
	// warnDays=7 would NOT warn (8 > 7), but it must warn now (8 <= 10) —
	// otherwise reordering happens too late to avoid a stockout given how
	// long restocking actually takes.
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active, lead_time_days) VALUES ('it-slow','Slow Ship','SHIP',100,1,10)`)
	mustExec(`INSERT INTO stock_locations (id, name) VALUES ('loc-1','Shop floor')`)
	mustExec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-1','it-slow','loc-1',16)`)
	for i := 0; i < 14; i++ {
		saleID := "s-" + string(rune('a'+i))
		mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
		          VALUES (?, ?, 'completed', 'sale', 400, 0, 400, datetime('now', ?))`,
			saleID, "R-"+saleID, "-"+string(rune('0'+i%9))+" days")
		mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		          VALUES (?, ?, 1, 'it-slow', 'Slow Ship', 4, 100, 0, 0, 0, 400, 400)`, "l-"+saleID, saleID)
	}

	state := common.LoadState(context.Background(), settings.NewStore(d), &config.Config{Theme: "default"})
	dp := &common.Deps{Cfg: &config.Config{Theme: "default"}, Db: d, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(d)}
	mux := http.NewServeMux()
	registerInventoryPage(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/inventory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "days-warn") {
		t.Fatal("item with DaysLeft(8) <= its own 10-day lead time must warn, even though 8 > the flat 7-day default")
	}
	// Reorder target: rate(2/day) × (leadTime(10)+bufferDays(7)) − onHand(16) = 18.
	if !strings.Contains(body, "order ~ 18") {
		t.Fatalf("expected the reorder suggestion to target the item's own lead time + buffer, not the flat 14-day default; body: %s", body)
	}
}
