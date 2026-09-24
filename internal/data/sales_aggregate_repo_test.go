package data

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#2535: the till-side producer for ADR-0111's per-(business
// date, till) sales rollup. These tests pin every bucket the cloudsync
// orchestration turns into the upload DTO, against a fixture spanning two
// tills and two days with a refund, a void, a discount, a cash sale with
// change, a card sale with a tip and two VAT rates.

// saSale inserts a sale row with the provenance columns the rollup keys on
// (till_id / register_id / cashier_id). local_date and voided_local_date
// are derived from the same literal the way InsertSale/UpdateSaleStatus do.
func saSale(t *testing.T, d *db.DB, id string, at time.Time, status, saleType, tillID, registerID, cashierID string, subtotal, discount, tax, total int64) {
	t.Helper()
	var reg, cashier any
	if registerID != "" {
		reg = registerID
	}
	if cashierID != "" {
		cashier = cashierID
	}
	ts := b8At(at)
	var voidedAt any
	if status == "voided" {
		voidedAt = ts
	}
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total,
  created_at, local_date, till_id, register_id, cashier_id, voided_at, voided_local_date)
VALUES (?, ?, ?, ?, 'GBP', ?, ?, ?, ?, ?, date(?, 'localtime'), ?, ?, ?, ?, date(?, 'localtime'))`,
		id, "R-"+id, status, saleType, subtotal, discount, tax, total, ts, ts, tillID, reg, cashier, voidedAt, voidedAt)
}

func saPay(t *testing.T, d *db.DB, id, saleID, method string, amount, change, tip int64) {
	t.Helper()
	mustExec(t, d, `INSERT INTO payments (id, sale_id, method_id, amount, currency, change_given, tip_amount) VALUES (?, ?, ?, ?, 'GBP', ?, ?)`,
		id, saleID, method, amount, change, tip)
}

type saFixture struct {
	d                *db.DB
	day, prevDay     string
	today, yesterday time.Time
}

func seedSalesAggregateFixture(t *testing.T) saFixture {
	t.Helper()
	d := b8OpenDB(t, "sales-aggregate.db")
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	yesterday := today.AddDate(0, 0, -1)

	mustExec(t, d, `INSERT INTO registers (id, name) VALUES ('reg-A', 'Front'), ('reg-B', 'Back')`)
	mustExec(t, d, `INSERT INTO users (id, username, display_name) VALUES ('u1', 'alice', 'Alice Example'), ('u2', 'bob', 'Bob Example')`)
	// payment_methods 'cash' and 'card' are seeded by 001_init.sql.
	b8Item(t, d, "item-1", 1190, nil, 1)
	b8Item(t, d, "item-2", 535, nil, 1)

	// ── today, register reg-A (till_id '' — this till's own sales) ──
	// s1 12:00 u1: 2x item-1 @19% (net 1000, tax 190), cash 2000 − 810 change.
	saSale(t, d, "s1", today, "completed", "sale", "", "reg-A", "u1", 1000, 0, 190, 1190)
	b8Line(t, d, "s1", 1, "item-1", "", "Item 1", 2, 1900, 190, 1000, 1190)
	mustExec(t, d, `INSERT INTO sale_line_modifiers (id, sale_line_id, option_id, group_name_snapshot, option_name_snapshot, price_delta_minor)
