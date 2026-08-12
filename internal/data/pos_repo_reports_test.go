package data

import (
	"context"
	"testing"
	"time"
)

// TopItems, PeriodComparison, and PaymentBreakdown are the last three
// pos_repo.go functions with no coverage anywhere in the codebase (checked
// via combined coverage across internal/data, internal/pos, and
// internal/pages — everything else in this file is now exercised at least
// indirectly). All three window on datetime('now', '-N days'), so — same
// lesson as the AuditActionSummary test in batch 2 — timestamps must be
// relative to time.Now(), not hardcoded literals.

func relDays(d int) string { return time.Now().AddDate(0, 0, d).UTC().Format(time.RFC3339) }

func TestTopItems(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO items(id,sku,name,base_price,is_active,is_weighed,unit) VALUES('itm1','SKU1','Apple',100,1,0,'each')`); err != nil {
		t.Fatal(err)
	}
	seedLifecycleSale(t, dbx, "sale1", "R1", "sale", "completed", relDays(-1), 200, 0)
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line1','sale1',1,'itm1','Apple','SKU1',3,100,0,0,300,300)`); err != nil {
		t.Fatal(err)
	}
	// Outside the 7-day window — must not be counted.
	seedLifecycleSale(t, dbx, "sale2", "R2", "sale", "completed", relDays(-30), 100, 0)
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line2','sale2',1,'itm1','Apple','SKU1',99,100,0,0,9900,9900)`); err != nil {
		t.Fatal(err)
	}

	start, end := lastDays(7)
	items, err := dbx.repo.TopItems(ctx, start, end, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected only the in-window sale line aggregated, got %+v", items)
	}
	if items[0].Name != "Apple" || items[0].Qty != 3 || items[0].Revenue != 300 {
		t.Fatalf("unexpected top item: %+v", items[0])
	}
}

func TestPeriodComparison(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	// Current period: 2 days ago, within the last 7 days.
	seedLifecycleSale(t, dbx, "sale-current", "R1", "sale", "completed", relDays(-2), 500, 0)
	// Regression: a sale from EARLIER TODAY (before "now", same calendar
	// day) must also land in the current period. PeriodComparison's upper
	// bound is `created_at < datetime('now', '+0 days')` — created_at is
	// stored as RFC3339 ("...THH:MM:SSZ") while datetime('now', ...)
	// produces SQLite's own format ("... HH:MM:SS", space, no suffix). On
	// the SAME calendar day this compared as a raw string, not a real date,
	// and 'T' (0x54) sorts lexically AFTER ' ' (0x20) — so ANY same-day
	// created_at, regardless of actual time of day, string-compared as NOT
	// less than "now", silently dropping every sale from today out of the
	// current-period bucket. Confirmed live via sqlite3 CLI 2026-07-29.
	seedLifecycleSale(t, dbx, "sale-earlier-today", "R5", "sale", "completed", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), 40, 0)
	// A year-ago-equivalent sale: 367 days ago falls inside the
	// [-(days+365), -365) window when days=7 (i.e. between 372 and 365 days
	// ago).
	seedLifecycleSale(t, dbx, "sale-year-ago", "R2", "sale", "completed", relDays(-367), 300, 0)
	// Neither period: 30 days ago.
	seedLifecycleSale(t, dbx, "sale-neither", "R3", "sale", "completed", relDays(-30), 999, 0)
	// A return in the current window must not count (PeriodComparison
	// filters sale_type='sale').
	seedLifecycleSale(t, dbx, "return-current", "R4", "return", "completed", relDays(-1), 50, 0)

	start, end := lastDays(7)
	current, yearAgo, err := dbx.repo.PeriodComparison(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if current.Count != 2 || current.Total != 540 {
		t.Fatalf("expected current period = 2 sales (2-days-ago + earlier-today) totalling 540, got %+v", current)
	}
	if yearAgo.Count != 1 || yearAgo.Total != 300 {
		t.Fatalf("expected year-ago period = 1 sale totalling 300, got %+v", yearAgo)
	}
}

func TestPaymentBreakdown(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	seedLifecycleSale(t, dbx, "sale1", "R1", "sale", "completed", relDays(-1), 220, 20)
	seedLifecycleSale(t, dbx, "sale2", "R2", "sale", "completed", relDays(-2), 110, 10)
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES
('pay1','sale1','cash',220,'GBP',20,?),
('pay2','sale2','cash',110,'GBP',0,?)`, relDays(-1), relDays(-2)); err != nil {
		t.Fatal(err)
	}
	// Outside the window.
	seedLifecycleSale(t, dbx, "sale3", "R3", "sale", "completed", relDays(-30), 500, 0)
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay3','sale3','cash',500,'GBP',0,?)`, relDays(-30)); err != nil {
		t.Fatal(err)
	}

	start, end := lastDays(7)
	breakdown, err := dbx.repo.PaymentBreakdown(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(breakdown) != 1 {
		t.Fatalf("expected only cash within the window, got %+v", breakdown)
	}
	if breakdown[0].Method != "cash" || breakdown[0].Count != 2 {
		t.Fatalf("expected 2 cash payments, got %+v", breakdown[0])
	}
	// (220-20) + (110-0) = 310 -- amount minus change given, matching a
	// cashier's actual net cash-drawer movement.
	if breakdown[0].Amount != 310 {
		t.Fatalf("expected net amount 310 (amount - change_given), got %d", breakdown[0].Amount)
	}
}
