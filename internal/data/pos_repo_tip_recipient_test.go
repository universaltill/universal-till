package data

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// InsertPayment/GetSaleDetail must round-trip tip_recipient end to end
// (ADR-0060 Decision 3): whose money a tip is for tax purposes is recorded
// per payment at capture time, so a later report (ut-docs#964) reads what
// was actually decided then, never a recomputation against today's policy.
func TestPOSRepo_TipRecipient_RoundTrips(t *testing.T) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "tip_recipient.db"))
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
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-tiprec','R-TIPREC-1','completed','sale','GBP',370,0,0,370,datetime('now'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('line-tiprec','sale-tiprec',1,'itm-coffee','Flat White',1,370,0,0,0,370,370)`)

	if err := repo.InsertPayment(ctx, nil, "pay-tiprec", "sale-tiprec", "card", 420, "GBP", "", 0, 50, "business", "2026-08-24T10:00:00Z", CardPresentFields{}); err != nil {
		t.Fatalf("insert payment: %v", err)
	}

	detail, ok, err := repo.GetSaleDetail(ctx, "R-TIPREC-1")
	if err != nil {
		t.Fatalf("GetSaleDetail: %v", err)
	}
	if !ok {
		t.Fatal("expected sale to be found")
	}
	if len(detail.Payments) != 1 {
		t.Fatalf("want 1 payment, got %d", len(detail.Payments))
	}
	if got := detail.Payments[0].TipRecipient; got != "business" {
		t.Fatalf("want tip_recipient 'business', got %q", got)
	}
}

// A payment row written before migration 061 conceptually has no recipient;
// the column default (and the read-side COALESCE) must surface 'employee' —
// the one default every researched market agrees on — never empty.
func TestPOSRepo_TipRecipient_DefaultsToEmployee(t *testing.T) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "tip_recipient_default.db"))
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
	mustExec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-tea','TEA','Tea',250,1)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-legacy','R-LEGACY-1','completed','sale','GBP',250,0,0,250,datetime('now'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('line-legacy','sale-legacy',1,'itm-tea','Tea',1,250,0,0,0,250,250)`)
	// Insert WITHOUT naming tip_recipient — the pre-061 write shape — so the
	// column default is what fills it.
	mustExec(`INSERT INTO payments (id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at) VALUES ('pay-legacy','sale-legacy','cash',250,'GBP',0,0,'2026-08-24T10:00:00Z')`)

	detail, ok, err := repo.GetSaleDetail(ctx, "R-LEGACY-1")
	if err != nil {
		t.Fatalf("GetSaleDetail: %v", err)
	}
	if !ok {
		t.Fatal("expected sale to be found")
	}
	if len(detail.Payments) != 1 || detail.Payments[0].TipRecipient != "employee" {
		t.Fatalf("want tip_recipient 'employee' by default, got %+v", detail.Payments)
	}
}
