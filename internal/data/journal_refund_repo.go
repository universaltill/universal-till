package data

import (
	"context"
	"fmt"

	"github.com/universaltill/universal-till/internal/money"
)

// Journal refund status values (ut-docs#7064).
const (
	JournalRefundRefunded = "refunded"
	JournalRefundPartly   = "partly"
	JournalRefundPending  = "pending"
)

// JournalRefund is what the journal shows about a refund, keyed by sale id.
type JournalRefund struct {
	// OriginalReceipt: on a return row, the refunded sale's receipt_no.
	OriginalReceipt string
	// Refunded: on a sale row, the sum of its completed linked returns.
	Refunded money.Money
	// Total: on a sale row, the sale's own total.
	Total money.Money
	// Status: on a sale row, "" | refunded | partly | pending.
	Status string
}

// SignedTotal is the amount the journal shows: a return reads negative (the
// stored Total stays positive for reports, safe mode and CSV).
func (e SaleJournalEntry) SignedTotal() money.Money {
	if e.SaleType == "return" {
		return money.FromMinor(-e.Total)
	}
	return money.FromMinor(e.Total)
}

// deriveRefundStatus: a pending refund leg wins (never "refunded" while the
// provider outcome is unconfirmed), else by how much of the total came back.
func deriveRefundStatus(refunded, total money.Money, pending bool) string {
	switch {
	case pending:
		return JournalRefundPending
	case refunded.IsPositive() && refunded >= total:
		return JournalRefundRefunded
	case refunded.IsPositive():
		return JournalRefundPartly
	}
	return ""
}

// JournalRefunds derives refund links and status for a page of journal rows
// in a constant number of queries (chunked by id count), no N+1. Only rows
// with something to show get an entry. Pure read.
func (r *POSRepo) JournalRefunds(ctx context.Context, entries []SaleJournalEntry) (map[string]*JournalRefund, error) {
	out := map[string]*JournalRefund{}
	var returnIDs, saleIDs []string
	totals := map[string]int64{}
	for _, e := range entries {
		if e.ID == "" {
			continue
		}
		if e.SaleType == "return" {
			returnIDs = append(returnIDs, e.ID)
		} else {
			saleIDs = append(saleIDs, e.ID)
			totals[e.ID] = e.Total
		}
	}

	// 1. return -> original receipt.
	for _, chunk := range ChunkStrings(returnIDs, IDChunkSize) {
		ph, args := inPlaceholders(chunk)
		rows, err := r.db.QueryContext(ctx, `SELECT k.sale_id, o.receipt_no FROM sale_links k JOIN sales o ON o.id = k.original_sale_id WHERE k.sale_id IN (`+ph+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("journal refund originals: %w", err)
		}
		for rows.Next() {
			var id, receipt string
			if err := rows.Scan(&id, &receipt); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan journal refund original: %w", err)
			}
			if out[id] == nil && receipt != "" {
				out[id] = &JournalRefund{OriginalReceipt: receipt}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("journal refund originals: %w", err)
		}
	}

	// 2. sale -> sum of completed returns (all tenders), same filter as
	// RefundedServiceChargeTotal.
	refunded := map[string]int64{}
	for _, chunk := range ChunkStrings(saleIDs, IDChunkSize) {
		ph, args := inPlaceholders(chunk)
		rows, err := r.db.QueryContext(ctx, `SELECT k.original_sale_id, COALESCE(SUM(s.total), 0) FROM sale_links k
JOIN sales s ON s.id = k.sale_id AND s.sale_type = 'return' AND s.status = 'completed'
WHERE k.original_sale_id IN (`+ph+`) GROUP BY k.original_sale_id`, args...)
		if err != nil {
			return nil, fmt.Errorf("journal refund totals: %w", err)
		}
		for rows.Next() {
			var id string
			var sum int64
			if err := rows.Scan(&id, &sum); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan journal refund total: %w", err)
			}
			refunded[id] = sum
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("journal refund totals: %w", err)
		}
	}

	// 3. refund legs whose outcome is unconfirmed.
	pending, err := NewPaymentLegJournalRepo(r.db).PendingRefundSales(ctx, saleIDs)
	if err != nil {
		return nil, err
	}

	for _, id := range saleIDs {
		jr := &JournalRefund{Refunded: money.FromMinor(refunded[id]), Total: money.FromMinor(totals[id])}
		jr.Status = deriveRefundStatus(jr.Refunded, jr.Total, pending[id])
		if jr.Status != "" {
			out[id] = jr
		}
	}
	return out, nil
}

// JournalRefundFor is JournalRefunds for one sale (the detail page); nil when
// there is nothing to show.
func (r *POSRepo) JournalRefundFor(ctx context.Context, saleID string, total int64, saleType string) (*JournalRefund, error) {
	m, err := r.JournalRefunds(ctx, []SaleJournalEntry{{ID: saleID, Total: total, SaleType: saleType}})
	if err != nil {
		return nil, err
	}
	return m[saleID], nil
}
