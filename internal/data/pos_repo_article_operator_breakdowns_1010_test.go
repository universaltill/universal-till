package data

// ut-docs#1010: day-close gains per-article-group, per-article and
// per-operator breakdowns alongside the existing per-department/per-till
// ones. ArticleGroupsForDay groups by an item's IMMEDIATE category (not
// rolled up to the root department, unlike DepartmentsForDay/dept_roots);
// ArticleSalesForDay is every sold article for the day (no LIMIT, grouped by
// sl.name_snapshot — the same key TopItems uses); OperatorSalesForDay groups
// by sales.cashier_id with a resolved display name. All three derive Net/
// Gross from sale_lines (total_before_tax/total_after_tax), the same
// convention export_repo.go's Net/Tax DTOs already use, so their Gross sums
// reconcile with DepartmentsForDay/ArticleSalesForDay's own totals (see the
// sum-consistency test below) — never sales.total, which can include
// service-charge/rounding the article-level figures deliberately exclude.
//
// Uses role-based test identifiers only ("cashier-a", "manager-b") per
// CLAUDE.md — never a real/personal name in test fixtures.

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
)

// b10User inserts a users row directly (role-based test identifiers only).
func b10User(t *testing.T, d *db.DB, id, username, displayName string) {
	t.Helper()
	mustExec(t, d, `INSERT INTO users (id, username, display_name, role, is_active) VALUES (?, ?, ?, 'cashier', 1)`,
		id, username, displayName)
}

// b10Category inserts a categories row.
func b10Category(t *testing.T, d *db.DB, id, name, parentID string) {
	t.Helper()
	var parent any
	if parentID != "" {
		parent = parentID
	}
	mustExec(t, d, `INSERT INTO categories (id, name, parent_id) VALUES (?, ?, ?)`, id, name, parent)
}

