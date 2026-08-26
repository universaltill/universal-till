package data

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// InsertPayment/GetSaleDetail must round-trip tip_amount end to end
// (docs/germany-pos-parity-backlog.md, "Tips: SumUp reader -> till
// auto-sync" -- the core domain model previously had zero tip concept, so
// a card terminal's tip had nowhere to persist). tip_amount is metadata
// on the payment row: it must NOT be folded into the sale total.
func TestPOSRepo_TipAmount_RoundTrips(t *testing.T) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "tip_amount.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	// 'card' payment method is already seeded by migration 001_init.sql.
	mustExec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-coffee','COFFEE','Flat White',370,1)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-tip','R-TIP-1','completed','sale','GBP',370,0,0,370,datetime('now'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('line-tip','sale-tip',1,'itm-coffee','Flat White',1,370,0,0,0,370,370)`)

	// 420 = 370 sale total + 50 tip, charged as a single card transaction --
	// the shape a SumUp reader's Cloud API transaction result would report.
	if err := repo.InsertPayment(ctx, nil, "pay-tip", "sale-tip", "card", 420, "GBP", "auth-ref-1", 0, 50, "employee", "", "2026-07-28T10:00:00Z", CardPresentFields{}); err != nil {
		t.Fatalf("insert payment: %v", err)
	}

	detail, ok, err := repo.GetSaleDetail(ctx, "R-TIP-1")
	if err != nil {
		t.Fatalf("GetSaleDetail: %v", err)
	}
	if !ok {
		t.Fatal("expected sale to be found")
	}
	if len(detail.Payments) != 1 {
		t.Fatalf("want 1 payment, got %d", len(detail.Payments))
	}
	p := detail.Payments[0]
	if p.TipAmount != 50 {
		t.Fatalf("want tip_amount 50, got %d", p.TipAmount)
	}
	if p.Amount != 420 {
		t.Fatalf("want amount 420 (sale total + tip), got %d", p.Amount)
	}
	if p.Reference != "auth-ref-1" {
		t.Fatalf("want reference auth-ref-1, got %q", p.Reference)
	}
	// Tip is metadata only -- it must never inflate the sale total itself
	// (tips are handled separately for payroll/tax, per the backlog note).
	if detail.Total != 370 {
		t.Fatalf("tip must not affect sale total; want 370, got %d", detail.Total)
	}
}

// A payment with no tip (the common case -- cash, or a card charge with no
// gratuity) must default tip_amount to 0, not NULL or an error.
func TestPOSRepo_TipAmount_DefaultsToZero(t *testing.T) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "tip_amount_zero.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	// 'cash' payment method is already seeded by migration 001_init.sql.
	mustExec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-tea','TEA','Tea',250,1)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-notip','R-NOTIP-1','completed','sale','GBP',250,0,0,250,datetime('now'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('line-notip','sale-notip',1,'itm-tea','Tea',1,250,0,0,0,250,250)`)

	if err := repo.InsertPayment(ctx, nil, "pay-notip", "sale-notip", "cash", 250, "GBP", "", 0, 0, "employee", "", "2026-07-28T10:00:00Z", CardPresentFields{}); err != nil {
		t.Fatalf("insert payment: %v", err)
	}

	detail, ok, err := repo.GetSaleDetail(ctx, "R-NOTIP-1")
	if err != nil {
		t.Fatalf("GetSaleDetail: %v", err)
	}
	if !ok {
		t.Fatal("expected sale to be found")
	}
	if len(detail.Payments) != 1 || detail.Payments[0].TipAmount != 0 {
		t.Fatalf("want tip_amount 0, got %+v", detail.Payments)
	}
}
