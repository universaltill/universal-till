package data

// ut-docs#1015: day-close gains a dine-in/takeaway revenue breakdown
// alongside the existing per-article-group/per-article/per-operator ones
// (ut-docs#1010). OrderTypeSalesForDay groups by sale_lines.order_type — the
// LINE's own normalized value ("" dine in, "takeaway" takeaway, per
// ADR-0073 Decision 1) — so a mixed sale's revenue splits across both
// buckets rather than needing a third "mixed" bucket, the same way BY
// ARTICLE decomposes a multi-item sale across its items. Net/Gross derive
// from sale_lines (total_before_tax/total_after_tax), the same convention
// ArticleGroupSales/ArticleSales/OperatorSales already use, so this
// reconciles with those three for the same window (see the sum-consistency
// test below, mirroring TestEODArticleBreakdowns_SumConsistency).

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

func TestOrderTypeSalesForDay_SplitsMixedSaleAcrossBuckets(t *testing.T) {
	d := b8OpenDB(t, "order-type-sales.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "coffee", 0, nil, 1)
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())

	// One MIXED sale: one dine-in line, one takeaway line. A sale-header-only
	// breakdown would have to invent a third "mixed" bucket; grouping by the
	// LINE's own order_type instead splits this correctly across the two
	// real buckets.
	b8Sale(t, d, "s1", b8At(today), "completed", "sale", 0, 0)
	b8Line(t, d, "s1", 1, "coffee", "", "Coffee (dine in)", 1, 0, 0, 300, 300)
	b8Line(t, d, "s1", 2, "coffee", "", "Coffee (takeaway)", 1, 0, 0, 250, 250)
	mustExec(t, d, `UPDATE sale_lines SET order_type = 'takeaway' WHERE id = 's1-l2'`)

	// A second, uniform takeaway-only sale — proves accumulation across
	// sales into the same bucket, not just within one sale's lines.
	b8Sale(t, d, "s2", b8At(today.Add(time.Hour)), "completed", "sale", 0, 0)
	b8Line(t, d, "s2", 1, "coffee", "", "Coffee (takeaway)", 2, 0, 0, 500, 500)
	mustExec(t, d, `UPDATE sale_lines SET order_type = 'takeaway' WHERE id = 's2-l1'`)

	day := b8ExpectedDay(t, d, today, 0, 0)
	rows, err := repo.OrderTypeSalesForDay(ctx, day)
	if err != nil {
		t.Fatalf("OrderTypeSalesForDay: %v", err)
	}
	got := map[string]OrderTypeSales{}
	for _, r := range rows {
		got[r.OrderType] = r
	}
	if len(got) != 2 {
		t.Fatalf("want 2 buckets (dine in, takeaway), got %+v", rows)
	}
	dineIn := got[""]
	if dineIn.Net != money.FromMinor(300) || dineIn.Gross != money.FromMinor(300) || dineIn.Qty != 1 {
		t.Fatalf("dine-in bucket = %+v, want net/gross 300 qty 1 (only s1's dine-in line)", dineIn)
	}
	takeaway := got["takeaway"]
	if takeaway.Net != money.FromMinor(750) || takeaway.Gross != money.FromMinor(750) || takeaway.Qty != 3 {
		t.Fatalf("takeaway bucket = %+v, want net/gross 750 qty 3 (s1's takeaway line + all of s2)", takeaway)
	}

	// ORDER BY order_type ASC is deliberate: "" sorts before "takeaway", so
	// dine in must come first regardless of which bucket has more revenue.
	if rows[0].OrderType != "" || rows[1].OrderType != "takeaway" {
		t.Fatalf("want dine-in (\"\") before takeaway regardless of revenue, got order %+v", rows)
	}
}

// TestOrderTypeSalesForDay_SumConsistency mirrors
// TestEODArticleBreakdowns_SumConsistency: the two buckets' Gross sum must
// reconcile with the day's total article revenue, computed independently the
// same way DepartmentsForDay/ArticleGroupsForDay's own baseline is.
func TestOrderTypeSalesForDay_SumConsistency(t *testing.T) {
	d := b8OpenDB(t, "order-type-sum.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "it", 0, nil, 1)
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())

	b8Sale(t, d, "s1", b8At(today), "completed", "sale", 0, 0)
	b8Line(t, d, "s1", 1, "it", "", "Item", 3, 0, 0, 600, 600)

	b8Sale(t, d, "s2", b8At(today.Add(time.Hour)), "completed", "sale", 0, 0)
	b8Line(t, d, "s2", 1, "it", "", "Item", 1, 0, 0, 1200, 1200)
	mustExec(t, d, `UPDATE sale_lines SET order_type = 'takeaway' WHERE id = 's2-l1'`)

	var wantGross int64
	if err := d.DB.QueryRowContext(ctx, `
SELECT COALESCE(SUM(sl.total_after_tax), 0)
FROM sale_lines sl JOIN sales s ON s.id = sl.sale_id
WHERE s.status = 'completed' AND s.sale_type = 'sale' AND date(s.created_at, 'localtime') = date(?)`,
		b8At(today)).Scan(&wantGross); err != nil {
		t.Fatalf("baseline total: %v", err)
	}
	if wantGross != 600+1200 {
		t.Fatalf("baseline sanity check failed: got %d", wantGross)
	}

	day := b8ExpectedDay(t, d, today, 0, 0)
	rows, err := repo.OrderTypeSalesForDay(ctx, day)
	if err != nil {
		t.Fatalf("OrderTypeSalesForDay: %v", err)
	}
	var sum money.Money
	for _, r := range rows {
		sum = sum.Add(r.Gross)
	}
	if want := money.FromMinor(wantGross); sum != want {
		t.Fatalf("sum(OrderTypeSales.Gross) = %v, want %v", sum, want)
	}
}

