package data

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/money"
)

// Shrinkage events (ut-docs#1465, G41): structured void/comp/waste
// management for a PRE-tender basket-line removal — distinct from a
// post-tender refund (G27, RefundsByWindow above), which returns a
// completed sale. Split into its own file rather than growing pos_repo.go
// (already 8000+ lines) further, following the same "new file once the
// natural home is already huge" call this repo's own CLAUDE.md describes
// for a ModifierRepo-style split — but still a *POSRepo method, not a new
// repo type: this is the same repository POSRepo already owns
// InsertAudit/RefundsByWindow/TopItems on, just physically split by file.

// InsertShrinkageEvent records one shrinkage_events row (035_shrinkage_
// events.sql). All-args-explicit and transaction-aware, mirroring
// InsertAudit's own parameter style (POSRepo.InsertAudit,
// internal/pages/refund_page.go's call sites) — tx may be nil (a direct,
// non-transactional write, exec(nil) resolves to r.db).
//
// unitPrice/extendedValue are money.Money — converted to raw minor-unit
// int64 here, at the DB boundary, via Minor() (internal/money's own
// documented convention). approverID is empty when the session user
// already held void_comp_waste directly (no PIN elevation); actorID is
// never empty (the session user is always known by the time a line is
// actually removed). id empty generates a fresh uuid, same as
// insertAudit does.
func (r *POSRepo) InsertShrinkageEvent(
	ctx context.Context, tx *sql.Tx,
	reasonCategory, itemID, itemName, sku string,
	quantity float64,
	unitPrice, extendedValue money.Money,
	actorID, approverID, note, orderType, registerID string,
	createdAt, id string,
) error {
	if id == "" {
		id = uuid.NewString()
	}
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO shrinkage_events (
    id, reason_category, item_id, item_name, sku, quantity,
    unit_price_minor, extended_value_minor, actor_id, approver_id, note,
    order_type, register_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, id, reasonCategory, nullIfEmpty(itemID), itemName, nullIfEmpty(sku), quantity,
		unitPrice.Minor(), extendedValue.Minor(), nullIfEmpty(actorID), nullIfEmpty(approverID), nullIfEmpty(note),
		orderType, nullIfEmpty(registerID), createdAt)
	if err != nil {
		return fmt.Errorf("insert shrinkage_events: %w", err)
	}
	return nil
}

// ShrinkageReasonTotal is one reason category's grouped total over a
// reporting window — mirrors MethodTotal's own {label, count, amount}
// shape (PaymentBreakdown, above in pos_repo.go).
type ShrinkageReasonTotal struct {
	ReasonCategory string `json:"reasonCategory"`
	Count          int    `json:"count"`
	Total          int64  `json:"total"`
}

// ShrinkageByReason sums shrinkage_events.extended_value_minor grouped by
// reason_category over [from, to) — the Shrinkage & Loss report tab's
// per-category totals. Mirrors RefundsByWindow/DiscountsByWindow's own
// windowArgs + datetime(...) half-open comparison. An empty window (no
// events at all, or none in range) returns an empty slice, never an
// error — same zero-window contract every other *ByWindow method in this
// package already has.
func (r *POSRepo) ShrinkageByReason(ctx context.Context, from, to time.Time) ([]ShrinkageReasonTotal, error) {
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT reason_category, COUNT(*), COALESCE(SUM(extended_value_minor), 0)
FROM shrinkage_events
WHERE datetime(created_at) >= datetime(?) AND datetime(created_at) < datetime(?)
GROUP BY reason_category ORDER BY reason_category`, fromStr, toStr)
	if err != nil {
		return nil, fmt.Errorf("shrinkage by reason: %w", err)
	}
	defer rows.Close()
	var out []ShrinkageReasonTotal
	for rows.Next() {
		var t ShrinkageReasonTotal
		if err := rows.Scan(&t.ReasonCategory, &t.Count, &t.Total); err != nil {
			return nil, fmt.Errorf("scan shrinkage reason total: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ShrinkageTopItems returns the top N items by shrinkage value (extended
// value, summed across every reason category) over [from, to) — mirrors
// TopItems' own shape and signature style (same file's sibling report,
// pos_repo.go), grouped by item_name (the denormalized snapshot, same as
// TopItems groups by sale_lines.name_snapshot — an item can be renamed or
// deleted after the fact, and this report must still read sensibly).
func (r *POSRepo) ShrinkageTopItems(ctx context.Context, from, to time.Time, limit int) ([]TopItem, error) {
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT item_name, SUM(quantity), COALESCE(SUM(extended_value_minor), 0) AS value
FROM shrinkage_events
WHERE datetime(created_at) >= datetime(?) AND datetime(created_at) < datetime(?)
GROUP BY item_name ORDER BY value DESC LIMIT ?`, fromStr, toStr, limit)
	if err != nil {
		return nil, fmt.Errorf("shrinkage top items: %w", err)
	}
	defer rows.Close()
	var out []TopItem
	for rows.Next() {
		var t TopItem
		if err := rows.Scan(&t.Name, &t.Qty, &t.Revenue); err != nil {
			return nil, fmt.Errorf("scan shrinkage top item: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
