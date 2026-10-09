package data

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// receiptsBusinessDay is sales.receipts.v1's window: the business date that
// "now" falls in (shifted back by the business-day start, like
// sales.by_day.v1's buckets), minus days_ago, as a half-open [from, to)
// from that date's start to the next one's.
func TestReceiptsBusinessDay(t *testing.T) {
	loc := time.FixedZone("shop", 2*3600)
	at := func(y int, mo time.Month, d, h, mi int) time.Time { return time.Date(y, mo, d, h, mi, 0, 0, loc) }
	cases := []struct {
		name     string
		now      time.Time
		hh, mm   int
		daysAgo  int
		wantDate string
		wantFrom time.Time
	}{
		{"midnight start", at(2026, 10, 9, 10, 0), 0, 0, 0, "2026-10-09", at(2026, 10, 9, 0, 0)},
		{"midnight start, just after midnight", at(2026, 10, 9, 0, 0), 0, 0, 0, "2026-10-09", at(2026, 10, 9, 0, 0)},
		{"04:00 start, before 04:00 is yesterday", at(2026, 10, 9, 3, 59), 4, 0, 0, "2026-10-08", at(2026, 10, 8, 4, 0)},
		{"04:00 start, at 04:00 is today", at(2026, 10, 9, 4, 0), 4, 0, 0, "2026-10-09", at(2026, 10, 9, 4, 0)},
		{"04:00 start, days_ago 3", at(2026, 10, 9, 12, 0), 4, 0, 3, "2026-10-06", at(2026, 10, 6, 4, 0)},
		{"06:30 start crosses a month", at(2026, 10, 1, 6, 0), 6, 30, 1, "2026-09-29", at(2026, 9, 29, 6, 30)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			date, from, to := receiptsBusinessDay(c.now, c.hh, c.mm, c.daysAgo)
			if date != c.wantDate || !from.Equal(c.wantFrom) || !to.Equal(c.wantFrom.AddDate(0, 0, 1)) {
				t.Fatalf("receiptsBusinessDay = %s [%s, %s), want %s [%s, %s)", date, from, to,
					c.wantDate, c.wantFrom, c.wantFrom.AddDate(0, 0, 1))
			}
		})
	}
}

