package data

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#84 remainder: SeasonalForecast — multi-year averaging, lunar-window
// shift awareness, category rollups. Real DB, real migrations, seeded sales.
//
// Window geometry used throughout (days=28):
//   solar k=1  [-365d, -337d)    lunar k=1  [-354d, -326d)
//   solar k=2  [-730d, -702d)    lunar k=2  [-709d, -681d)
//   solar k=3 [-1095d,-1067d)    lunar k=3 [-1063d,-1035d)

func sfAt(daysAgo int) string { return b8At(time.Now().AddDate(0, 0, -daysAgo)) }

func sfSale(t *testing.T, d *db.DB, saleID, itemID string, qty float64, daysAgo int) {
	t.Helper()
	b8Sale(t, d, saleID, sfAt(daysAgo), "completed", "sale", 0, 100)
	b8Line(t, d, saleID, 1, itemID, "", "Line "+itemID, qty, 0, 0, 100, 100)
}

func TestPOSRepo_SeasonalForecast_LunarWindowCatchesShiftedDemand(t *testing.T) {
	d := b8OpenDB(t, "sf_lunar.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "steady", 100, nil, 1)
	b8Item(t, d, "moving", 100, nil, 1)

	// One year of history. "steady" sold in the solar window; "moving" sold
	// at -330d — INSIDE lunar k=1 [-354,-326) but OUTSIDE solar k=1
	// [-365,-337): the lunar-holiday case the old single-window code missed.
	sfSale(t, d, "s1", "steady", 5, 360)
	sfSale(t, d, "s2", "moving", 9, 330)

	items, _, err := repo.SeasonalForecast(ctx, 28, 10)
	if err != nil {
		t.Fatalf("SeasonalForecast: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d: %+v", len(items), items)
	}
	// Ordered by expected DESC: moving (9) first.
	if items[0].Name != "Name moving" || items[0].Expected != 9 || !items[0].Lunar || items[0].SuggestQty != 9 {
		t.Fatalf("moving = %+v, want expected 9, Lunar=true, suggest 9", items[0])
	}
	if items[1].Name != "Name steady" || items[1].Expected != 5 || items[1].Lunar {
		t.Fatalf("steady = %+v, want expected 5, Lunar=false", items[1])
	}
}

func TestPOSRepo_SeasonalForecast_MultiYearAveraging(t *testing.T) {
	d := b8OpenDB(t, "sf_multiyear.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "veteran", 100, nil, 1)
	b8Item(t, d, "newcomer", 100, nil, 1)
	b8Item(t, d, "faded", 100, nil, 1)

	// Two full years of history (oldest sale -729d anchors availability).
	// veteran: 100 units in the year-1 solar window, 50 in year-2's.
	sfSale(t, d, "v1", "veteran", 100, 364)
	sfSale(t, d, "v2", "veteran", 50, 729)
	// newcomer: exists only since last year — 40 units in year-1's window.
	// Must NOT be diluted by the shop's second year of history.
	sfSale(t, d, "n1", "newcomer", 40, 364)
	// faded: sold only two years ago (30 units) — averaged down over the
	// two years since its signal first appears.
	sfSale(t, d, "f1", "faded", 30, 729)

	items, _, err := repo.SeasonalForecast(ctx, 28, 10)
	if err != nil {
		t.Fatalf("SeasonalForecast: %v", err)
	}
	got := map[string]SeasonalItem{}
	for _, it := range items {
		got[it.Name] = it
	}
	if it := got["Name veteran"]; it.Expected != 75 || it.Years != 2 {
		t.Fatalf("veteran = %+v, want expected (100+50)/2 = 75 over 2 years", it)
	}
	if it := got["Name newcomer"]; it.Expected != 40 || it.Years != 1 {
		t.Fatalf("newcomer = %+v, want expected 40 over 1 year (no dilution)", it)
	}
	if it := got["Name faded"]; it.Expected != 15 || it.Years != 2 {
		t.Fatalf("faded = %+v, want expected 30/2 = 15 over 2 years", it)
	}
}

func TestPOSRepo_SeasonalForecast_SameSaleNotDoubleCountedAcrossWindows(t *testing.T) {
	d := b8OpenDB(t, "sf_overlap.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "both", 100, nil, 1)
	// -340d sits in BOTH solar k=1 [-365,-337) and lunar k=1 [-354,-326).
	// Per-year signal is max(solar, lunar), so it must count once, not twice.
	sfSale(t, d, "b1", "both", 7, 340)

	items, _, err := repo.SeasonalForecast(ctx, 28, 10)
	if err != nil {
		t.Fatalf("SeasonalForecast: %v", err)
	}
	if len(items) != 1 || items[0].Expected != 7 {
		t.Fatalf("overlap item = %+v, want expected 7 (counted once)", items)
	}
	if items[0].Lunar {
		t.Fatalf("equal solar/lunar signal must not flag lunar: %+v", items[0])
	}
}

// TestPOSRepo_SeasonalForecast_ThreeYearsWithOverlapAndLunarK3 exercises all
// three prior years together on one item, INCLUDING lunar k=3 — no prior
// test placed a sale in that specific window (ut-docs#199 review finding).
// It's also the first test to combine a k=1 solar/lunar overlap with signal
// in k=2 and k=3 in the SAME query, the exact shape the single-scan
// seasonalWindowQuery rewrite (up to 6 CASE-bucketed columns in one row) had
// no dedicated coverage for.
func TestPOSRepo_SeasonalForecast_ThreeYearsWithOverlapAndLunarK3(t *testing.T) {
	d := b8OpenDB(t, "sf_3yr_overlap.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "anchor", 100, nil, 1)
	b8Item(t, d, "thorough", 100, nil, 1)

	// anchor's own sale is old enough on its own to pin yearsAvail=3
	// (3*365-28=1067 days), independent of "thorough"'s windows below.
	sfSale(t, d, "anchor1", "anchor", 1, 1094)

	// k=1: −340d sits in BOTH solar[1] [−365,−337) and lunar[1] [−354,−326)
	// — must count ONCE via max(solar,lunar).
	sfSale(t, d, "th-k1", "thorough", 6, 340)
	// k=2: −720d is solar[2] [−730,−702) ONLY (lunar[2] is [−709,−681)).
	sfSale(t, d, "th-k2", "thorough", 10, 720)
	// k=3: −1050d is lunar[3] [−1063,−1035) ONLY (solar[3] is [−1095,−1067)).
	sfSale(t, d, "th-k3", "thorough", 8, 1050)

	items, _, err := repo.SeasonalForecast(ctx, 28, 10)
	if err != nil {
		t.Fatalf("SeasonalForecast: %v", err)
	}
	got := map[string]SeasonalItem{}
	for _, it := range items {
		got[it.Name] = it
	}
	it, ok := got["Name thorough"]
	if !ok {
		t.Fatalf("thorough item missing from %+v", items)
	}
	// (max(6,6) + max(10,0) + max(0,8)) / 3 = 24/3 = 8.0;
	// sumSolar = 6+10+0 = 16 > sumLunar = 6+0+8 = 14, so not Lunar.
	if it.Expected != 8 || it.Years != 3 || it.Lunar {
		t.Fatalf("thorough = %+v, want Expected 8, Years 3, Lunar false", it)
	}
}

// TestPOSRepo_SeasonalForecast_ZeroSumWindowItemDoesNotPoisonCategoryWithNaN
// covers a gap the single-scan rewrite opened (ut-docs#199 review finding):
// the old per-window queries used HAVING units > 0, so an item could never
// enter the result with an all-zero signal. seasonalWindowQuery's WHERE only
// guarantees a matching ROW existed in some window, not that it summed
// positive — a net-zero-quantity line still passes it. Without the
// firstK==0 guard in SeasonalForecast, that item's Expected becomes 0/0 =
// NaN, which then poisons its entire category's rollup (Expected += NaN).
func TestPOSRepo_SeasonalForecast_ZeroSumWindowItemDoesNotPoisonCategoryWithNaN(t *testing.T) {
	d := b8OpenDB(t, "sf_zerosum.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	mustExec(t, d, `INSERT INTO categories (id, name) VALUES ('c1', 'Bakery')`)
	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES
		('anchor', 'SKU-anchor', 'Name anchor', 100, 1, 'c1'),
		('zeroed', 'SKU-zeroed', 'Name zeroed', 100, 1, 'c1')`)

	sfSale(t, d, "a1", "anchor", 5, 360)
	// A zero-quantity line still produces a row inside the k=1 window, so it
	// passes seasonalWindowQuery's WHERE gate, but its window sum is 0.
	b8Sale(t, d, "z1", sfAt(350), "completed", "sale", 0, 100)
	b8Line(t, d, "z1", 1, "zeroed", "", "Zeroed", 0, 0, 0, 100, 100)

	items, cats, err := repo.SeasonalForecast(ctx, 28, 10)
	if err != nil {
		t.Fatalf("SeasonalForecast: %v", err)
	}
	for _, it := range items {
		if it.Name == "Name zeroed" {
			t.Fatalf("zero-signal item must not appear in the forecast at all: %+v", it)
		}
		if math.IsNaN(it.Expected) {
			t.Fatalf("NaN leaked into an item: %+v", it)
		}
	}
	for _, c := range cats {
		if math.IsNaN(c.Expected) {
			t.Fatalf("NaN leaked into category rollup: %+v", c)
		}
	}
}

func TestPOSRepo_SeasonalForecast_CategoryRollup(t *testing.T) {
	d := b8OpenDB(t, "sf_cats.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	mustExec(t, d, `INSERT INTO categories (id, name) VALUES ('c1', 'Bakery')`)
	mustExec(t, d, `INSERT INTO stock_locations (id, name) VALUES ('loc1', 'Shop')`)
	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES
		('bread', 'SKU-bread', 'Name bread', 100, 1, 'c1'),
		('bun',   'SKU-bun',   'Name bun',   100, 1, 'c1')`)
	b8Item(t, d, "loose", 100, nil, 1) // no category → uncategorized bucket
	// Bread has a genuine SURPLUS (20 on hand vs 10 expected). This is the
	// discriminating fixture: summed per-item suggestions give 0+6=6, while
	// the forbidden ceil(catExpected−catOnHand) = ceil(16−20) would give 0 —
	// bread's surplus must not cover the buns.
	b8Stock(t, d, "inv1", "bread", "loc1", 20)

	sfSale(t, d, "s1", "bread", 10, 360)
	sfSale(t, d, "s2", "bun", 6, 360)
	sfSale(t, d, "s3", "loose", 3, 360)

	items, cats, err := repo.SeasonalForecast(ctx, 28, 10)
	if err != nil {
		t.Fatalf("SeasonalForecast: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %+v", items)
	}
	if items[0].Category != "Bakery" {
		t.Fatalf("bread category = %q, want Bakery", items[0].Category)
	}
	if len(cats) != 2 {
		t.Fatalf("expected 2 category buckets, got %+v", cats)
	}
	if cats[0].Name != "Bakery" || cats[0].Expected != 16 || cats[0].OnHand != 20 || cats[0].SuggestQty != 6 {
		t.Fatalf("Bakery rollup = %+v, want expected 16 onHand 20 suggest 6 (sum of member suggestions, NOT ceil(16-20)=0)", cats[0])
	}
	if cats[1].Name != "" || cats[1].Expected != 3 || cats[1].SuggestQty != 3 {
		t.Fatalf("uncategorized rollup = %+v, want expected 3 suggest 3", cats[1])
	}
}

func TestPOSRepo_SeasonalForecast_VariantStockCountsAsOnHand(t *testing.T) {
	d := b8OpenDB(t, "sf_variantstock.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	// An item sold AND stocked at variant level: the demand query folds the
	// variant sale up to the parent item, so the on-hand lookup must fold
	// variant stock up too — otherwise the shop is told to reorder scarves
	// while 50 sit on the shelf.
	b8Item(t, d, "scarf", 800, nil, 1)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, name, price) VALUES ('v1', 'scarf', 'Red', 800)`)
	mustExec(t, d, `INSERT INTO stock_locations (id, name) VALUES ('loc1', 'Shop')`)
	mustExec(t, d, `INSERT INTO inventory (id, variant_id, location_id, quantity) VALUES ('inv-v1', 'v1', 'loc1', 50)`)
	b8Sale(t, d, "s1", sfAt(360), "completed", "sale", 0, 100)
	b8Line(t, d, "s1", 1, "", "v1", "Scarf Red", 4, 0, 0, 100, 100)

	items, _, err := repo.SeasonalForecast(ctx, 28, 10)
	if err != nil {
		t.Fatalf("SeasonalForecast: %v", err)
	}
	if len(items) != 1 || items[0].OnHand != 50 || items[0].SuggestQty != 0 {
		t.Fatalf("variant-stocked item = %+v, want onHand 50 suggest 0", items)
	}
}

func TestPOSRepo_SeasonalForecast_LunarWindowBoundaryPinsYearLength(t *testing.T) {
	d := b8OpenDB(t, "sf_lunarboundary.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "anchor", 100, nil, 1)
	b8Item(t, d, "edge", 100, nil, 1)

	// anchor establishes one year of history via the solar window. edge sits
	// at −326.5d: inside lunar k=1 [−354d, −326d) only if the lunar year
	// rounds to 354 (a 355-day constant would shift the window to [−355,−327)
	// and drop it). Half-day offset keeps clear of the now-vs-now race
	// between this process and SQLite's 'now'.
	sfSale(t, d, "s1", "anchor", 5, 360)
	b8Sale(t, d, "s2", b8At(time.Now().Add(-326*24*time.Hour-12*time.Hour)), "completed", "sale", 0, 100)
	b8Line(t, d, "s2", 1, "edge", "", "Edge", 3, 0, 0, 100, 100)

	items, _, err := repo.SeasonalForecast(ctx, 28, 10)
	if err != nil {
		t.Fatalf("SeasonalForecast: %v", err)
	}
	names := map[string]bool{}
	for _, it := range items {
		names[it.Name] = true
	}
	if !names["Name edge"] || !names["Name anchor"] {
		t.Fatalf("edge sale at -326.5d must land inside the 354-day lunar window, got %+v", items)
	}
}

// ut-docs#199: SeasonalForecast used to run one query PER window (up to 6
// full sale_lines/sales scans per call — 3 years × solar+lunar — each
// unindexable because the datetime(s.created_at) wrapper the storage format
// requires defeats idx_sales_created). seasonalWindowQuery collapses all
// requested windows into ONE query via CASE-bucketed sums, so the join is
// scanned once no matter how many windows are requested.
func TestSeasonalWindowQuery_IsSingleScanNotOnePerWindow(t *testing.T) {
	windows := []seasonalWindow{
		{k: 1, lunar: false, offset: 365},
		{k: 1, lunar: true, offset: 354},
		{k: 2, lunar: false, offset: 730},
		{k: 2, lunar: true, offset: 708},
	}
	query, args := seasonalWindowQuery(windows, 28)

	if n := strings.Count(strings.ToUpper(query), "FROM SALE_LINES"); n != 1 {
		t.Fatalf("query joins sale_lines %d times, want exactly 1 (one query, not one per window):\n%s", n, query)
	}
	// Each window's bound pair is used twice — once in its SELECT CASE, once
	// in the WHERE OR — so 4 windows × 2 uses × 2 bounds (from/to) = 16 args.
	if want := len(windows) * 4; len(args) != want {
		t.Fatalf("args = %d, want %d", len(args), want)
	}
}

// TestSeasonalWindowQuery_PlanIsOneScan runs the real generated query
// through SQLite's own planner: EXPLAIN QUERY PLAN must show sale_lines
// scanned exactly once, per ut-docs#199's acceptance criteria.
func TestSeasonalWindowQuery_PlanIsOneScan(t *testing.T) {
	d := b8OpenDB(t, "sf_plan.db")
	windows := []seasonalWindow{
		{k: 1, lunar: false, offset: 365},
		{k: 1, lunar: true, offset: 354},
		{k: 2, lunar: false, offset: 730},
		{k: 2, lunar: true, offset: 708},
		{k: 3, lunar: false, offset: 1095},
		{k: 3, lunar: true, offset: 1063},
	}
	query, args := seasonalWindowQuery(windows, 28)

	rows, err := d.DB.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	scans := 0
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		// Match the sale_lines table by its "sl" alias as SQLite's own
		// planner names it ("SCAN sl" / "SEARCH sl USING INDEX ..."), not by
		// a substring of the detail text — an index happening to be named
		// with "sale_lines" in it (or not) is a planner/schema detail this
		// test shouldn't be coupled to; the alias token is what's actually
		// being asserted (ut-docs#199 review finding).
		if fields := strings.Fields(detail); len(fields) >= 2 && fields[1] == "sl" {
			scans++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	if scans != 1 {
		t.Fatalf("plan scans sale_lines (alias sl) %d times, want 1 (single scan for all 6 windows):\n%s", scans, query)
	}
}

func TestPOSRepo_SeasonalForecast_EmptyWithoutHistory(t *testing.T) {
	d := b8OpenDB(t, "sf_empty.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "young", 100, nil, 1)
	// Recent sales only — no year-old history, card stays hidden.
	sfSale(t, d, "s1", "young", 5, 10)

	items, cats, err := repo.SeasonalForecast(ctx, 28, 10)
	if err != nil {
		t.Fatalf("SeasonalForecast: %v", err)
	}
	if len(items) != 0 || len(cats) != 0 {
		t.Fatalf("fresh shop must forecast nothing, got items=%+v cats=%+v", items, cats)
	}
}

func TestPOSRepo_SeasonalForecast_ExpectedRoundedToOneDecimal(t *testing.T) {
	d := b8OpenDB(t, "sf_round.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "thirds", 100, nil, 1)
	// Three years of signal: 10+10+5 over 3 years = 8.333… → 8.3 displayed.
	sfSale(t, d, "y1", "thirds", 10, 364)
	sfSale(t, d, "y2", "thirds", 10, 729)
	sfSale(t, d, "y3", "thirds", 5, 1094)

	items, _, err := repo.SeasonalForecast(ctx, 28, 10)
	if err != nil {
		t.Fatalf("SeasonalForecast: %v", err)
	}
	if len(items) != 1 || math.Abs(items[0].Expected-8.3) > 1e-9 || items[0].Years != 3 {
		t.Fatalf("thirds = %+v, want expected 8.3 over 3 years", items)
	}
	// Suggestion still rounds the need UP: ceil(8.3 − 0) = 9.
	if items[0].SuggestQty != 9 {
		t.Fatalf("suggest = %d, want 9", items[0].SuggestQty)
	}
}