// TestEndOfDay_PopulatesOrderTypes_SingleDayOnly mirrors
// TestEndOfDay_PopulatesArticleBreakdowns_SingleDayOnly: OrderTypes
// populates under EndOfDay/EndOfDayRange(from==to), same gate as
// ArticleGroups/Articles/Operators, and stays empty on a multi-day range.
func TestEndOfDay_PopulatesOrderTypes_SingleDayOnly(t *testing.T) {
	d := b8OpenDB(t, "eod-order-type-wiring.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "it", 0, nil, 1)
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	b8Sale(t, d, "s1", b8At(today), "completed", "sale", 0, 0)
	b8Line(t, d, "s1", 1, "it", "", "Item", 1, 0, 0, 200, 200)

	day := b8ExpectedDay(t, d, today, 0, 0)
	rep, err := repo.EndOfDay(ctx, day)
	if err != nil {
		t.Fatalf("EndOfDay: %v", err)
	}
	if len(rep.OrderTypes) != 1 {
		t.Fatalf("EndOfDay (single day) want 1 order-type bucket, got %d: %+v", len(rep.OrderTypes), rep.OrderTypes)
	}

	rangeRep, err := repo.EndOfDayRange(ctx, day, day)
	if err != nil {
		t.Fatalf("EndOfDayRange (from==to): %v", err)
	}
	if len(rangeRep.OrderTypes) != 1 {
		t.Fatalf("EndOfDayRange with from==to should populate the same as EndOfDay, got %+v", rangeRep.OrderTypes)
	}

	yesterday := today.AddDate(0, 0, -1)
	yesterdayDay := b8ExpectedDay(t, d, yesterday, 0, 0)
	multiDayRep, err := repo.EndOfDayRange(ctx, yesterdayDay, day)
	if err != nil {
		t.Fatalf("EndOfDayRange (multi-day): %v", err)
	}
	if len(multiDayRep.OrderTypes) != 0 {
		t.Fatalf("EndOfDayRange (multi-day) must NOT populate OrderTypes (non-goal), got %+v", multiDayRep.OrderTypes)
	}
}

// TestEndOfDayInstant_PopulatesOrderTypes mirrors
// TestEndOfDayInstant_PopulatesArticleBreakdowns: internal/pages' generateEOD
// (the live "Run end-of-day" endpoint) calls EndOfDayInstant, never
// EndOfDay/dateRangeSummary — a breakdown wired only into dateRangeSummary
// would never appear on an actual day-close.
func TestEndOfDayInstant_PopulatesOrderTypes(t *testing.T) {
	d := b8OpenDB(t, "eod-order-type-instant.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "it", 0, nil, 1)
	from := iwAnchor()
	to := from.Add(time.Hour)
	b8Sale(t, d, "s1", b8At(from.Add(30*time.Minute)), "completed", "sale", 0, 0)
	b8Line(t, d, "s1", 1, "it", "", "Item", 1, 0, 0, 400, 400)
	mustExec(t, d, `UPDATE sale_lines SET order_type = 'takeaway' WHERE id = 's1-l1'`)
	// Outside the window entirely — must not leak into the breakdown.
	b8Sale(t, d, "s2", b8At(to.Add(time.Hour)), "completed", "sale", 0, 0)
	b8Line(t, d, "s2", 1, "it", "", "Item", 9, 0, 0, 1800, 1800)

	rep, err := repo.EndOfDayInstant(ctx, from, to)
	if err != nil {
		t.Fatalf("EndOfDayInstant: %v", err)
	}
	if len(rep.OrderTypes) != 1 || rep.OrderTypes[0].OrderType != "takeaway" || rep.OrderTypes[0].Gross != money.FromMinor(400) {
		t.Fatalf("OrderTypes via EndOfDayInstant = %+v, want 1 takeaway row / gross 400 (window-scoped, not s2's 1800)", rep.OrderTypes)
	}
}
