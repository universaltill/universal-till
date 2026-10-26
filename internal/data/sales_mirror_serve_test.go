package data

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// The main till's serve-side reads (ut-docs#5074 M2, design §11).

// Every batched read finds its rows through an index: a page of 200 sales
// must never scan sale_lines, payments or any other child table. A later
// migration that drops one of those indexes fails here.
func TestSalesMirrorServeReadsUseIndexes(t *testing.T) {
	d, err := db.Open(testsupport.MigratedDBFile(t, "mirror_plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for name, q := range map[string]string{
		"sales": mirrorLoadSalesSQL, "lines": mirrorLoadLinesSQL, "modifiers": mirrorLoadModifiersSQL,
		"payments": mirrorLoadPaymentsSQL, "charges": mirrorLoadChargesSQL, "discounts": mirrorLoadDiscountsSQL,
		"links": mirrorLoadLinksSQL, "closes": mirrorLoadClosesSQL,
	} {
		rows, err := d.DB.Query(`EXPLAIN QUERY PLAN `+fmt.Sprintf(q, "?,?,?"), "a", "b", "c")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		for _, p := range plan {
			if strings.HasPrefix(p, "SCAN ") && !strings.Contains(p, "USING TEMP") {
				t.Errorf("%s read scans a table: %q (plan %q)", name, p, plan)
			}
		}
	}
}

// LoadMirrorSales returns every copied field of a sale with all its
// children, the main till's own sale under the main till's id, and never a
// held sale; LoadMirrorCloses returns the archived report verbatim.
func TestSalesMirrorRepo_LoadMirrorSalesRoundTrip(t *testing.T) {
	d, err := db.Open(testsupport.MigratedDBFile(t, "mirror_load.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	for _, s := range []string{
		`INSERT INTO tills (id, name) VALUES ('till-2', 'Front')`,
		`INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES ('card', 'Card', 'card', 1)`,
		`INSERT INTO sales (id, receipt_no, display_no, status, sale_type, tender_type, currency, subtotal, discount_total, tax_total, total,
		   rounding, note, created_at, completed_at, till_id, service_charge_amount, order_type, table_id, tracking_token,
		   service_charge_tax_basis_bp, voucher_issue_total, local_date, offline, customer_id, cashier_id, register_id)
		 VALUES ('s1', 'T2-000000001', '7', 'completed', 'sale', 'card', 'GBP', 1000, 50, 167, 1010, 0, 'window seat',
		   '2026-10-26T10:00:00Z', '2026-10-26T10:00:05Z', 'till-2', 60, 'takeaway', NULL, 'secret-token', 0, 0, '2026-10-26', 1, NULL, NULL, NULL)`,
		`INSERT INTO sales (id, receipt_no, status, subtotal, total, local_date) VALUES ('s2', '000000002', 'completed', 5, 5, '2026-10-26')`,
		`INSERT INTO sales (id, receipt_no, status, subtotal, total, local_date) VALUES ('held', '000000003', 'active', 5, 5, '2026-10-26')`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-cut', 'CUT', 'Cut', 1000, 1)`,
		`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		 VALUES ('l1', 's1', 1, 'itm-cut', 'Cut', 1.5, 1000, 2000, 167, 833, 1000)`,
		`INSERT INTO sale_line_modifiers (id, sale_line_id, group_name_snapshot, option_name_snapshot, price_delta_minor) VALUES ('m1', 'l1', 'Style', 'Fade', 0)`,
		`INSERT INTO payments (id, sale_id, method_id, amount, currency, masked_pan, auth_code, terminal_id, trace_id, local_date)
		 VALUES ('p1', 's1', 'card', 1010, 'GBP', 'VISA **** 4242', 'A1', 'T9', 'TR', '2026-10-26')`,
		`INSERT INTO sale_charges (sale_id, seq, key, label, amount_minor) VALUES ('s1', 1, 'service', 'Service', 60)`,
		`INSERT INTO sale_discounts (id, sale_id, type, value, amount, reason) VALUES ('d1', 's1', 'fixed', 50, 50, 'loyal')`,
		`INSERT INTO sale_links (id, sale_id, original_sale_id, reason) VALUES ('k1', 's1', 's2', 'exchange')`,
		`INSERT INTO report_archive (id, kind, period, content_json, z_number) VALUES ('ra1', 'eod', '2026-10-26', '{"a": 1 }', 4)`,
	} {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	r := NewSalesMirrorRepo(d.DB)
	src := MirrorSource{MainTillID: "main-1", MainTillName: "Back counter"}
	got, err := r.LoadMirrorSales(ctx, []string{"s1", "s2", "held", "missing", "s1"}, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["held"] != nil {
		t.Fatalf("loaded %d sales (%v), want s1 and s2 only", len(got), got)
	}
	s := got["s1"]
	if s.OriginTillID != "till-2" || s.OriginTillName != "Front" || s.ReceiptNo != "T2-000000001" || *s.DisplayNo != "7" ||
		s.Total != 1010 || s.ServiceChargeAmount != 60 || *s.Note != "window seat" || !s.Offline || s.OrderType != "takeaway" {
		t.Fatalf("header = %+v", s)
	}
	if len(s.Lines) != 1 || s.Lines[0].Quantity != 1.5 || len(s.Lines[0].Modifiers) != 1 || s.Lines[0].Modifiers[0].OptionNameSnapshot != "Fade" {
		t.Fatalf("lines = %+v", s.Lines)
	}
	if len(s.Payments) != 1 || *s.Payments[0].MaskedPAN != "VISA **** 4242" || *s.Payments[0].TraceID != "TR" {
		t.Fatalf("payments = %+v", s.Payments)
	}
	if len(s.Charges) != 1 || len(s.Discounts) != 1 || len(s.Links) != 1 || s.Links[0].OriginalSaleID != "s2" {
		t.Fatalf("children: charges=%v discounts=%v links=%v", s.Charges, s.Discounts, s.Links)
	}
	if m := got["s2"]; m.OriginTillID != "main-1" || m.OriginTillName != "Back counter" {
		t.Fatalf("main till's own sale origin = %q/%q", m.OriginTillID, m.OriginTillName)
	}
	closes, err := r.LoadMirrorCloses(ctx, []string{"ra1"}, src)
	if err != nil || closes["ra1"] == nil || closes["ra1"].ContentJSON != `{"a": 1 }` || *closes["ra1"].ZNumber != 4 || closes["ra1"].OriginTillID != "main-1" {
		t.Fatalf("closes = %+v err=%v", closes, err)
	}
}

// The state re-sync's page read takes its order from the index: no
// temporary sort of the rest of the window on every page (M2 review
// finding 4).
func TestSalesMirrorStateReadUsesIndexOrder(t *testing.T) {
	d, err := db.Open(testsupport.MigratedDBFile(t, "mirror_state_plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rows, err := d.DB.Query(`EXPLAIN QUERY PLAN `+mirrorStateSalesSQL, "2026-01-01", "2026-01-01", -1, "t", 200)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		_ = rows.Scan(&id, &parent, &notused, &detail)
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, " | ")
	if strings.Contains(joined, "TEMP B-TREE") || !strings.Contains(joined, "idx_sales_local_date") {
		t.Fatalf("state read plan = %q, want idx_sales_local_date without a temp sort", joined)
	}
}
