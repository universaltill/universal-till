package pages

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#5739: a sale that issued multi-purpose vouchers has their face
// value in TOTAL but (deliberately) in neither Subtotal nor Tax, so the
// receipt needs a "Vouchers issued" row for the rows to add up.

// seedVoucherTotalsSale records a completed, tax-exclusive GBP sale. itemMinor
// is the single item line (0 = voucher-only sale); voucherMinor is the sale's
// voucher_issue_total, which is part of total.
func seedVoucherTotalsSale(t *testing.T, dp *common.Deps, id, receiptNo string, itemMinor, voucherMinor int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, voucher_issue_total, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', ?, 0, 0, ?, ?, datetime('now'), datetime('now'))`, id, receiptNo, itemMinor, itemMinor+voucherMinor, voucherMinor); err != nil {
		t.Fatal(err)
	}
	if itemMinor > 0 {
		if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES(?, ?, 1, 'itm1', 'Apple', 'ABC', 1, ?, 0, 0, ?, ?)`, id+"-l1", id, itemMinor, itemMinor, itemMinor); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at) VALUES(?,?,'cash',?,'GBP',0,0,datetime('now'))`,
		id+"-p", id, itemMinor+voucherMinor); err != nil {
		t.Fatal(err)
	}
}

func totalsByLabel(doc print.Doc) map[string]string {
	m := map[string]string{}
	for _, kv := range doc.Totals {
		m[kv.Label] = kv.Amount
	}
	return m
}

func TestBuildReceiptDoc_VoucherOnlySaleShowsVouchersIssued_5739(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedVoucherTotalsSale(t, dp, "sv1", "R-V1", 0, 3500) // GBP 25 + GBP 10
	doc, err := buildReceiptDoc(context.Background(), dp, "R-V1")
	if err != nil {
		t.Fatal(err)
	}
	m := totalsByLabel(doc)
	if got := m["Vouchers issued"]; !strings.Contains(got, "35.00") {
		t.Fatalf("want a Vouchers issued row of 35.00, totals = %+v", doc.Totals)
	}
	if got := m["Subtotal"]; !strings.Contains(got, "0.00") || strings.Contains(got, "35") {
		t.Fatalf("subtotal must stay 0.00, totals = %+v", doc.Totals)
	}
	if got := m["TOTAL"]; !strings.Contains(got, "35.00") {
		t.Fatalf("TOTAL = %q, totals = %+v", got, doc.Totals)
	}
	// The voucher row sits immediately before TOTAL.
	vi, ti := -1, -1
	for i, kv := range doc.Totals {
		switch kv.Label {
		case "Vouchers issued":
			vi = i
		case "TOTAL":
			ti = i
		}
	}
	if vi < 0 || ti != vi+1 {
		t.Fatalf("Vouchers issued must be immediately before TOTAL: %+v", doc.Totals)
	}
}

func TestBuildReceiptDoc_MixedItemAndVoucherRowsAddUp_5739(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedVoucherTotalsSale(t, dp, "sv2", "R-V2", 1200, 2500)
	doc, err := buildReceiptDoc(context.Background(), dp, "R-V2")
	if err != nil {
		t.Fatal(err)
	}
	m := totalsByLabel(doc)
	want := map[string]string{"Subtotal": "12.00", "Vouchers issued": "25.00", "TOTAL": "37.00"}
	for k, v := range want {
		if !strings.Contains(m[k], v) {
			t.Fatalf("%s = %q, want it to contain %s; totals = %+v", k, m[k], v, doc.Totals)
		}
	}
	// 12.00 + 0.00 tax + 25.00 = 37.00
}

func TestBuildReceiptDoc_NoVoucherRowWithoutVouchers_5739(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "sv3", "R-V3", "sale", "", 120, 0, 0)
	doc, err := buildReceiptDoc(context.Background(), dp, "R-V3")
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range doc.Totals {
		if kv.Label == "Vouchers issued" {
			t.Fatalf("no voucher issued, yet a Vouchers issued row printed: %+v", doc.Totals)
		}
	}
}

func voucherTotalsFuncs() map[string]any {
	return map[string]any{
		"money":         func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
		"qty":           func(v any) string { return fmt.Sprintf("%v", v) },
		"barcodesvg":    httpx.BarcodeSVG,
		"tenderLabel":   func(s string) string { return s },
		"barcode128svg": httpx.Code128SVG,
		"bpPercent":     func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
		"T":             func(key string) string { return key },
	}
}