// sales.receipts.v1 (ADR-0149 §6, ut-docs#3976): one business date's
// completed receipts — sales and returns — with lines, applied payments,
// tip, currency and whether the receipt was fiscally signed. Never a card
// detail.
func TestSalesReceiptsView(t *testing.T) {
	dbo, err := db.Open(testsupport.MigratedDBFile(t, "receipts-view.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()

	prev := coreViewNow
	coreViewNow = func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.Local) }
	t.Cleanup(func() { coreViewNow = prev })
	if err := NewSettingsRepo(dbo.DB).Set(ctx, "reports.business_day_start", "04:00"); err != nil {
		t.Fatal(err)
	}
	// stored is created_at as the till stores it: UTC, "YYYY-MM-DD HH:MM:SS".
	stored := func(d, h, mi, s int) string {
		return time.Date(2026, 10, d, h, mi, s, 0, time.Local).UTC().Format("2006-01-02 15:04:05")
	}

	mustExec(t, dbo, `INSERT INTO users(id, username, display_name, role, pin_hash) VALUES('u1', 'u1', 'Sam', 'cashier', 'h')`)
	mustExec(t, dbo, `INSERT INTO items(id, sku, name, base_price) VALUES('i1', 'HC', 'Haircut', 800)`)
	mustExec(t, dbo, `INSERT INTO item_variants(id, item_id, name, price) VALUES('v1', 'i1', 'Shampoo 250ml', 200)`)
	sale := func(id, receipt, status, saleType, cashier, createdAt string, sub, disc, tax, total int64) {
		t.Helper()
		var c any
		if cashier != "" {
			c = cashier
		}
		mustExec(t, dbo, `INSERT INTO sales(id, receipt_no, status, sale_type, till_id, cashier_id, currency,
			subtotal, discount_total, tax_total, total, created_at) VALUES(?, ?, ?, ?, 'till-a', ?, 'GBP', ?, ?, ?, ?, ?)`,
			id, receipt, status, saleType, c, sub, disc, tax, total, createdAt)
	}
	sale("s-before", "R-0", "completed", "sale", "u1", stored(9, 3, 59, 59), 100, 0, 0, 100) // previous business day
	sale("s1", "R-1", "completed", "sale", "u1", stored(9, 4, 0, 0), 1100, 100, 150, 1150)   // first second of the day
	sale("s2", "R-2", "completed", "return", "", stored(9, 9, 0, 0), -300, 0, -50, -300)
	sale("s3", "R-3", "voided", "sale", "u1", stored(9, 10, 0, 0), 500, 0, 0, 500)
	sale("s4", "R-4", "held", "sale", "u1", stored(9, 10, 30, 0), 500, 0, 0, 500)
	sale("s5", "R-5", "completed", "sale", "u1", stored(9, 11, 0, 0), 0, 0, 0, 0)
	sale("s-after", "R-6", "completed", "sale", "u1", stored(10, 4, 0, 0), 100, 0, 0, 100) // next business day

	mustExec(t, dbo, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, variant_id, name_snapshot, sku_snapshot,
		quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES
		('l2', 's1', 2, NULL, 'v1', 'Shampoo 250ml', NULL, 1.5, 200, 0, 2000, 50, 250, 300),
		('l1', 's1', 1, 'i1', NULL, 'Haircut', 'HC', 1, 800, 100, 2000, 100, 700, 800),
		('l3', 's2', 1, 'i1', NULL, 'Haircut', 'HC', -1, 300, 0, 2000, -50, -250, -300),
		('l0', 's-before', 1, 'i1', NULL, 'Haircut', 'HC', 1, 100, 0, 0, 0, 100, 100)`)
	mustExec(t, dbo, `INSERT INTO payments(id, sale_id, method_id, amount, change_given, tip_amount,
		masked_pan, auth_code, terminal_id, trace_id, reference, voucher_id) VALUES
		('p1', 's1', 'card', 1000, 0, 200, '************4242', 'AUTHCODE9', 'TERMID7', 'TRACEID5', 'REFXYZ', NULL),
		('p2', 's1', 'cash', 500, 150, 50, NULL, NULL, NULL, NULL, NULL, 'VOUCHERID3'),
		('p3', 's2', 'cash', -300, 0, 0, NULL, NULL, NULL, NULL, NULL, NULL),
		('p0', 's-before', 'cash', 100, 0, 999, NULL, NULL, NULL, NULL, NULL, NULL)`)
	mustExec(t, dbo, `INSERT INTO fiscal_tse_signatures(sale_id, signature) VALUES('s1', 'sig')`)
	mustExec(t, dbo, `INSERT INTO fiscal_receipt_evidence(sale_id, qr_payload) VALUES('s2', 'qr')`)

	v, ok := LookupCoreView("sales.receipts.v1")
	if !ok {
		t.Fatal("sales.receipts.v1 not registered")
	}
	if v.Permission != "view:sales" {
		t.Fatalf("permission = %q, want view:sales", v.Permission)
	}
	args, err := v.ParseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if args["days_ago"] != 0 || args["offset"] != 0 || args["limit"] != 50 {
		t.Fatalf("default args = %v, want days_ago 0, offset 0, limit 50", args)
	}
	out, err := RunCoreView(ctx, dbo.DB, v, args, CoreViewMaxResult)
	if err != nil {
		t.Fatal(err)
	}
	s1 := `{"id":"s1","receipt_no":"R-1","sale_type":"sale","business_date":"2026-10-09","created_at":"` + stored(9, 4, 0, 0) + `",` +
		`"till_id":"till-a","cashier_id":"u1","currency":"GBP","subtotal_minor":1100,"discount_minor":100,"tax_minor":150,"total_minor":1150,` +
		`"tip_minor":250,"fiscal_signed":true,"lines":[` +
		`{"line_no":1,"item_id":"i1","variant_id":"","name":"Haircut","sku":"HC","quantity":1,"unit_price_minor":800,"discount_minor":100,"tax_minor":100,"total_minor":800},` +
		`{"line_no":2,"item_id":"","variant_id":"v1","name":"Shampoo 250ml","sku":"","quantity":1.5,"unit_price_minor":200,"discount_minor":0,"tax_minor":50,"total_minor":300}],` +
		`"payments":[{"method_id":"card","amount_minor":1000,"tip_minor":200},{"method_id":"cash","amount_minor":350,"tip_minor":50}]}`
	s2 := `{"id":"s2","receipt_no":"R-2","sale_type":"return","business_date":"2026-10-09","created_at":"` + stored(9, 9, 0, 0) + `",` +
		`"till_id":"till-a","cashier_id":"","currency":"GBP","subtotal_minor":-300,"discount_minor":0,"tax_minor":-50,"total_minor":-300,` +
		`"tip_minor":0,"fiscal_signed":true,"lines":[` +
		`{"line_no":1,"item_id":"i1","variant_id":"","name":"Haircut","sku":"HC","quantity":-1,"unit_price_minor":300,"discount_minor":0,"tax_minor":-50,"total_minor":-300}],` +
		`"payments":[{"method_id":"cash","amount_minor":-300,"tip_minor":0}]}`
	s5 := `{"id":"s5","receipt_no":"R-5","sale_type":"sale","business_date":"2026-10-09","created_at":"` + stored(9, 11, 0, 0) + `",` +
		`"till_id":"till-a","cashier_id":"u1","currency":"GBP","subtotal_minor":0,"discount_minor":0,"tax_minor":0,"total_minor":0,` +
		`"tip_minor":0,"fiscal_signed":false,"lines":[],"payments":[]}`
	if want := "[" + s1 + "," + s2 + "," + s5 + "]"; string(out) != want {
		t.Fatalf("result:\n got  %s\n want %s", out, want)
	}
	for _, secret := range []string{"4242", "AUTHCODE9", "TERMID7", "TRACEID5", "REFXYZ", "VOUCHERID3",
		"masked_pan", "auth_code", "terminal_id", "trace_id", "reference", "voucher_id", "customer_id"} {
		if bytes.Contains(out, []byte(secret)) {
			t.Errorf("sales.receipts.v1 leaks %q", secret)
		}
	}

	run := func(args map[string]int) []ReceiptViewRow {
		t.Helper()
		out, err := RunCoreView(ctx, dbo.DB, v, args, CoreViewMaxResult)
		if err != nil {
			t.Fatal(err)
		}
		var rows []ReceiptViewRow
		if err := json.Unmarshal(out, &rows); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
		return rows
	}
	ids := func(rows []ReceiptViewRow) string {
		s := ""
		for _, r := range rows {
			s += r.ID + " "
		}
		return s
	}

	// Paging walks the same order.
	if got := ids(run(map[string]int{"days_ago": 0, "offset": 1, "limit": 1})); got != "s2 " {
		t.Fatalf("offset 1 limit 1 = %s, want s2", got)
	}
	if got := ids(run(map[string]int{"days_ago": 0, "offset": 2, "limit": 50})); got != "s5 " {
		t.Fatalf("offset 2 = %s, want s5", got)
	}

	// The second before the day start belongs to the previous business date.
	prevDay := run(map[string]int{"days_ago": 1, "offset": 0, "limit": 50})
	if len(prevDay) != 1 || prevDay[0].ID != "s-before" || prevDay[0].BusinessDate != "2026-10-08" ||
		prevDay[0].TipMinor != 999 || prevDay[0].FiscalSigned || len(prevDay[0].Lines) != 1 {
		t.Fatalf("days_ago 1 = %+v, want only s-before on 2026-10-08", prevDay)
	}

	// A business date with no receipts is [].
	out, err = RunCoreView(ctx, dbo.DB, v, map[string]int{"days_ago": 31, "offset": 0, "limit": 50}, CoreViewMaxResult)
	if err != nil || string(out) != "[]" {
		t.Fatalf("empty day = %s, %v; want []", out, err)
	}

	// Oldest first across both created_at forms (Fable review, ut-docs#3976):
	// the sale path writes RFC3339 "…T…Z", the schema default and older rows
	// "YYYY-MM-DD HH:MM:SS". A text ORDER BY would put every space-form row
	// first ('T' > ' '), so 10:00 would sort after 11:00.
	rfc := time.Date(2026, 10, 9, 10, 0, 0, 0, time.Local).UTC().Format(time.RFC3339)
	sale("s-rfc", "R-7", "completed", "sale", "u1", rfc, 0, 0, 0, 0)
	if got := ids(run(map[string]int{"days_ago": 0, "offset": 0, "limit": 50})); got != "s1 s2 s-rfc s5 " {
		t.Fatalf("mixed created_at forms = %s, want s1 s2 s-rfc s5 (oldest first)", got)
	}
}

func TestSalesReceiptsViewArgs(t *testing.T) {
	v, _ := LookupCoreView("sales.receipts.v1")
	for _, raw := range []string{`{"days_ago":31,"offset":1000000,"limit":100}`, `{"days_ago":0,"offset":0,"limit":1}`} {
		if _, err := v.ParseArgs([]byte(raw)); err != nil {
			t.Errorf("ParseArgs(%s): %v", raw, err)
		}
	}
	for _, raw := range []string{`{"days_ago":-1}`, `{"days_ago":32}`, `{"offset":-1}`, `{"offset":1000001}`,
		`{"limit":0}`, `{"limit":101}`, `{"days":7}`, `{"days_ago":"1"}`, `{"days_ago":1.5}`} {
		if _, err := v.ParseArgs([]byte(raw)); err == nil {
			t.Errorf("ParseArgs(%s) accepted, want an error", raw)
		}
	}
}
