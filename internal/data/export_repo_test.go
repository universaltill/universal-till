package data

import (
	"context"
	"fmt"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// exportTestItemID is a single items row shared by every seeded sale line in
// this file — sale_lines has a CHECK requiring exactly one of item_id/
// variant_id to be set (see 001_init.sql), so every line needs a real item.
const exportTestItemID = "itm-export"

func seedExportTestItem(t *testing.T, dbx *posTestDB) {
	t.Helper()
	if _, err := dbx.d.DB.ExecContext(context.Background(),
		`INSERT OR IGNORE INTO items(id, sku, name, base_price, is_active, is_weighed, unit) VALUES(?, 'SKU-EXP', 'Widget', 1000, 1, 0, 'each')`,
		exportTestItemID); err != nil {
		t.Fatalf("seed export test item: %v", err)
	}
}

// seedExportSale inserts a completed sale plus one sale_line (single tax
// rate) and one payment, mirroring seedLifecycleSale (pos_repo_lifecycle_test.go)
// but extended with the sale_lines/payments rows SalesForExport needs.
func seedExportSale(t *testing.T, dbx *posTestDB, id, receiptNo, saleType, createdAt string, netMinor, taxMinor int64, taxRateBP int, method string, paidMinor int64) {
	t.Helper()
	ctx := context.Background()
	total := netMinor + taxMinor
	seedLifecycleSale(t, dbx, id, receiptNo, saleType, "completed", createdAt, total, taxMinor)
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES(?, ?, 1, ?, 'Widget', 1, ?, ?, ?, ?, ?)`,
		id+"-line1", id, exportTestItemID, netMinor, taxRateBP, taxMinor, netMinor, total); err != nil {
		t.Fatalf("seed sale line for %s: %v", id, err)
	}
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at)
VALUES(?, ?, ?, ?, 'GBP', 0, ?)`, id+"-pay1", id, method, paidMinor, createdAt); err != nil {
		t.Fatalf("seed payment for %s: %v", id, err)
	}
}

func TestSalesForExport(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	seedExportTestItem(t, dbx)

	// A single-band, single-payment sale (the common case).
	seedExportSale(t, dbx, "sale1", "R1", "sale", "2026-06-15T09:00:00Z", 1000, 200, 2000, "cash", 1200)
	// A second sale, same day, different rate — proves GROUP BY tax_rate_bp
	// and per-sale isolation (not aggregated across sales).
	seedExportSale(t, dbx, "sale2", "R2", "sale", "2026-06-15T14:00:00Z", 500, 0, 0, "card", 500)
	// On the boundary day (last day of range, late in the day) — must be
	// included: the whole final day is in range.
	seedExportSale(t, dbx, "sale-boundary", "R3", "sale", "2026-06-30T23:59:59Z", 300, 60, 2000, "cash", 360)
	// Before the range — must be excluded.
	seedExportSale(t, dbx, "sale-before", "R4", "sale", "2026-06-14T23:59:59Z", 900, 0, 0, "cash", 900)
	// After the range — must be excluded.
	seedExportSale(t, dbx, "sale-after", "R5", "sale", "2026-07-01T00:00:00Z", 900, 0, 0, "cash", 900)
	// A return in range — excluded (this dataset is sales only, matching
	// PaymentBreakdown/busyBuckets' sale_type='sale' precedent).
	seedExportSale(t, dbx, "sale-return", "R6", "return", "2026-06-15T10:00:00Z", 100, 20, 2000, "cash", 120)

	got, err := dbx.repo.SalesForExport(ctx, "2026-06-15", "2026-06-30")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 sales in range, got %d: %+v", len(got), got)
	}

	if got[0].ReceiptNo != "R1" {
		t.Fatalf("expected R1 first (ordered by created_at), got %+v", got[0])
	}
	if got[0].Total != money.FromMinor(1200) {
		t.Fatalf("expected total 1200, got %d", got[0].Total.Minor())
	}
	if len(got[0].TaxLines) != 1 || got[0].TaxLines[0].RateBP != 2000 ||
		got[0].TaxLines[0].Net != money.FromMinor(1000) || got[0].TaxLines[0].Tax != money.FromMinor(200) {
		t.Fatalf("unexpected tax lines for R1: %+v", got[0].TaxLines)
	}
	if len(got[0].Payments) != 1 || got[0].Payments[0].Method != "cash" || got[0].Payments[0].Amount != money.FromMinor(1200) {
		t.Fatalf("unexpected payments for R1: %+v", got[0].Payments)
	}

	if got[1].ReceiptNo != "R2" || len(got[1].TaxLines) != 1 || got[1].TaxLines[0].RateBP != 0 {
		t.Fatalf("unexpected R2 row: %+v", got[1])
	}
	if got[1].Payments[0].Method != "card" {
		t.Fatalf("expected card payment for R2, got %+v", got[1].Payments)
	}

	if got[2].ReceiptNo != "R3" {
		t.Fatalf("expected the boundary-day sale included, got %+v", got[2])
	}
}

