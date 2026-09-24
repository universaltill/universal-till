package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

// Per-(business date, till) sales rollup reads for the cloud sales-aggregate
// upload (ut-docs#2535; ADR-0111 §1). internal/cloudsync turns
// these into the wire DTO; the SQL lives here (repository pattern).
//
// Conventions shared by every method below:
//   - business date = sales.local_date, the same local-calendar-day column
//     EndOfDay/dateRangeSummary windows on (ut-docs#869/#1342), so a day's
//     rollup covers exactly the Z-report's sales. A voided sale belongs to
//     the day it was VOIDED (COALESCE(voided_local_date, local_date)), the
//     same rule as EODReport.CancelCount.
//   - till key = sales.till_id when non-empty (a replica's journaled sale,
//     ADR-0011 D3), else register_id, else selfTill — the caller's own
//     identity for sales that carry neither (tillKeyExpr).
//   - "net sales" = SUM(sales.total) over completed sale-type sales — the
//     same figure busyBuckets (Sales-trend hour/weekday charts) and
//     EODReport.Gross sum. Returns are NOT subtracted inside a bucket; they
//     travel as refund counts (and net out in by_payment_method, which
//     mirrors EODMethod In − Out).

// tillKeyExpr resolves a sale's rollup till key; its one '?' is selfTill.
const tillKeyExpr = `COALESCE(NULLIF(s.till_id, ''), NULLIF(s.register_id, ''), ?)`

// SalesAggregateKey is one (business date, till) that has sales to roll up.
type SalesAggregateKey struct {
	BusinessDate string
	TillID       string
}

// SalesAggregateHour is one local-hour bucket of completed sale-type sales.
type SalesAggregateHour struct {
	Hour  int
	Net   money.Money
	Count int
}

// SalesAggregatePayment is one payment method's net takings for the day,
// mirroring EODMethod (In − Out: amount − change_given, sales minus
// returns — so it INCLUDES tips, exactly like EODMethod.In) plus the
// method's tips (EODTip). ExpectedCash is the cash method's net cash taken
// (cash tendered − change given − cash refunded), zero for any other method.
type SalesAggregatePayment struct {
	Method       string
	Amount       money.Money
	Tips         money.Money
	ExpectedCash money.Money
}

// SalesAggregateItem is one item's sold quantity for the day (completed
// sale-type sales only, like ArticleSalesForDay). A variant line rolls up
// under its parent item. TopModifierID is the most frequently selected
// modifier option on that item's lines ("" when none; ties break on the
// lower option id so the payload is deterministic).
type SalesAggregateItem struct {
	ItemID        string
	Qty           float64
	TopModifierID string
}

// SalesAggregateCashier is one cashier's (sales.cashier_id — an id, never a
// name) figures for the day. Net/Count/ItemQty cover completed sale-type
// sales; Refunds counts completed returns, Voids voided sales (on their
// void day), Discounts discounted sales.
type SalesAggregateCashier struct {
	StaffID   string
	Net       money.Money
	Count     int
	ItemQty   float64
	Refunds   int
	Voids     int
	Discounts int
}

// SalesAggregate is one (business date, till) rollup. Cashiers is nil
// unless the caller asked for it (the per-shop reports.cloud_staff_breakdown
// toggle, ADR-0111 §4).
type SalesAggregate struct {
	Hourly        []SalesAggregateHour
	Payments      []SalesAggregatePayment
	Items         []SalesAggregateItem
	Cashiers      []SalesAggregateCashier
	RefundCount   int
	VoidCount     int
	DiscountCount int
}

// discountedSaleExpr is true for a sale carrying any discount: a whole-sale
// discount (sales.discount_total) or any sale_discounts row (the canonical
// ledger, which also records every per-line discount).
const discountedSaleExpr = `(s.discount_total > 0 OR EXISTS (SELECT 1 FROM sale_discounts sd WHERE sd.sale_id = s.id AND sd.amount > 0))`

// voidDayExpr is the day a voided sale is counted on (EODReport.CancelCount).
const voidDayExpr = `COALESCE(s.voided_local_date, s.local_date)`

