package data

import (
	"context"
	"fmt"

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

// ExportStockRow is one item's on-hand quantity at one stock location, for
// an export/report plugin's "export.requested.ask" payload (ut-docs#59).
// It reflects current on-hand stock, not a historical level as of the
// requested date range's end — there is no stock-movement history table to
// reconstruct a past-dated quantity from (a future card if a real fiscal
// format ever needs it).
type ExportStockRow struct {
	ItemID       string  `json:"item_id"`
	Name         string  `json:"name"`
	SKU          string  `json:"sku"`
	LocationID   string  `json:"location_id"`
	LocationName string  `json:"location_name"`
	CurrentQty   float64 `json:"current_qty"`
	ReorderLevel int     `json:"reorder_level"`
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
// an export/report plugin's payload — the same rows ListStockLevels already
// serves the inventory page, reshaped with JSON tags for the wire.
func (r *POSRepo) StockForExport(ctx context.Context) ([]ExportStockRow, error) {
	levels, err := r.ListStockLevels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ExportStockRow, len(levels))
	for i, l := range levels {
		out[i] = ExportStockRow{
			ItemID:       l.ItemID,
			Name:         l.Name,
			SKU:          l.SKU,
			LocationID:   l.LocationID,
			LocationName: l.LocationName,
			CurrentQty:   l.CurrentQty,
			ReorderLevel: l.ReorderLevel,
		}
	}
	return out, nil
}

// SalesForExport returns completed sales in [from, to] (inclusive of the
// whole final day — same "append a sentinel high character" trick as
// InvoiceRepo.List) with their tax-band and payment-method breakdowns, for
// an export/report plugin's "export.requested.ask" payload.
//
// Only sale_type='sale' rows are returned — matching the existing
// PaymentBreakdown/busyBuckets/MarginByItem precedent of excluding returns
// outright, rather than TaxSummary's include-with-sign-flip convention.
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
