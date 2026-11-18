package data

// Online channels report (ADR-0169 §9, ut-docs#5427): per-period totals of
// the channel orders recorded on this (main) till, for Reports → Online
// channels. Read-only; never joined to sales/payments, so no till figure can
// change because of it.

import (
	"context"
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

// channelUnpaidCancelled is the SQL predicate for an order cancelled before
// money was taken: not counted (its total_minor was never received and its
// refund_total_minor is usually 0). A cancelled order that was paid is
// counted; its refund nets it out. payment_state values: the closed set in
// cloudsync.channelPaymentStates, plus ” (none sent).
const channelUnpaidCancelled = `status = 'cancelled' AND payment_state IN ('', 'pending', 'authorized', 'voided')`

// ChannelSummaryRow is one (channel, currency) line, or a per-currency total.
type ChannelSummaryRow struct {
	PluginKey string // "" on a total row
	Currency  string
	Orders    int
	Gross     money.Money // SUM(total_minor)
	Refunds   money.Money // SUM(refund_total_minor): refunded so far, dated by placed_at
	Net       money.Money // Gross - Refunds
}

// ChannelCurrencyGroup is every channel row in one currency. Total is set
// only when the currency has two or more rows. Amounts are never added
// across currencies.
type ChannelCurrencyGroup struct {
	Currency string
	Rows     []ChannelSummaryRow
	Total    *ChannelSummaryRow
}

// ChannelSummary is the Online channels report for one window.
type ChannelSummary struct {
	Groups          []ChannelCurrencyGroup // currency ASC, rows plugin_key ASC
	CancelledUnpaid int                    // orders in the window excluded by channelUnpaidCancelled
}

// SummaryByWindow totals the counted orders whose placed_at is in [from, to),
// with the same instant compare as RefundsByWindow (windowArgs +
// datetime(...)), so an order on a boundary falls in the same period as a
// till sale at that instant. placed_at is RFC 3339 UTC with a Z, which
// datetime() normalises (fractional seconds are truncated).
//
// Reporting definitions (accepted, ut-docs#5427): an order cancelled before
// payment is not counted but is reported as a count; refunds are dated by the
// order's placed_at, not the day the refund happened. datetime(placed_at)
// cannot use idx_channel_orders_placed; accepted: one shop's online orders,
// read only when the tab is opened.
func (r *ChannelOrdersRepo) SummaryByWindow(ctx context.Context, from, to time.Time) (ChannelSummary, error) {
	var sum ChannelSummary
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT plugin_key, currency, COUNT(*),
       COALESCE(SUM(total_minor), 0), COALESCE(SUM(refund_total_minor), 0)
FROM channel_orders
WHERE datetime(placed_at) >= datetime(?) AND datetime(placed_at) < datetime(?)
  AND NOT (`+channelUnpaidCancelled+`)
GROUP BY plugin_key, currency
ORDER BY currency, plugin_key`, fromStr, toStr)
	if err != nil {
		return ChannelSummary{}, fmt.Errorf("channel summary: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row ChannelSummaryRow
		var gross, refunds int64
		if err := rows.Scan(&row.PluginKey, &row.Currency, &row.Orders, &gross, &refunds); err != nil {
			return ChannelSummary{}, fmt.Errorf("channel summary: %w", err)
		}
		row.Gross = money.FromMinor(gross)
		row.Refunds = money.FromMinor(refunds)
		row.Net = row.Gross.Sub(row.Refunds)
		if n := len(sum.Groups); n == 0 || sum.Groups[n-1].Currency != row.Currency {
			sum.Groups = append(sum.Groups, ChannelCurrencyGroup{Currency: row.Currency})
		}
		g := &sum.Groups[len(sum.Groups)-1]
		g.Rows = append(g.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return ChannelSummary{}, fmt.Errorf("channel summary: %w", err)
	}
	for i := range sum.Groups {
		g := &sum.Groups[i]
		if len(g.Rows) < 2 {
			continue
		}
		tot := ChannelSummaryRow{Currency: g.Currency}
		for _, row := range g.Rows {
			tot.Orders += row.Orders
			tot.Gross = tot.Gross.Add(row.Gross)
			tot.Refunds = tot.Refunds.Add(row.Refunds)
			tot.Net = tot.Net.Add(row.Net)
		}
		g.Total = &tot
	}
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM channel_orders
WHERE datetime(placed_at) >= datetime(?) AND datetime(placed_at) < datetime(?)
  AND `+channelUnpaidCancelled, fromStr, toStr).Scan(&sum.CancelledUnpaid); err != nil {
		return ChannelSummary{}, fmt.Errorf("channel summary: %w", err)
	}
	return sum, nil
}

// HasAny reports whether this till has ever recorded a channel order (the
// Reports tab is hidden until it has).
func (r *ChannelOrdersRepo) HasAny(ctx context.Context) (bool, error) {
	var has bool
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM channel_orders)`).Scan(&has); err != nil {
		return false, fmt.Errorf("channel orders has any: %w", err)
	}
	return has, nil
}