// SalesAggregateKeys lists every (business date, till) in [from, to]
// (YYYY-MM-DD, inclusive) that has a completed sale or a void, ordered by
// date then till.
func (r *POSRepo) SalesAggregateKeys(ctx context.Context, from, to, selfTill string) ([]SalesAggregateKey, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT d, t FROM (
  SELECT s.local_date AS d, `+tillKeyExpr+` AS t
  FROM sales s
  WHERE s.status = 'completed' AND s.local_date BETWEEN date(?) AND date(?)
  UNION
  SELECT `+voidDayExpr+` AS d, `+tillKeyExpr+` AS t
  FROM sales s
  WHERE s.status = 'voided' AND `+voidDayExpr+` BETWEEN date(?) AND date(?)
)
ORDER BY d, t`, selfTill, from, to, selfTill, from, to)
	if err != nil {
		return nil, fmt.Errorf("sales aggregate keys: %w", err)
	}
	defer rows.Close()
	var out []SalesAggregateKey
	for rows.Next() {
		var k SalesAggregateKey
		if err := rows.Scan(&k.BusinessDate, &k.TillID); err != nil {
			return nil, fmt.Errorf("scan sales aggregate key: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// SalesAggregateForTill reads one (business date, till) rollup. Every slice
// is ordered deterministically (hour; method; item id; staff id) so the
// caller's content hash only moves when the figures do.
func (r *POSRepo) SalesAggregateForTill(ctx context.Context, day, tillID, selfTill string, withCashiers bool) (SalesAggregate, error) {
	var agg SalesAggregate
	// Completed sales on this (day, till): the scope every figure but voids uses.
	scope := `s.status = 'completed' AND s.local_date = date(?) AND ` + tillKeyExpr + ` = ?`
	args := []any{day, selfTill, tillID}

	// Hourly: local hour of created_at (no business-day-start shift — the
	// rollup's day is the calendar local_date, so its hours are too).
	rows, err := r.db.QueryContext(ctx, `
SELECT CAST(strftime('%H', s.created_at, 'localtime') AS INTEGER) AS h, COALESCE(SUM(s.total), 0), COUNT(*)
FROM sales s
WHERE s.sale_type = 'sale' AND `+scope+`
GROUP BY h ORDER BY h`, args...)
	if err != nil {
		return agg, fmt.Errorf("sales aggregate hourly: %w", err)
	}
	for rows.Next() {
		var h SalesAggregateHour
		var net int64
		if err := rows.Scan(&h.Hour, &net, &h.Count); err != nil {
			rows.Close()
			return agg, fmt.Errorf("scan sales aggregate hour: %w", err)
		}
		h.Net = money.FromMinor(net)
		agg.Hourly = append(agg.Hourly, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return agg, err
	}

	// Payment methods: EODMethod's In − Out and EODTip's tip sum, one pass.
	rows, err = r.db.QueryContext(ctx, `
SELECT p.method_id,
  COALESCE(SUM(CASE WHEN s.sale_type = 'sale'   THEN p.amount - p.change_given END), 0)
  - COALESCE(SUM(CASE WHEN s.sale_type = 'return' THEN p.amount - p.change_given END), 0),
  COALESCE(SUM(p.tip_amount), 0)
FROM payments p
JOIN sales s ON s.id = p.sale_id
WHERE `+scope+`
GROUP BY p.method_id ORDER BY p.method_id`, args...)
	if err != nil {
		return agg, fmt.Errorf("sales aggregate payments: %w", err)
	}
	for rows.Next() {
		var p SalesAggregatePayment
		var amount, tips int64
		if err := rows.Scan(&p.Method, &amount, &tips); err != nil {
			rows.Close()
			return agg, fmt.Errorf("scan sales aggregate payment: %w", err)
		}
		p.Amount, p.Tips = money.FromMinor(amount), money.FromMinor(tips)
		if p.Method == "cash" {
			// The Z-report's drawer figure (CashReconciliation.CashSales,
			// ut-docs#1046): cash tips held out — they travel in Tips.
			// Opening float and pay-ins/outs are shift data, not included.
			p.ExpectedCash = p.Amount.Sub(p.Tips)
		}
		agg.Payments = append(agg.Payments, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return agg, err
	}

	// Items: sold quantity per parent item (variant lines roll up).
	rows, err = r.db.QueryContext(ctx, `
SELECT COALESCE(sl.item_id, iv.item_id, '') AS item, COALESCE(SUM(sl.quantity), 0)
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
LEFT JOIN item_variants iv ON iv.id = sl.variant_id
WHERE s.sale_type = 'sale' AND `+scope+`
GROUP BY item ORDER BY item`, args...)
	if err != nil {
		return agg, fmt.Errorf("sales aggregate items: %w", err)
	}
	idx := map[string]int{}
	for rows.Next() {
		var it SalesAggregateItem
		if err := rows.Scan(&it.ItemID, &it.Qty); err != nil {
			rows.Close()
			return agg, fmt.Errorf("scan sales aggregate item: %w", err)
		}
		idx[it.ItemID] = len(agg.Items)
		agg.Items = append(agg.Items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return agg, err
	}

	// Top modifier option per item: ordered so the first row seen per item
	// is its most-selected option (lowest option id on a tie).
	rows, err = r.db.QueryContext(ctx, `
SELECT COALESCE(sl.item_id, iv.item_id, '') AS item, m.option_id, COUNT(*) AS n
FROM sale_line_modifiers m
JOIN sale_lines sl ON sl.id = m.sale_line_id
JOIN sales s ON s.id = sl.sale_id
LEFT JOIN item_variants iv ON iv.id = sl.variant_id
WHERE m.option_id IS NOT NULL AND m.option_id != '' AND s.sale_type = 'sale' AND `+scope+`
GROUP BY item, m.option_id
ORDER BY item, n DESC, m.option_id`, args...)
	if err != nil {
		return agg, fmt.Errorf("sales aggregate modifiers: %w", err)
	}
	for rows.Next() {
		var item, option string
		var n int
		if err := rows.Scan(&item, &option, &n); err != nil {
			rows.Close()
			return agg, fmt.Errorf("scan sales aggregate modifier: %w", err)
		}
		if i, ok := idx[item]; ok && agg.Items[i].TopModifierID == "" {
			agg.Items[i].TopModifierID = option
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return agg, err
	}

	// Top-level counts: refunds and discounted sales over the completed
	// scope; voids on their void day (EODReport.CancelCount's rule).
	err = r.db.QueryRowContext(ctx, `
SELECT
  COALESCE(SUM(CASE WHEN s.sale_type = 'return' THEN 1 END), 0),
  COALESCE(SUM(CASE WHEN s.sale_type = 'sale' AND `+discountedSaleExpr+` THEN 1 END), 0)
FROM sales s
WHERE `+scope, args...).Scan(&agg.RefundCount, &agg.DiscountCount)
	if err != nil {
		return agg, fmt.Errorf("sales aggregate counts: %w", err)
	}
	err = r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM sales s
WHERE s.status = 'voided' AND `+voidDayExpr+` = date(?) AND `+tillKeyExpr+` = ?`,
		day, selfTill, tillID).Scan(&agg.VoidCount)
	if err != nil {
		return agg, fmt.Errorf("sales aggregate voids: %w", err)
	}

	if withCashiers {
		cashiers, err := r.salesAggregateCashiers(ctx, day, tillID, selfTill)
		if err != nil {
			return agg, err
		}
		agg.Cashiers = cashiers
	}
	return agg, nil
}