VALUES ('m1', 's1-l1', 'opt-large', 'Size', 'Large', 0)`)
	saPay(t, d, "p1", "s1", "cash", 2000, 810, 0)
	// s2 14:00 u1: 1x item-2 @7% (net 500, tax 35), card 535 + 50 tip.
	saSale(t, d, "s2", today.Add(2*time.Hour), "completed", "sale", "", "reg-A", "u1", 500, 0, 35, 535)
	b8Line(t, d, "s2", 1, "item-2", "", "Item 2", 1, 700, 35, 500, 535)
	saPay(t, d, "p2", "s2", "card", 585, 0, 50)
	// s3 13:00 u2: 1x item-1 @19% with a 100 line discount, card 1071.
	saSale(t, d, "s3", today.Add(time.Hour), "completed", "sale", "", "reg-A", "u2", 900, 0, 171, 1071)
	b8Line(t, d, "s3", 1, "item-1", "", "Item 1", 1, 1900, 171, 900, 1071)
	b8Discount(t, d, "disc-s3", "s3", "s3-l1", "line_discount", 100)
	saPay(t, d, "p3", "s3", "card", 1071, 0, 0)
	// r1 15:00 u1: refund of item-2, 535 cash out.
	saSale(t, d, "r1", today.Add(3*time.Hour), "completed", "return", "", "reg-A", "u1", 500, 0, 35, 535)
	b8Line(t, d, "r1", 1, "item-2", "", "Item 2", 1, 700, 35, 500, 535)
	saPay(t, d, "pr1", "r1", "cash", 535, 0, 0)
	// v1 16:00 u2: voided sale (a Storno) — no revenue, counted as a void.
	saSale(t, d, "v1", today.Add(4*time.Hour), "voided", "sale", "", "reg-A", "u2", 300, 0, 0, 300)

	// ── today, a replica's journaled sale (till_id wins over register_id) ──
	saSale(t, d, "s4", today, "completed", "sale", "replica-1", "reg-B", "u2", 1000, 0, 190, 1190)
	b8Line(t, d, "s4", 1, "item-1", "", "Item 1", 1, 1900, 190, 1000, 1190)
	saPay(t, d, "p4", "s4", "card", 1190, 0, 0)

	// ── yesterday: reg-B, and one sale with no till/register at all ──
	saSale(t, d, "s5", yesterday, "completed", "sale", "", "reg-B", "u1", 500, 0, 35, 535)
	b8Line(t, d, "s5", 1, "item-2", "", "Item 2", 1, 700, 35, 500, 535)
	saPay(t, d, "p5", "s5", "cash", 535, 0, 0)
	saSale(t, d, "s6", yesterday, "completed", "sale", "", "", "", 100, 0, 0, 100)
	b8Line(t, d, "s6", 1, "item-2", "", "Item 2", 1, 0, 0, 100, 100)

	return saFixture{
		d: d, today: today, yesterday: yesterday,
		day:     b8ExpectedDay(t, d, today, 0, 0),
		prevDay: b8ExpectedDay(t, d, yesterday, 0, 0),
	}
}

func TestSalesAggregateKeys_PerDateAndResolvedTill(t *testing.T) {
	f := seedSalesAggregateFixture(t)
	repo := NewPOSRepo(f.d.DB)
	keys, err := repo.SalesAggregateKeys(context.Background(), f.prevDay, f.day, "self-till")
	if err != nil {
		t.Fatalf("SalesAggregateKeys: %v", err)
	}
	want := []SalesAggregateKey{
		{BusinessDate: f.prevDay, TillID: "reg-B"},
		{BusinessDate: f.prevDay, TillID: "self-till"},
		{BusinessDate: f.day, TillID: "reg-A"},
		{BusinessDate: f.day, TillID: "replica-1"},
	}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %+v\nwant %+v", keys, want)
	}
	// Window bound: only today.
	keys, err = repo.SalesAggregateKeys(context.Background(), f.day, f.day, "self-till")
	if err != nil || len(keys) != 2 {
		t.Fatalf("today-only keys = %+v, err %v", keys, err)
	}
}

func TestSalesAggregateForTill_Buckets(t *testing.T) {
	f := seedSalesAggregateFixture(t)
	repo := NewPOSRepo(f.d.DB)
	ctx := context.Background()

	agg, err := repo.SalesAggregateForTill(ctx, f.day, "reg-A", "self-till", false)
	if err != nil {
		t.Fatalf("SalesAggregateForTill: %v", err)
	}
	h := func(tm time.Time) int { return b8ExpectedSlot(t, f.d, "%H", tm, 0, 0) }
	wantHourly := []SalesAggregateHour{
		{Hour: h(f.today), Net: money.FromMinor(1190), Count: 1},
		{Hour: h(f.today.Add(time.Hour)), Net: money.FromMinor(1071), Count: 1},
		{Hour: h(f.today.Add(2 * time.Hour)), Net: money.FromMinor(535), Count: 1},
	}
	sort.Slice(wantHourly, func(i, j int) bool { return wantHourly[i].Hour < wantHourly[j].Hour })
	if !reflect.DeepEqual(agg.Hourly, wantHourly) {
		t.Fatalf("hourly = %+v\nwant %+v", agg.Hourly, wantHourly)
	}

	wantPay := []SalesAggregatePayment{
		// card: 585 (535 + 50 tip) + 1071; tips 50; no expected cash.
		{Method: "card", Amount: money.FromMinor(1656), Tips: money.FromMinor(50), ExpectedCash: 0},
		// cash: 2000 − 810 change − 535 refunded = 655, all of it expected in the drawer.
		{Method: "cash", Amount: money.FromMinor(655), Tips: 0, ExpectedCash: money.FromMinor(655)},
	}
	if !reflect.DeepEqual(agg.Payments, wantPay) {
		t.Fatalf("payments = %+v\nwant %+v", agg.Payments, wantPay)
	}

	wantItems := []SalesAggregateItem{
		{ItemID: "item-1", Qty: 3, TopModifierID: "opt-large"},
		{ItemID: "item-2", Qty: 1},
	}
	if !reflect.DeepEqual(agg.Items, wantItems) {
		t.Fatalf("items = %+v\nwant %+v", agg.Items, wantItems)
	}

	if agg.RefundCount != 1 || agg.VoidCount != 1 || agg.DiscountCount != 1 {
		t.Fatalf("counts refund=%d void=%d discount=%d, want 1/1/1", agg.RefundCount, agg.VoidCount, agg.DiscountCount)
	}
	if agg.Cashiers != nil {
		t.Fatalf("cashier breakdown must not be read when off: %+v", agg.Cashiers)
	}

	agg, err = repo.SalesAggregateForTill(ctx, f.day, "reg-A", "self-till", true)
	if err != nil {
		t.Fatalf("SalesAggregateForTill(cashiers): %v", err)
	}
	wantCashiers := []SalesAggregateCashier{
		{StaffID: "u1", Net: money.FromMinor(1725), Count: 2, ItemQty: 3, Refunds: 1, Voids: 0, Discounts: 0},
		{StaffID: "u2", Net: money.FromMinor(1071), Count: 1, ItemQty: 1, Refunds: 0, Voids: 1, Discounts: 1},
	}
	if !reflect.DeepEqual(agg.Cashiers, wantCashiers) {
		t.Fatalf("cashiers = %+v\nwant %+v", agg.Cashiers, wantCashiers)
	}

	// The replica's sale is its own till, not reg-A's.
	rep, err := repo.SalesAggregateForTill(ctx, f.day, "replica-1", "self-till", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Hourly) != 1 || rep.Hourly[0].Net != money.FromMinor(1190) || rep.RefundCount != 0 || rep.VoidCount != 0 {
		t.Fatalf("replica-1 aggregate = %+v", rep)
	}
	// The till-less sale falls back to this till's own identity.
	self, err := repo.SalesAggregateForTill(ctx, f.prevDay, "self-till", "self-till", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(self.Hourly) != 1 || self.Hourly[0].Net != money.FromMinor(100) {
		t.Fatalf("self-till aggregate = %+v", self)
	}
}

func TestSalesForTaxBandsForTill_ScopedToTill(t *testing.T) {
	f := seedSalesAggregateFixture(t)
	repo := NewPOSRepo(f.d.DB)
	ctx := context.Background()

	ids := func(sales []EODTaxBandSale) []string {
		var out []string
		for _, s := range sales {
			out = append(out, s.ID)
		}
		sort.Strings(out)
		return out
	}
	regA, err := repo.SalesForTaxBandsForTill(ctx, f.day, "reg-A", "self-till")
	if err != nil {
		t.Fatalf("SalesForTaxBandsForTill: %v", err)
	}
	if got := ids(regA); !reflect.DeepEqual(got, []string{"r1", "s1", "s2", "s3"}) {
		t.Fatalf("reg-A band sales = %v", got)
	}
	for _, s := range regA {
		if len(s.Lines) != 1 || len(s.Payments) != 1 {
			t.Fatalf("sale %s lines/payments not attached: %+v", s.ID, s)
		}
	}
	rep, err := repo.SalesForTaxBandsForTill(ctx, f.day, "replica-1", "self-till")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(rep); !reflect.DeepEqual(got, []string{"s4"}) {
		t.Fatalf("replica-1 band sales = %v", got)
	}
	// Whole-day (unscoped) read is unchanged by the refactor.
	all, err := repo.SalesForTaxBands(ctx, f.day, f.day)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(all); !reflect.DeepEqual(got, []string{"r1", "s1", "s2", "s3", "s4"}) {
		t.Fatalf("all band sales = %v", got)
	}
}

func TestSalesAggregateUploads_HashUpsertPrune(t *testing.T) {
	d := b8OpenDB(t, "sales-aggregate-uploads.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	at := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

	if h, ok, err := repo.SalesAggregateUploadHash(ctx, "2026-09-24", "reg-A"); err != nil || ok || h != "" {
		t.Fatalf("empty lookup = %q %v %v", h, ok, err)
	}
	if err := repo.RecordSalesAggregateUpload(ctx, "2026-09-24", "reg-A", "h1", at); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := repo.RecordSalesAggregateUpload(ctx, "2026-09-24", "reg-A", "h2", at); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if h, ok, err := repo.SalesAggregateUploadHash(ctx, "2026-09-24", "reg-A"); err != nil || !ok || h != "h2" {
		t.Fatalf("after upsert = %q %v %v", h, ok, err)
	}
	if err := repo.RecordSalesAggregateUpload(ctx, "2026-09-01", "reg-A", "old", at); err != nil {
		t.Fatal(err)
	}
	n, err := repo.PruneSalesAggregateUploads(ctx, "2026-09-11")
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1", n, err)
	}
	if _, ok, _ := repo.SalesAggregateUploadHash(ctx, "2026-09-01", "reg-A"); ok {
		t.Fatalf("old row survived prune")
	}
	if _, ok, _ := repo.SalesAggregateUploadHash(ctx, "2026-09-24", "reg-A"); !ok {
		t.Fatalf("in-window row pruned")
	}
}

// Review finding (2535): expected_cash_minor is the Z-report's drawer
// figure, which holds cash tips out (CashReconciliation.CashSales,
// ut-docs#1046) — tips travel separately in tips_minor.
func TestSalesAggregateForTill_ExpectedCashHoldsTipsOut(t *testing.T) {
	d := b8OpenDB(t, "sales-aggregate-cash-tip.db")
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local)
	b8Item(t, d, "item-1", 1000, nil, 1)
	saSale(t, d, "s1", at, "completed", "sale", "", "", "", 1000, 0, 0, 1000)
	b8Line(t, d, "s1", 1, "item-1", "", "Item 1", 1, 1000, 0, 1000, 1000)
	saPay(t, d, "p1", "s1", "cash", 1100, 0, 100)
	agg, err := NewPOSRepo(d.DB).SalesAggregateForTill(ctx, at.Format("2006-01-02"), "self", "self", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(agg.Payments) != 1 {
		t.Fatalf("payments = %+v", agg.Payments)
	}
	p := agg.Payments[0]
	if p.Amount.Minor() != 1100 || p.Tips.Minor() != 100 || p.ExpectedCash.Minor() != 1000 {
		t.Fatalf("cash bucket = amount %d tips %d expected %d, want 1100/100/1000", p.Amount.Minor(), p.Tips.Minor(), p.ExpectedCash.Minor())
	}
}
