package data

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// InsertPayment/GetSaleDetail must round-trip the card-present reconciliation
// fields (masked PAN, auth code, terminal ID, trace ID) end to end
// (ut-docs#543 -- the data model has nowhere for a locally-attached card
// terminal, e.g. a future ZVT integration, to record what it charged).
// These fields are optional metadata on the payment row, same shape as
// tip_amount (see pos_repo_tip_amount_test.go): never folded into the sale
// total, never required for existing (non-card-present) payment methods.
func TestPOSRepo_CardPresentFields_RoundTrip(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "card_present.db"))
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
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-cp','R-CP-1','completed','sale','GBP',370,0,0,370,datetime('now'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('line-cp','sale-cp',1,'itm-coffee','Flat White',1,370,0,0,0,370,370)`)

	if err := repo.InsertPayment(ctx, nil, "pay-cp", "sale-cp", "card", 370, "GBP", "", 0, 0, "2026-08-16T10:00:00Z",
		CardPresentFields{MaskedPAN: "VISA •••• 4242", AuthCode: "013579", TerminalID: "TERM-01", TraceID: "TRACE-99"}); err != nil {
		t.Fatalf("insert payment: %v", err)
	}

	detail, ok, err := repo.GetSaleDetail(ctx, "R-CP-1")
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
	if p.MaskedPAN != "VISA •••• 4242" {
		t.Fatalf("want masked_pan %q, got %q", "VISA •••• 4242", p.MaskedPAN)
	}
	if p.AuthCode != "013579" {
		t.Fatalf("want auth_code 013579, got %q", p.AuthCode)
	}
	if p.TerminalID != "TERM-01" {
		t.Fatalf("want terminal_id TERM-01, got %q", p.TerminalID)
	}
	if p.TraceID != "TRACE-99" {
		t.Fatalf("want trace_id TRACE-99, got %q", p.TraceID)
	}
}

// A payment with no card-present data (the common case today -- cash, or
// any existing HTTP payment method) must default all four fields to "",
// not NULL surfacing as some other zero value or an error.
func TestPOSRepo_CardPresentFields_DefaultEmpty(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "card_present_empty.db"))
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
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-cash','R-CASH-1','completed','sale','GBP',250,0,0,250,datetime('now'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('line-cash','sale-cash',1,'itm-tea','Tea',1,250,0,0,0,250,250)`)

	if err := repo.InsertPayment(ctx, nil, "pay-cash", "sale-cash", "cash", 250, "GBP", "", 0, 0, "2026-08-16T10:00:00Z", CardPresentFields{}); err != nil {
		t.Fatalf("insert payment: %v", err)
	}

	detail, ok, err := repo.GetSaleDetail(ctx, "R-CASH-1")
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
	if p.MaskedPAN != "" || p.AuthCode != "" || p.TerminalID != "" || p.TraceID != "" {
		t.Fatalf("want all card-present fields empty for a cash payment, got %+v", p)
	}
}
