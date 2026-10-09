package data

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ReceiptViewRow is one receipt of the sales.receipts.v1 core read view
// (ADR-0149 §6, ut-docs#3976). Its JSON shape — with ReceiptViewLine and
// ReceiptViewPayment — is a published contract (ut-docs
// reference/contracts/plugin-views.md, pinned by
// TestCoreViewRowShapesArePinned): never add a page-only field here, and
// never a card or customer detail (masked PAN, auth code, terminal/trace
// id, payment reference, voucher id, customer id).
//
// Amounts are integer minor units as stored: a return's are as the return
// sale stores them. Lines and Payments are [] (never null) when empty.
type ReceiptViewRow struct {
	ID           string `json:"id"`
	ReceiptNo    string `json:"receipt_no"`
	SaleType     string `json:"sale_type"`     // "sale" or "return"
	BusinessDate string `json:"business_date"` // YYYY-MM-DD, the business date asked for
	CreatedAt    string `json:"created_at"`    // as stored (UTC)
	TillID       string `json:"till_id"`
	CashierID    string `json:"cashier_id"` // "" when none
	Currency     string `json:"currency"`

	SubtotalMinor int64 `json:"subtotal_minor"`
	DiscountMinor int64 `json:"discount_minor"`
	TaxMinor      int64 `json:"tax_minor"`
	TotalMinor    int64 `json:"total_minor"`
	TipMinor      int64 `json:"tip_minor"` // sum of the sale's payment tips

	// FiscalSigned: the receipt carries a fiscal signature or signed
	// evidence (a fiscal_tse_signatures or fiscal_receipt_evidence row) —
	// ADR-0149 §5's "signed receipt". Neutral: whichever fiscal plugin wrote it.
	FiscalSigned bool `json:"fiscal_signed"`

	Lines    []ReceiptViewLine    `json:"lines"`
	Payments []ReceiptViewPayment `json:"payments"`
}

// ReceiptViewLine is one sale line of a ReceiptViewRow. A variant line has
// VariantID and an empty ItemID (sale_lines holds exactly one of them).
type ReceiptViewLine struct {
	LineNo         int     `json:"line_no"`
	ItemID         string  `json:"item_id"`
	VariantID      string  `json:"variant_id"`
	Name           string  `json:"name"` // name snapshot at sale time
	SKU            string  `json:"sku"`  // SKU snapshot, "" when none
	Quantity       float64 `json:"quantity"`
	UnitPriceMinor int64   `json:"unit_price_minor"`
	DiscountMinor  int64   `json:"discount_minor"`
	TaxMinor       int64   `json:"tax_minor"`
	TotalMinor     int64   `json:"total_minor"` // after tax
}

// ReceiptViewPayment is one payment of a ReceiptViewRow. AmountMinor is the
// applied amount — tendered minus change — as payments.breakdown.v1 sums it.
type ReceiptViewPayment struct {
	MethodID    string `json:"method_id"`
	AmountMinor int64  `json:"amount_minor"`
	TipMinor    int64  `json:"tip_minor"`
}

// coreViewNow is the clock the date-relative views read; a test pins it.
var coreViewNow = time.Now

// receiptsBusinessDay is sales.receipts.v1's window, in now's location: the
// business date now falls in — the wall clock shifted back by the
// business-day start hh:mm, the same shift sales.by_day.v1 buckets with —
// minus daysAgo days, and the half-open [from, to) running from that date
// at hh:mm to the next date at hh:mm.
func receiptsBusinessDay(now time.Time, hh, mm, daysAgo int) (date string, from, to time.Time) {
	loc := now.Location()
	shifted := time.Date(now.Year(), now.Month(), now.Day(), now.Hour()-hh, now.Minute()-mm, now.Second(), 0, loc)
	y, mo, d := shifted.Date()
	from = time.Date(y, mo, d-daysAgo, hh, mm, 0, 0, loc)
	return from.Format("2006-01-02"), from, from.AddDate(0, 0, 1)
}