// salesAggregateCashiers is the by_cashier breakdown: ids only — the users
// table is never joined, so no name can leak into the upload.
func (r *POSRepo) salesAggregateCashiers(ctx context.Context, day, tillID, selfTill string) ([]SalesAggregateCashier, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT COALESCE(s.cashier_id, '') AS staff,
  COALESCE(SUM(CASE WHEN s.status = 'completed' AND s.local_date = date(?) AND s.sale_type = 'sale' THEN s.total END), 0),
  COALESCE(SUM(CASE WHEN s.status = 'completed' AND s.local_date = date(?) AND s.sale_type = 'sale' THEN 1 END), 0),
  COALESCE(SUM(CASE WHEN s.status = 'completed' AND s.local_date = date(?) AND s.sale_type = 'sale'
                    THEN (SELECT COALESCE(SUM(sl.quantity), 0) FROM sale_lines sl WHERE sl.sale_id = s.id) END), 0),
  COALESCE(SUM(CASE WHEN s.status = 'completed' AND s.local_date = date(?) AND s.sale_type = 'return' THEN 1 END), 0),
  COALESCE(SUM(CASE WHEN s.status = 'voided' AND `+voidDayExpr+` = date(?) THEN 1 END), 0),
  COALESCE(SUM(CASE WHEN s.status = 'completed' AND s.local_date = date(?) AND s.sale_type = 'sale' AND `+discountedSaleExpr+` THEN 1 END), 0)
FROM sales s
WHERE `+tillKeyExpr+` = ?
  AND ((s.status = 'completed' AND s.local_date = date(?)) OR (s.status = 'voided' AND `+voidDayExpr+` = date(?)))
GROUP BY staff ORDER BY staff`,
		day, day, day, day, day, day, selfTill, tillID, day, day)
	if err != nil {
		return nil, fmt.Errorf("sales aggregate cashiers: %w", err)
	}
	defer rows.Close()
	out := []SalesAggregateCashier{}
	for rows.Next() {
		var c SalesAggregateCashier
		var net int64
		if err := rows.Scan(&c.StaffID, &net, &c.Count, &c.ItemQty, &c.Refunds, &c.Voids, &c.Discounts); err != nil {
			return nil, fmt.Errorf("scan sales aggregate cashier: %w", err)
		}
		c.Net = money.FromMinor(net)
		out = append(out, c)
	}
	return out, rows.Err()
}

// SalesForTaxBandsForTill is SalesForTaxBands scoped to one (business date,
// till) — the same header/lines/voucher-issue/payment reads, so the caller
// bands a till's day with exactly the Z-report's per-sale math
// (pos.EODTaxBandsFromSales). With a single till the result equals
// SalesForTaxBands(day, day).
func (r *POSRepo) SalesForTaxBandsForTill(ctx context.Context, day, tillID, selfTill string) ([]EODTaxBandSale, error) {
	return r.salesForTaxBandsWhere(ctx,
		`s.local_date = date(?) AND `+tillKeyExpr+` = ?`, day, selfTill, tillID)
}

// SalesAggregateUploadHash returns the content hash of the last rollup the
// cloud accepted for (businessDate, tillID); ok is false when none was.
func (r *POSRepo) SalesAggregateUploadHash(ctx context.Context, businessDate, tillID string) (string, bool, error) {
	var h string
	err := r.db.QueryRowContext(ctx,
		`SELECT content_hash FROM sales_aggregate_uploads WHERE business_date = ? AND till_id = ?`,
		businessDate, tillID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("sales aggregate upload hash: %w", err)
	}
	return h, true, nil
}

// RecordSalesAggregateUpload upserts the ledger row for a rollup the cloud
// accepted (HTTP 200) — call it only after a successful upload.
func (r *POSRepo) RecordSalesAggregateUpload(ctx context.Context, businessDate, tillID, hash string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO sales_aggregate_uploads (business_date, till_id, content_hash, uploaded_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (business_date, till_id) DO UPDATE SET content_hash = excluded.content_hash, uploaded_at = excluded.uploaded_at`,
		businessDate, tillID, hash, at.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("record sales aggregate upload: %w", err)
	}
	return nil
}

// PruneSalesAggregateUploads deletes ledger rows for business dates before
// `before` (YYYY-MM-DD, exclusive) and returns how many went.
func (r *POSRepo) PruneSalesAggregateUploads(ctx context.Context, before string) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM sales_aggregate_uploads WHERE business_date < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("prune sales aggregate uploads: %w", err)
	}
	return res.RowsAffected()
}