// TestSalesForExport_GroupsBandsAndMethods is the aggregation regression the
// single-line/single-payment fixtures above cannot catch: every sale there
// has exactly one sale_line and one payment, so dropping either GROUP BY
// clause from exportSaleTaxLines/exportSalePayments still passes. A real
// fiscal export stands or falls on per-band and per-method totals, so this
// seeds one sale with *two* lines sharing a rate plus a third at another
// rate, and two payments sharing a method plus a third on another method —
// and one of those payments hands back change, proving the amounts are net
// of change_given (the PaymentBreakdown convention) rather than gross tender.
func TestSalesForExport_GroupsBandsAndMethods(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	seedExportTestItem(t, dbx)

	// net 1000+2000 @2000bp (tax 200+400) + net 500 @500bp (tax 25) = 4125.
	seedLifecycleSale(t, dbx, "multi", "R10", "sale", "completed", "2026-06-15T09:00:00Z", 4125, 625)
	lines := []struct {
		no                         int
		net, rateBP, tax, afterTax int64
	}{
		{1, 1000, 2000, 200, 1200},
		{2, 2000, 2000, 400, 2400},
		{3, 500, 500, 25, 525},
	}
	for _, l := range lines {
		if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES(?, 'multi', ?, ?, 'Widget', 1, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("multi-line%d", l.no), l.no, exportTestItemID, l.net, l.rateBP, l.tax, l.net, l.afterTax); err != nil {
			t.Fatalf("seed line %d: %v", l.no, err)
		}
	}
	// cash 2000 tendered with 500 change back (net 1500) + cash 1000 => 2500
	// on "cash"; card 1625 => 1625 on "card". Total applied 4125.
	pays := []struct {
		id, method          string
		amount, changeGiven int64
	}{
		{"multi-pay1", "cash", 2000, 500},
		{"multi-pay2", "cash", 1000, 0},
		{"multi-pay3", "card", 1625, 0},
	}
	for _, p := range pays {
		if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at)
VALUES(?, 'multi', ?, ?, 'GBP', ?, '2026-06-15T09:00:00Z')`, p.id, p.method, p.amount, p.changeGiven); err != nil {
			t.Fatalf("seed payment %s: %v", p.id, err)
		}
	}

	got, err := dbx.repo.SalesForExport(ctx, "2026-06-15", "2026-06-15")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected the one seeded sale, got %+v", got)
	}

	// Two distinct bands, ORDER BY tax_rate_bp DESC, each summed across its
	// own lines: 2000bp => net 3000 / tax 600; 500bp => net 500 / tax 25.
	wantBands := []ExportSaleTaxLine{
		{RateBP: 2000, Net: money.FromMinor(3000), Tax: money.FromMinor(600)},
		{RateBP: 500, Net: money.FromMinor(500), Tax: money.FromMinor(25)},
	}
	if len(got[0].TaxLines) != len(wantBands) {
		t.Fatalf("expected one tax line per distinct rate %+v, got %+v", wantBands, got[0].TaxLines)
	}
	for i, want := range wantBands {
		if got[0].TaxLines[i] != want {
			t.Fatalf("tax line %d = %+v, want %+v", i, got[0].TaxLines[i], want)
		}
	}

	// Two distinct methods, ORDER BY method_id: card then cash, each net of
	// change_given and summed across its own payment rows.
	wantPays := []ExportSalePayment{
		{Method: "card", Amount: money.FromMinor(1625)},
		{Method: "cash", Amount: money.FromMinor(2500)},
	}
	if len(got[0].Payments) != len(wantPays) {
		t.Fatalf("expected one payment per distinct method %+v, got %+v", wantPays, got[0].Payments)
	}
	for i, want := range wantPays {
		if got[0].Payments[i] != want {
			t.Fatalf("payment %d = %+v, want %+v", i, got[0].Payments[i], want)
		}
	}
}

func TestSalesForExport_EmptyRange(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	got, err := dbx.repo.SalesForExport(ctx, "2026-01-01", "2026-01-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no sales, got %+v", got)
	}
}

// TestStockForExport is the ut-docs#59 counterpart to TestSalesForExport:
// an export/report plugin needs current stock levels, not just sales, to
// build a "speedy"-parity stock export. StockForExport reshapes
// ListStockLevels' rows (already exercised in depth by
// TestListStockLevels_Batch8 -- active-only, per-location, variant-exclusion)
// with JSON tags for the wire; this just proves the reshape carries every
// field through correctly.
func TestStockForExport(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dbx.d.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO items(id, sku, name, base_price, reorder_level, is_active) VALUES('itm-stock', 'SKU-STK', 'Stock Widget', 500, 3, 1)`)
	// Inactive item's inventory must not leak into the export (matches
	// ListStockLevels' i.is_active = 1 filter).
	mustExec(`INSERT INTO items(id, sku, name, base_price, reorder_level, is_active) VALUES('itm-stock-off', 'SKU-OFF', 'Inactive Widget', 500, 0, 0)`)
	mustExec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity) VALUES('inv1', 'itm-stock', NULL, 'loc1', 7.5)`)
	mustExec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity) VALUES('inv2', 'itm-stock-off', NULL, 'loc1', 99)`)

	got, err := dbx.repo.StockForExport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 active stock row, got %+v", got)
	}
	row := got[0]
	if row.ItemID != "itm-stock" || row.Name != "Stock Widget" || row.SKU != "SKU-STK" {
		t.Fatalf("unexpected item fields: %+v", row)
	}
	if row.LocationID != "loc1" || row.LocationName != "Main" {
		t.Fatalf("unexpected location fields: %+v", row)
	}
	if row.CurrentQty != 7.5 || row.ReorderLevel != 3 {
		t.Fatalf("unexpected qty/reorder fields: %+v", row)
	}
}

func TestStockForExport_Empty(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	got, err := dbx.repo.StockForExport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no stock rows, got %+v", got)
	}
}