// b10ItemWithCategory inserts an item with an explicit category_id (b8Item
// leaves category_id NULL, which every other helper in this file needs to
// stay unset).
func b10ItemWithCategory(t *testing.T, d *db.DB, id, categoryID string) {
	t.Helper()
	var cat any
	if categoryID != "" {
		cat = categoryID
	}
	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price, category_id, is_active) VALUES (?, ?, ?, 0, ?, 1)`,
		id, "SKU-"+id, "Name "+id, cat)
}

// b10SetCashier stamps a sale's cashier_id after the fact — b8Sale has no
// cashier parameter, and adding one would touch every existing b8Sale call
// site for no reason.
func b10SetCashier(t *testing.T, d *db.DB, saleID, cashierID string) {
	t.Helper()
	var cid any
	if cashierID != "" {
		cid = cashierID
	}
	mustExec(t, d, `UPDATE sales SET cashier_id = ? WHERE id = ?`, cid, saleID)
}

func TestArticleGroupsForDay_GroupsByImmediateCategoryNotRoot(t *testing.T) {
	d := b8OpenDB(t, "article-groups.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	// Electronics (root) > Phones (child). A DEPARTMENT report would roll
	// both items up under "Electronics"; ArticleGroupsForDay must NOT do
	// that — it groups by the item's own immediate category.
	b10Category(t, d, "electronics", "Electronics", "")
	b10Category(t, d, "phones", "Phones", "electronics")
	b10ItemWithCategory(t, d, "phone", "phones")
	b10ItemWithCategory(t, d, "laptop", "electronics")

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	b8Sale(t, d, "s1", b8At(today), "completed", "sale", 0, 0)
	b8Line(t, d, "s1", 1, "phone", "", "Phone", 1, 0, 0, 50000, 50000)
	b8Line(t, d, "s1", 2, "laptop", "", "Laptop", 1, 0, 0, 120000, 120000)

	day := b8ExpectedDay(t, d, today, 0, 0)
	rows, err := repo.ArticleGroupsForDay(ctx, day)
	if err != nil {
		t.Fatalf("ArticleGroupsForDay: %v", err)
	}
	got := map[string]ArticleGroupSales{}
	for _, r := range rows {
		got[r.Group] = r
	}
	if len(got) != 2 {
		t.Fatalf("want 2 immediate-category groups (Phones, Electronics), got %+v", rows)
	}
	if g := got["Phones"]; g.Net != money.FromMinor(50000) || g.Gross != money.FromMinor(50000) || g.Qty != 1 {
		t.Fatalf("Phones = %+v, want net/gross 50000 qty 1 (must NOT roll up into Electronics)", g)
	}
	if g := got["Electronics"]; g.Net != money.FromMinor(120000) || g.Gross != money.FromMinor(120000) || g.Qty != 1 {
		t.Fatalf("Electronics = %+v, want net/gross 120000 qty 1 (only the laptop, directly categorized)", g)
	}
}

func TestArticleSalesForDay_AllArticlesNoLimit(t *testing.T) {
	d := b8OpenDB(t, "article-sales.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	b8Item(t, d, "it", 0, nil, 1)
	b8Sale(t, d, "s1", b8At(today), "completed", "sale", 0, 0)
	// 15 distinct articles — well past any plausible TopItems-style LIMIT —
	// to prove ArticleSalesForDay returns every one, not a top-N slice.
	const n = 15
	for i := 0; i < n; i++ {
		name := "Article-" + string(rune('A'+i))
		b8Line(t, d, "s1", i+1, "it", "", name, 1, 0, 0, 100, 100)
	}

	day := b8ExpectedDay(t, d, today, 0, 0)
	rows, err := repo.ArticleSalesForDay(ctx, day)
	if err != nil {
		t.Fatalf("ArticleSalesForDay: %v", err)
	}
	if len(rows) != n {
		t.Fatalf("want all %d articles, got %d: %+v", n, len(rows), rows)
	}
	for _, r := range rows {
		if r.Net != money.FromMinor(100) || r.Gross != money.FromMinor(100) || r.Qty != 1 {
			t.Fatalf("article %+v, want net/gross 100 qty 1", r)
		}
	}
}

func TestOperatorSalesForDay_GroupsByCashierWithDisplayName(t *testing.T) {
	d := b8OpenDB(t, "operator-sales.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b10User(t, d, "cashier-a", "cashier.a", "Cashier A")
	// cashier-b has no display name set at all in this scenario — falls
	// back through username, matching workerAllocationDisplayNames'
	// fallback (reports_page.go): DisplayName if non-empty, else Username.
	mustExec(t, d, `INSERT INTO users (id, username, display_name, role, is_active) VALUES ('cashier-b', 'cashier.b', '', 'cashier', 1)`)

	b8Item(t, d, "it", 0, nil, 1)
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())

	b8Sale(t, d, "sa1", b8At(today), "completed", "sale", 0, 0)
	b8Line(t, d, "sa1", 1, "it", "", "Item", 1, 0, 0, 1000, 1000)
	b10SetCashier(t, d, "sa1", "cashier-a")

	b8Sale(t, d, "sa2", b8At(today.Add(time.Hour)), "completed", "sale", 0, 0)
	b8Line(t, d, "sa2", 1, "it", "", "Item", 1, 0, 0, 500, 500)
	b10SetCashier(t, d, "sa2", "cashier-a")

	b8Sale(t, d, "sb1", b8At(today), "completed", "sale", 0, 0)
	b8Line(t, d, "sb1", 1, "it", "", "Item", 2, 0, 0, 2000, 2000)
	b10SetCashier(t, d, "sb1", "cashier-b")

	day := b8ExpectedDay(t, d, today, 0, 0)
	rows, err := repo.OperatorSalesForDay(ctx, day)
	if err != nil {
		t.Fatalf("OperatorSalesForDay: %v", err)
	}
	got := map[string]OperatorSales{}
	for _, r := range rows {
		got[r.CashierID] = r
	}
	if len(got) != 2 {
		t.Fatalf("want 2 operators, got %+v", rows)
	}
	a := got["cashier-a"]
	if a.DisplayName != "Cashier A" || a.Count != 2 || a.Net != money.FromMinor(1500) || a.Gross != money.FromMinor(1500) {
		t.Fatalf("cashier-a = %+v, want display 'Cashier A' count 2 net/gross 1500", a)
	}
	b := got["cashier-b"]
	if b.DisplayName != "cashier.b" || b.Count != 1 || b.Net != money.FromMinor(2000) || b.Gross != money.FromMinor(2000) {
		t.Fatalf("cashier-b = %+v, want display 'cashier.b' (no display_name set) count 1 net/gross 2000", b)
	}
}

// TestOperatorSalesForDay_AttributionSurvivesManagerElevation is the ut-docs#1010
// dual-attribution acceptance criterion: a sale rung by a cashier, with a
// manager-override elevation audited on/around it (InsertAuditElevated —
// internal/pages/elevation.go's checkOrElevate flow, ADR unchanged here),
// must still attribute the sale's revenue to the CASHIER who rang it, never
// to the approving manager. sales.cashier_id is the sole attribution column
// this method reads; audit_log's actor_id/blocked_actor_id record who
// approved/was-blocked for a DIFFERENT purpose (the audit trail) and must
// never leak into revenue attribution.
func TestOperatorSalesForDay_AttributionSurvivesManagerElevation(t *testing.T) {
	d := b8OpenDB(t, "operator-elevation.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b10User(t, d, "cashier-a", "cashier.a", "Cashier A")
	b10User(t, d, "manager-b", "manager.b", "Manager B")
	b8Item(t, d, "it", 0, nil, 1)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	b8Sale(t, d, "sale-elevated", b8At(today), "completed", "sale", 0, 0)
	b8Line(t, d, "sale-elevated", 1, "it", "", "Item", 1, 0, 0, 5000, 5000)
	b10SetCashier(t, d, "sale-elevated", "cashier-a")

	// A manager-override elevation happened on this sale: the approving
	// manager (actorID) is manager-b, the originally-blocked session user
	// (blockedActorID) is cashier-a — exactly checkOrElevate's shape for an
	// elevated action (e.g. a discount or a void needing manager PIN) taken
	// on this sale.
	if err := repo.InsertAuditElevated(ctx, nil, "manager-b", "cashier-a",
		"sale", "sale-elevated", "discount_override", map[string]any{"reason": "test"},
		b8At(today), ""); err != nil {
		t.Fatalf("InsertAuditElevated: %v", err)
	}

	day := b8ExpectedDay(t, d, today, 0, 0)
	rows, err := repo.OperatorSalesForDay(ctx, day)
	if err != nil {
		t.Fatalf("OperatorSalesForDay: %v", err)
	}
	got := map[string]OperatorSales{}
	for _, r := range rows {
		got[r.CashierID] = r
	}
	if _, ok := got["manager-b"]; ok {
		t.Fatalf("manager-b must NOT be attributed any revenue from an elevation it only approved: %+v", rows)
	}
	a, ok := got["cashier-a"]
	if !ok || a.Net != money.FromMinor(5000) || a.Gross != money.FromMinor(5000) || a.Count != 1 {
		t.Fatalf("cashier-a must still be attributed the sale's full revenue despite the elevation: %+v", rows)
	}
}

// TestEODArticleBreakdowns_SumConsistency is the ut-docs#1010 sum-consistency
// acceptance criterion: for a day with multiple groups/articles/operators,
// the three breakdowns' Gross sums must all reconcile with each other AND
// with the day's total article revenue (independently computed here the same
// way DepartmentsForDay's own revenue figure is: SUM(sale_lines.total_after_tax)
// over completed sales that day) — proving no breakdown silently drops or
// double-counts a line relative to the others.
func TestEODArticleBreakdowns_SumConsistency(t *testing.T) {
	d := b8OpenDB(t, "eod-breakdown-sum.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b10Category(t, d, "grocery", "Grocery", "")
	b10Category(t, d, "electronics", "Electronics", "")
	b10ItemWithCategory(t, d, "milk", "grocery")
	b10ItemWithCategory(t, d, "phone", "electronics")
	b10User(t, d, "cashier-a", "cashier.a", "Cashier A")
	b10User(t, d, "cashier-b", "cashier.b", "Cashier B")

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())

	b8Sale(t, d, "s1", b8At(today), "completed", "sale", 0, 0)
	b8Line(t, d, "s1", 1, "milk", "", "Milk", 3, 0, 0, 600, 600)
	b10SetCashier(t, d, "s1", "cashier-a")

	b8Sale(t, d, "s2", b8At(today.Add(time.Hour)), "completed", "sale", 0, 0)
	b8Line(t, d, "s2", 1, "phone", "", "Phone", 1, 0, 0, 50000, 50000)
	b10SetCashier(t, d, "s2", "cashier-b")

	b8Sale(t, d, "s3", b8At(today.Add(2*time.Hour)), "completed", "sale", 0, 0)
	b8Line(t, d, "s3", 1, "milk", "", "Milk", 1, 0, 0, 200, 200)
	b8Line(t, d, "s3", 2, "phone", "", "Phone", 1, 0, 0, 45000, 45000)
	b10SetCashier(t, d, "s3", "cashier-a")

	// Independent baseline: total article revenue for the day, computed the
	// same way DepartmentsForDay computes its own per-row revenue (SUM over
	// sale_lines.total_after_tax for completed sales that day) — NOT via
	// any of the three methods under test, so it's a genuine cross-check.
	var wantGross int64
	if err := d.DB.QueryRowContext(ctx, `