func TestRenderReceipt_TotalsShowVouchersIssued_5739(t *testing.T) {
	chdirRoot(t)
	funcs := voucherTotalsFuncs()
	payments := []pos.PaymentInput{{MethodID: "cash", Amount: 4000}}
	issued := []receiptVoucherIssue{
		{Code: "GS-1", Amount: 2500},
		{Code: "GS-2", Amount: 1000},
		// A single-purpose voucher is taxed into the subtotal, not a liability.
		{Code: "GS-3", Amount: 700, SinglePurpose: true},
		// A top-up is always multi-purpose, so it IS counted (25+10+5 = 40).
		{Code: "GS-4", Amount: 500, TopUp: true},
	}
	render := func(design receiptDesign, vs []receiptVoucherIssue) string {
		html, err := renderReceipt(funcs, "123", nil, payments, 0, 0, 4000, false, 0, "", 0, nil, false, false, false, false, nil, nil, "My Store", design, "", vs, "", nil, nil, "", "en", 0)
		if err != nil {
			t.Fatal(err)
		}
		i := strings.Index(html, `class="totals"`)
		if i < 0 {
			t.Fatalf("no totals block: %s", html)
		}
		return html[i:]
	}
	totals := func(html string) string {
		// the totals block ends at the first payments/voucher section
		if j := strings.Index(html, "receipt-payments"); j > 0 {
			return html[:j]
		}
		return html
	}
	on := totals(render(receiptDesign{ShowTax: true}, issued))
	vi := strings.Index(on, "receipt.vouchers.issued")
	ti := strings.Index(on, "basket.total")
	if vi < 0 || ti < vi || !strings.Contains(on[vi:ti], "$40.00") {
		t.Fatalf("want 'Vouchers issued: $35.00' (multi-purpose + top-up) before the total, got: %s", on)
	}
	if strings.Contains(on[vi:ti], "$47.00") {
		t.Fatalf("single-purpose voucher must not be summed in: %s", on)
	}
	if off := totals(render(receiptDesign{ShowTax: false}, issued)); strings.Contains(off, "receipt.vouchers.issued") {
		t.Fatalf("ShowTax off must not print the row: %s", off)
	}
	if none := totals(render(receiptDesign{ShowTax: true}, nil)); strings.Contains(none, "receipt.vouchers.issued") {
		t.Fatalf("no vouchers, no row: %s", none)
	}
	if single := totals(render(receiptDesign{ShowTax: true}, issued[2:3])); strings.Contains(single, "receipt.vouchers.issued") {
		t.Fatalf("single-purpose only, no liability row: %s", single)
	}
}

// The tender handler builds each receipt entry with newReceiptVoucherIssue;
// a single-purpose voucher must be flagged so it is not counted as a liability.
func TestReceiptVoucherIssueTotal_SinglePurposeNotCounted_5739(t *testing.T) {
	multi := newReceiptVoucherIssue(pos.VoucherIssueInput{VoucherID: "GS-1", Amount: money.FromMinor(2500), Purpose: data.VoucherPurposeMulti})
	blank := newReceiptVoucherIssue(pos.VoucherIssueInput{VoucherID: "GS-2", Amount: money.FromMinor(1000)})
	single := newReceiptVoucherIssue(pos.VoucherIssueInput{VoucherID: "GS-3", Amount: money.FromMinor(700), Purpose: data.VoucherPurposeSingle})
	if multi.SinglePurpose || blank.SinglePurpose || !single.SinglePurpose {
		t.Fatalf("SinglePurpose flags wrong: %+v %+v %+v", multi, blank, single)
	}
	topUp := receiptVoucherIssue{Code: "GS-4", Amount: 500, TopUp: true}
	if got := receiptVoucherIssueTotal([]receiptVoucherIssue{multi, blank, single, topUp}); got != 4000 {
		t.Fatalf("total = %d, want 4000 (single-purpose excluded, top-up included)", got)
	}
	if got := receiptVoucherIssueTotal([]receiptVoucherIssue{single}); got != 0 {
		t.Fatalf("single-purpose only = %d, want 0", got)
	}
}
