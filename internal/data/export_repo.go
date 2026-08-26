package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
)

// ExportSaleRow is one completed sale as an export/report plugin needs it
// (ut-docs#221): enough to build a real fiscal file (DATEV, DSFinV-K, ...)
// rather than just the from/to/entry_key envelope export.requested.ask
// carried before this change.
type ExportSaleRow struct {
	ReceiptNo string              `json:"receipt_no"`
	CreatedAt string              `json:"created_at"`
	Total     money.Money         `json:"total"`
	TaxLines  []ExportSaleTaxLine `json:"tax_lines"` // one per distinct tax_rate_bp on the sale
	Payments  []ExportSalePayment `json:"payments"`  // one per payment method used
}

// ExportSaleTaxLine is one tax band's net/tax totals within a single sale.
type ExportSaleTaxLine struct {
	RateBP int64       `json:"rate_bp"`
	Net    money.Money `json:"net"`
	Tax    money.Money `json:"tax"`
}

// ExportSalePayment is one payment method's total within a single sale.
type ExportSalePayment struct {
	Method string      `json:"method"`
	Amount money.Money `json:"amount"`
}

// ExportStockRow is one item's (or one variant's) on-hand quantity at one
// stock location, for an export/report plugin's "export.requested.ask"
// payload (ut-docs#59; variant rows added by ut-docs#240 — ADR-0043). It
// reflects current on-hand stock, not a historical level as of the
// requested date range's end — there is no stock-movement history table to
// reconstruct a past-dated quantity from (a future card if a real fiscal
// format ever needs it).
//
// VariantID/VariantName are empty for an item-level row. When set, the row
// is variant-scoped: ItemID/Name still identify the *parent* item (so a
// plugin can group by item), SKU is the variant's own SKU (not the
// parent's), and CurrentQty is that variant's own stock — never folded into
// the parent item's row, and the parent's own row (if any) is unaffected.
type ExportStockRow struct {
	ItemID       string  `json:"item_id"`
	Name         string  `json:"name"`
	SKU          string  `json:"sku"`
	VariantID    string  `json:"variant_id,omitempty"`
	VariantName  string  `json:"variant_name,omitempty"`
	LocationID   string  `json:"location_id"`
	LocationName string  `json:"location_name"`
	CurrentQty   float64 `json:"current_qty"`
	ReorderLevel int     `json:"reorder_level"`
}

// EODCloseExport is one archived day-close's payment-method x VAT-rate
// cross-tab (+ tips/vouchers/cash-skim), for an accounting-export plugin
// (ut-docs#1005). Read from the ALREADY-ARCHIVED, immutable report
// (ArchivedReportRow.Content, json-unmarshaled), never recomputed fresh,
// so a generated batch can never disagree with the Z-report a merchant
// already has in hand. ZNumber is the document key (DATEV Belegfeld1).
type EODCloseExport struct {
	ZNumber int64     `json:"z_number"`
	Report  EODReport `json:"report"`
}

// EODClosesForExport returns every archived day-close ("eod" kind) whose
// period falls in [from, to], oldest first, as export-payload closes —
// ArchivedReportsInRange plus the EODClosesFromArchive conversion. Same
// caller-bounds-the-range division of responsibility as
// ArchivedReportsInRange itself.
func (r *POSRepo) EODClosesForExport(ctx context.Context, from, to string) ([]EODCloseExport, error) {
	rows, err := r.ArchivedReportsInRange(ctx, from, to)
	if err != nil {
		return nil, err
	}
	return EODClosesFromArchive(rows), nil
}

// EODClosesFromArchive converts archived report rows (any kind — e.g. an
// ArchivedReportsInRange result) to export closes, keeping only
// Kind=="eod" rows. A row whose Content fails to unmarshal is SKIPPED with
// a warning log, never an error: one corrupt archive row must not take
// every other close's export down with it (the export plugin sees, and can
// refuse on, whatever gaps matter to its own format — e.g. a missing
// Z-number in the sequence).
func EODClosesFromArchive(rows []ArchivedReportRow) []EODCloseExport {
	// Empty (not nil): the slice is JSON-encoded into the plugin payload,
	// and "[]" vs "null" is a wire-visible difference — same reasoning as
	// ArchivedReportsInRange's own non-nil result.
	out := []EODCloseExport{}
	for _, row := range rows {
		if row.Kind != "eod" {
			continue
		}
		var rep EODReport
		if err := json.Unmarshal([]byte(row.Content), &rep); err != nil {
			logging.L().Warnf("pos: eod closes for export: skipping archived report %s (period %s): content_json unparseable: %v", row.ID, row.Period, err)
			continue
		}
		out = append(out, EODCloseExport{ZNumber: row.ZNumber, Report: rep})
	}
	return out
}