SELECT COALESCE(SUM(sl.total_after_tax), 0)
FROM sale_lines sl JOIN sales s ON s.id = sl.sale_id
WHERE s.status = 'completed' AND s.sale_type = 'sale' AND date(s.created_at, 'localtime') = date(?)`,
		b8At(today)).Scan(&wantGross); err != nil {
		t.Fatalf("baseline total: %v", err)
	}
	if wantGross != 600+50000+200+45000 {
		t.Fatalf("baseline sanity check failed: got %d", wantGross)
	}

	day := b8ExpectedDay(t, d, today, 0, 0)
	groups, err := repo.ArticleGroupsForDay(ctx, day)
	if err != nil {
		t.Fatalf("ArticleGroupsForDay: %v", err)
	}
	articles, err := repo.ArticleSalesForDay(ctx, day)
	if err != nil {
		t.Fatalf("ArticleSalesForDay: %v", err)
	}
	operators, err := repo.OperatorSalesForDay(ctx, day)
	if err != nil {
		t.Fatalf("OperatorSalesForDay: %v", err)
	}
	if len(groups) != 2 || len(articles) != 2 || len(operators) != 2 {
		t.Fatalf("want 2 groups, 2 articles, 2 operators; got %d/%d/%d", len(groups), len(articles), len(operators))
	}

	var groupSum, articleSum, operatorSum money.Money
	for _, g := range groups {
		groupSum = groupSum.Add(g.Gross)
	}
	for _, a := range articles {
		articleSum = articleSum.Add(a.Gross)
	}
	for _, o := range operators {
		operatorSum = operatorSum.Add(o.Gross)
	}
	want := money.FromMinor(wantGross)
	if groupSum != want {
		t.Fatalf("sum(ArticleGroupSales.Gross) = %v, want %v", groupSum, want)
	}
	if articleSum != want {
		t.Fatalf("sum(ArticleSales.Gross) = %v, want %v", articleSum, want)
	}
	if operatorSum != want {
		t.Fatalf("sum(OperatorSales.Gross) = %v, want %v", operatorSum, want)
	}
}

// TestEndOfDay_PopulatesArticleBreakdowns_SingleDayOnly proves the EODReport
// wiring: ArticleGroups/Articles/Operators populate under EndOfDay (from ==
// to, same gate Departments/Tills already use) and stay empty on a
// multi-day EndOfDayRange — explicit non-goal, symmetric with the existing
// gated breakdowns.
func TestEndOfDay_PopulatesArticleBreakdowns_SingleDayOnly(t *testing.T) {
	d := b8OpenDB(t, "eod-wiring.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b10Category(t, d, "grocery", "Grocery", "")
	b10ItemWithCategory(t, d, "milk", "grocery")
	b10User(t, d, "cashier-a", "cashier.a", "Cashier A")

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	b8Sale(t, d, "s1", b8At(today), "completed", "sale", 0, 0)
	b8Line(t, d, "s1", 1, "milk", "", "Milk", 1, 0, 0, 200, 200)
	b10SetCashier(t, d, "s1", "cashier-a")

	day := b8ExpectedDay(t, d, today, 0, 0)
	rep, err := repo.EndOfDay(ctx, day)
	if err != nil {
		t.Fatalf("EndOfDay: %v", err)
	}
	if len(rep.ArticleGroups) != 1 || len(rep.Articles) != 1 || len(rep.Operators) != 1 {
		t.Fatalf("EndOfDay (single day) want 1/1/1 breakdown rows, got %d/%d/%d",
			len(rep.ArticleGroups), len(rep.Articles), len(rep.Operators))
	}

	rangeRep, err := repo.EndOfDayRange(ctx, day, day)
	if err != nil {
		t.Fatalf("EndOfDayRange (from==to): %v", err)
	}
	if len(rangeRep.ArticleGroups) != 1 {
		t.Fatalf("EndOfDayRange with from==to should populate the same as EndOfDay, got %+v", rangeRep.ArticleGroups)
	}

	yesterday := today.AddDate(0, 0, -1)
	yesterdayDay := b8ExpectedDay(t, d, yesterday, 0, 0)
	multiDayRep, err := repo.EndOfDayRange(ctx, yesterdayDay, day)
	if err != nil {
		t.Fatalf("EndOfDayRange (multi-day): %v", err)
	}
	if len(multiDayRep.ArticleGroups) != 0 || len(multiDayRep.Articles) != 0 || len(multiDayRep.Operators) != 0 {
		t.Fatalf("EndOfDayRange (multi-day) must NOT populate the new breakdowns (non-goal), got %d/%d/%d",
			len(multiDayRep.ArticleGroups), len(multiDayRep.Articles), len(multiDayRep.Operators))
	}
}

// TestEndOfDayInstant_PopulatesArticleBreakdowns is the acceptance criterion
// that actually matters for real shops: internal/pages' generateEOD (the
// live "Run end-of-day" endpoint) calls EndOfDayInstant → dateRangeSummaryInstant,
// NEVER EndOfDay/dateRangeSummary — EndOfDay is only reachable from the
// scheduler tick's own day-boundary path and EndOfDayRange's ad-hoc
// multi-day download. A first pass of this feature wired the three new
// breakdowns into dateRangeSummary only, which would have shipped a card
// marked Done whose "BY ARTICLE GROUP"/"BY ARTICLE"/"BY OPERATOR" sections
// never once appeared on an actual day-close. This test would have caught
// that: it exercises the SAME close-to-close path generateEOD uses (see
// eod_instant_window_test.go's iwAnchor()/half-open-window conventions).
func TestEndOfDayInstant_PopulatesArticleBreakdowns(t *testing.T) {
	d := b8OpenDB(t, "eod-instant-breakdowns.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b10Category(t, d, "grocery", "Grocery", "")
	b10ItemWithCategory(t, d, "milk", "grocery")
	b10User(t, d, "cashier-a", "cashier.a", "Cashier A")

	from := iwAnchor()
	to := from.Add(time.Hour)
	b8Sale(t, d, "s1", b8At(from.Add(30*time.Minute)), "completed", "sale", 0, 0)
	b8Line(t, d, "s1", 1, "milk", "", "Milk", 2, 0, 0, 400, 400)
	b10SetCashier(t, d, "s1", "cashier-a")
	// Outside the window entirely — must not leak into any breakdown.
	b8Sale(t, d, "s2", b8At(to.Add(time.Hour)), "completed", "sale", 0, 0)
	b8Line(t, d, "s2", 1, "milk", "", "Milk", 9, 0, 0, 1800, 1800)
	b10SetCashier(t, d, "s2", "cashier-a")

	rep, err := repo.EndOfDayInstant(ctx, from, to)
	if err != nil {
		t.Fatalf("EndOfDayInstant: %v", err)
	}
	if len(rep.ArticleGroups) != 1 || rep.ArticleGroups[0].Gross != money.FromMinor(400) {
		t.Fatalf("ArticleGroups via EndOfDayInstant = %+v, want 1 row / gross 400 (window-scoped, not s2's 1800)", rep.ArticleGroups)
	}
	if len(rep.Articles) != 1 || rep.Articles[0].Gross != money.FromMinor(400) {
		t.Fatalf("Articles via EndOfDayInstant = %+v, want 1 row / gross 400", rep.Articles)
	}
	if len(rep.Operators) != 1 || rep.Operators[0].CashierID != "cashier-a" || rep.Operators[0].Gross != money.FromMinor(400) {
		t.Fatalf("Operators via EndOfDayInstant = %+v, want cashier-a / gross 400", rep.Operators)
	}
}