// ListReceiptViewRows returns one page of the completed receipts (sales and
// returns) created in [from, to), oldest first, each labelled businessDate.
// It orders by datetime(created_at), then id: rows hold both RFC3339 and
// "YYYY-MM-DD HH:MM:SS" forms, which sort wrongly as plain text. Three
// queries per page whatever its size: the sales (tip and fiscal_signed as
// subqueries), then all their lines, then all their payments.
func (r *POSRepo) ListReceiptViewRows(ctx context.Context, businessDate string, from, to time.Time, offset, limit int) ([]ReceiptViewRow, error) {
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, `
		SELECT s.id, s.receipt_no, s.sale_type, s.created_at, COALESCE(s.till_id, ''), COALESCE(s.cashier_id, ''),
		       s.currency, s.subtotal, s.discount_total, s.tax_total, s.total,
		       (SELECT COALESCE(SUM(p.tip_amount), 0) FROM payments p WHERE p.sale_id = s.id),
		       EXISTS (SELECT 1 FROM fiscal_tse_signatures f WHERE f.sale_id = s.id)
		         OR EXISTS (SELECT 1 FROM fiscal_receipt_evidence e WHERE e.sale_id = s.id)
		FROM sales s
		WHERE s.status = 'completed' AND s.sale_type IN ('sale', 'return')
		  AND datetime(s.created_at) >= datetime(?) AND datetime(s.created_at) < datetime(?)
		ORDER BY datetime(s.created_at), s.id
		LIMIT ? OFFSET ?`, fromStr, toStr, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list receipt view rows: %w", err)
	}
	defer rows.Close()
	out := []ReceiptViewRow{}
	index := map[string]int{}
	for rows.Next() {
		rr := ReceiptViewRow{BusinessDate: businessDate, Lines: []ReceiptViewLine{}, Payments: []ReceiptViewPayment{}}
		var signed int
		if err := rows.Scan(&rr.ID, &rr.ReceiptNo, &rr.SaleType, &rr.CreatedAt, &rr.TillID, &rr.CashierID,
			&rr.Currency, &rr.SubtotalMinor, &rr.DiscountMinor, &rr.TaxMinor, &rr.TotalMinor, &rr.TipMinor, &signed); err != nil {
			return nil, fmt.Errorf("scan receipt view row: %w", err)
		}
		rr.FiscalSigned = signed != 0
		index[rr.ID] = len(out)
		out = append(out, rr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list receipt view rows: %w", err)
	}
	rows.Close()
	if len(out) == 0 {
		return out, nil
	}

	in := strings.TrimSuffix(strings.Repeat("?,", len(out)), ",")
	ids := make([]any, len(out))
	for i, rr := range out {
		ids[i] = rr.ID
	}

	lineRows, err := r.db.QueryContext(ctx, `
		SELECT sale_id, line_no, COALESCE(item_id, ''), COALESCE(variant_id, ''), name_snapshot, COALESCE(sku_snapshot, ''),
		       quantity, unit_price, line_discount, tax_amount, total_after_tax
		FROM sale_lines
		WHERE sale_id IN (`+in+`)
		ORDER BY sale_id, line_no`, ids...)
	if err != nil {
		return nil, fmt.Errorf("list receipt view lines: %w", err)
	}
	defer lineRows.Close()
	for lineRows.Next() {
		var saleID string
		var l ReceiptViewLine
		if err := lineRows.Scan(&saleID, &l.LineNo, &l.ItemID, &l.VariantID, &l.Name, &l.SKU,
			&l.Quantity, &l.UnitPriceMinor, &l.DiscountMinor, &l.TaxMinor, &l.TotalMinor); err != nil {
			return nil, fmt.Errorf("scan receipt view line: %w", err)
		}
		if i, ok := index[saleID]; ok {
			out[i].Lines = append(out[i].Lines, l)
		}
	}
	if err := lineRows.Err(); err != nil {
		return nil, fmt.Errorf("list receipt view lines: %w", err)
	}
	lineRows.Close()

	payRows, err := r.db.QueryContext(ctx, `
		SELECT sale_id, method_id, amount - change_given, tip_amount
		FROM payments
		WHERE sale_id IN (`+in+`)
		ORDER BY sale_id, paid_at, rowid`, ids...)
	if err != nil {
		return nil, fmt.Errorf("list receipt view payments: %w", err)
	}
	defer payRows.Close()
	for payRows.Next() {
		var saleID string
		var p ReceiptViewPayment
		if err := payRows.Scan(&saleID, &p.MethodID, &p.AmountMinor, &p.TipMinor); err != nil {
			return nil, fmt.Errorf("scan receipt view payment: %w", err)
		}
		if i, ok := index[saleID]; ok {
			out[i].Payments = append(out[i].Payments, p)
		}
	}
	return out, payRows.Err()
}
