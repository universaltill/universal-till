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
	if to != "" {
		to += "￿" // include the whole final day for date-only bounds
	} else {
		to = "￿"
	}

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

	for _, k := range keys {
		taxLines, err := r.exportSaleTaxLines(ctx, k.id)
		if err != nil {
			return nil, err
		}
		payments, err := r.exportSalePayments(ctx, k.id)
		if err != nil {
			return nil, err
		}
		out[k.idx].TaxLines = taxLines
		out[k.idx].Payments = payments
	}
	return out, nil
}

func (r *POSRepo) exportSaleTaxLines(ctx context.Context, saleID string) ([]ExportSaleTaxLine, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT tax_rate_bp, COALESCE(SUM(total_before_tax), 0), COALESCE(SUM(tax_amount), 0)
FROM sale_lines
WHERE sale_id = ?
GROUP BY tax_rate_bp
ORDER BY tax_rate_bp DESC`, saleID)
	if err != nil {
		return nil, fmt.Errorf("export sale tax lines: %w", err)
	}
	defer rows.Close()
	var out []ExportSaleTaxLine
	for rows.Next() {
		var rateBP, netMinor, taxMinor int64
		if err := rows.Scan(&rateBP, &netMinor, &taxMinor); err != nil {
			return nil, fmt.Errorf("scan export tax line: %w", err)
		}
		out = append(out, ExportSaleTaxLine{
			RateBP: rateBP,
			Net:    money.FromMinor(netMinor),
			Tax:    money.FromMinor(taxMinor),
		})
	}
	return out, rows.Err()
}

func (r *POSRepo) exportSalePayments(ctx context.Context, saleID string) ([]ExportSalePayment, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT method_id, COALESCE(SUM(amount - change_given), 0)
FROM payments
WHERE sale_id = ?
GROUP BY method_id
ORDER BY method_id`, saleID)
	if err != nil {
		return nil, fmt.Errorf("export sale payments: %w", err)
	}
	defer rows.Close()
	var out []ExportSalePayment
	for rows.Next() {
		var method string
		var amountMinor int64
		if err := rows.Scan(&method, &amountMinor); err != nil {
			return nil, fmt.Errorf("scan export payment: %w", err)
		}
		out = append(out, ExportSalePayment{Method: method, Amount: money.FromMinor(amountMinor)})
	}
	return out, rows.Err()
}