// exportToSentinel applies the "include the whole final day" trick
// SalesForExport and CountSalesForExport both need for a date-only upper
// bound (same trick as InvoiceRepo.List) -- shared so the two queries can
// never silently disagree about which sales are in range.
func exportToSentinel(to string) string {
	if to == "" {
		return "￿"
	}
	return to + "￿"
}

// CountSalesForExport returns how many sales SalesForExport would return for
// the same [from, to], without loading any row data -- a cheap COUNT(*)
// used to reject an over-large export before the batch gather in
// SalesForExport (and the WASM dispatch after it) ever runs (ut-docs#439).
// The WHERE clause mirrors SalesForExport's exactly, via exportToSentinel,
// so the two can never disagree on which sales match.
func (r *POSRepo) CountSalesForExport(ctx context.Context, from, to string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM sales
WHERE status = 'completed' AND sale_type = 'sale'
  AND created_at >= ? AND created_at <= ?`, from, exportToSentinel(to)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count sales for export: %w", err)
	}
	return n, nil
}

// StockForExport returns current on-hand stock per active item/location for
// an export/report plugin's payload — item-level rows are the same ones
// ListStockLevels serves the inventory page, reshaped with JSON tags for
// the wire; variant-level rows come from variantStockForExport (ut-docs#240)
// and are appended, not merged into their parent's row.
func (r *POSRepo) StockForExport(ctx context.Context) ([]ExportStockRow, error) {
	levels, err := r.ListStockLevels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ExportStockRow, 0, len(levels))
	for _, l := range levels {
		out = append(out, ExportStockRow{
			ItemID:       l.ItemID,
			Name:         l.Name,
			SKU:          l.SKU,
			LocationID:   l.LocationID,
			LocationName: l.LocationName,
			CurrentQty:   l.CurrentQty,
			ReorderLevel: l.ReorderLevel,
		})
	}
	variants, err := r.variantStockForExport(ctx)
	if err != nil {
		return nil, err
	}
	return append(out, variants...), nil
}

// variantStockForExport returns current on-hand stock for variant-scoped
// inventory rows (inventory.item_id NULL, variant_id set — see the CHECK
// constraint in 001_init.sql), the export-payload counterpart to
// ListStockLevels' item-scoped query. Deliberately a separate query rather
// than a change to ListStockLevels: the /inventory page's existing
// item-only view is unchanged (ADR-0043) and keeps its own dedicated test
// (TestListStockLevels_Batch8).
//
// Both the variant and its parent item must be active — a deactivated
// item's variant stock doesn't appear, matching ListStockLevels'
// i.is_active filter for item-level rows.
func (r *POSRepo) variantStockForExport(ctx context.Context) ([]ExportStockRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT i.id, i.name, v.id, v.sku, v.name, inv.location_id, COALESCE(sl.name, ''),
       COALESCE(inv.quantity, 0), COALESCE(i.reorder_level, 0)
FROM inventory inv
JOIN item_variants v ON v.id = inv.variant_id
JOIN items i ON i.id = v.item_id
LEFT JOIN stock_locations sl ON sl.id = inv.location_id
WHERE i.is_active = 1 AND v.is_active = 1
ORDER BY i.name, v.name, sl.name`)
	if err != nil {
		return nil, fmt.Errorf("query variant stock for export: %w", err)
	}
	defer rows.Close()
	var out []ExportStockRow
	for rows.Next() {
		var row ExportStockRow
		var sku sql.NullString
		if err := rows.Scan(&row.ItemID, &row.Name, &row.VariantID, &sku, &row.VariantName,
			&row.LocationID, &row.LocationName, &row.CurrentQty, &row.ReorderLevel); err != nil {
			return nil, fmt.Errorf("scan variant stock for export: %w", err)
		}
		row.SKU = sku.String
		out = append(out, row)
	}
	return out, rows.Err()
}

// SalesForExport returns completed sales in [from, to] (inclusive of the
// whole final day — same "append a sentinel high character" trick as
// InvoiceRepo.List) with their tax-band and payment-method breakdowns, for
// an export/report plugin's "export.requested.ask" payload.
//
// Only sale_type='sale' rows are returned — matching the existing
// PaymentBreakdown/busyBuckets/MarginByItem precedent of excluding returns
// outright, rather than the Tax tab's (SalesForTaxWindow/computeTaxSummary,
// ut-docs#1115) include-with-sign-flip convention.
// ExportSaleRow has no field to carry a return's sign, and a plugin needs to
// tell a genuine sale from a refund unambiguously; a future card can add
// returns as their own rows (with a SaleType field) if a real export format
// needs them.
func (r *POSRepo) SalesForExport(ctx context.Context, from, to string) ([]ExportSaleRow, error) {
	to = exportToSentinel(to)

	rows, err := r.db.QueryContext(ctx, `
SELECT id, receipt_no, created_at, total
FROM sales
WHERE status = 'completed' AND sale_type = 'sale'
  AND created_at >= ? AND created_at <= ?
ORDER BY created_at ASC, receipt_no ASC`, from, to)
	if err != nil {
		return nil, fmt.Errorf("sales for export: %w", err)
	}
	defer rows.Close()

	type saleKey struct {
		id  string
		idx int
	}
	var keys []saleKey
	var out []ExportSaleRow
	for rows.Next() {
		var id, receiptNo, createdAt string
		var totalMinor int64
		if err := rows.Scan(&id, &receiptNo, &createdAt, &totalMinor); err != nil {
			return nil, fmt.Errorf("scan export sale: %w", err)
		}
		keys = append(keys, saleKey{id: id, idx: len(out)})
		out = append(out, ExportSaleRow{
			ReceiptNo: receiptNo,
			CreatedAt: createdAt,
			Total:     money.FromMinor(totalMinor),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sales for export: %w", err)
	}
	if len(out) == 0 {
		return out, nil
	}

	// ut-docs#229: batch both breakdowns across every matched sale in two
	// range-scoped joined queries instead of two extra queries PER sale
	// (the previous shape meant ~100k queries for a busy till's year-long
	// export). Same WHERE/GROUP BY semantics as the per-sale helpers this
	// replaces, just joined against `sales` once for the whole range
	// rather than filtered to one sale_id at a time.
	taxLinesBySale, err := r.exportSaleTaxLinesBatch(ctx, from, to)
	if err != nil {
		return nil, err
	}
	paymentsBySale, err := r.exportSalePaymentsBatch(ctx, from, to)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		out[k.idx].TaxLines = taxLinesBySale[k.id]
		out[k.idx].Payments = paymentsBySale[k.id]
	}
	return out, nil
}

// exportSaleTaxLinesBatch returns every completed sale's tax-band breakdown
// within [from, to] (same bounds SalesForExport already resolved), grouped
// by sale ID then tax_rate_bp — one query for the whole range instead of
// one per sale. Rows arrive ordered by sale_id then tax_rate_bp DESC, so
// appending in scan order reproduces exportSaleTaxLines' old per-sale
// ordering for each sale's slice.
func (r *POSRepo) exportSaleTaxLinesBatch(ctx context.Context, from, to string) (map[string][]ExportSaleTaxLine, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT sl.sale_id, sl.tax_rate_bp, COALESCE(SUM(sl.total_before_tax), 0), COALESCE(SUM(sl.tax_amount), 0)
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
WHERE s.status = 'completed' AND s.sale_type = 'sale'
  AND s.created_at >= ? AND s.created_at <= ?
GROUP BY sl.sale_id, sl.tax_rate_bp
ORDER BY sl.sale_id, sl.tax_rate_bp DESC`, from, to)
	if err != nil {
		return nil, fmt.Errorf("export sale tax lines batch: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]ExportSaleTaxLine)
	for rows.Next() {
		var saleID string
		var rateBP, netMinor, taxMinor int64
		if err := rows.Scan(&saleID, &rateBP, &netMinor, &taxMinor); err != nil {
			return nil, fmt.Errorf("scan export tax line: %w", err)
		}
		out[saleID] = append(out[saleID], ExportSaleTaxLine{
			RateBP: rateBP,
			Net:    money.FromMinor(netMinor),
			Tax:    money.FromMinor(taxMinor),
		})
	}
	return out, rows.Err()
}

// exportSalePaymentsBatch is exportSaleTaxLinesBatch's payments-side
// counterpart — one query for the whole range's payment-method breakdown
// instead of one per sale.
func (r *POSRepo) exportSalePaymentsBatch(ctx context.Context, from, to string) (map[string][]ExportSalePayment, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT p.sale_id, p.method_id, COALESCE(SUM(p.amount - p.change_given), 0)
FROM payments p
JOIN sales s ON s.id = p.sale_id
WHERE s.status = 'completed' AND s.sale_type = 'sale'
  AND s.created_at >= ? AND s.created_at <= ?
GROUP BY p.sale_id, p.method_id
ORDER BY p.sale_id, p.method_id`, from, to)
	if err != nil {
		return nil, fmt.Errorf("export sale payments batch: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]ExportSalePayment)
	for rows.Next() {
		var saleID, method string
		var amountMinor int64
		if err := rows.Scan(&saleID, &method, &amountMinor); err != nil {
			return nil, fmt.Errorf("scan export payment: %w", err)
		}
		out[saleID] = append(out[saleID], ExportSalePayment{Method: method, Amount: money.FromMinor(amountMinor)})
	}
	return out, rows.Err()
}
