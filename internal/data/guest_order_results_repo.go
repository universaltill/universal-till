package data

// Guest order results (ut-docs#4337, ADR-0159 §4/§5, migration 078): the
// result of each guest_order directive the main till applied, keyed by the
// cloud's GuestOrder id. cloudsync saves a result before posting it, reads
// it back for a re-delivered directive (idempotency: the same display_no
// and total, no second apply), and re-posts every unacknowledged row on
// later check-ins until the cloud answers 200 or 404.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// guestOrderResultPostedRetention / guestOrderResultUnpostedRetention bound
// the table (Prune). The cloud stops delivering a guest_order after 10
// minutes and deletes the GuestOrder 24 h after it turns terminal, so a
// posted row is useless after a day and an unposted one could only 404
// after two.
const (
	guestOrderResultPostedRetention   = 24 * time.Hour
	guestOrderResultUnpostedRetention = 48 * time.Hour
)

// GuestOrderResult is one stored guest_order outcome. Status is the
// directive result status ("applied" or "failed"); Message the refusal
// code on failure. DisplayNo/TotalMinor/Currency are the result's data
// object on success (TotalMinor in integer minor units).
type GuestOrderResult struct {
	GuestOrderID string
	DirectiveID  string
	Status       string
	Message      string
	DisplayNo    string
	TotalMinor   int64
	Currency     string
	Posted       bool
	CreatedAt    string
}

// GuestOrderResultsRepo owns all SQL for guest_order_results.
type GuestOrderResultsRepo struct {
	db *sql.DB
}

func NewGuestOrderResultsRepo(db *sql.DB) *GuestOrderResultsRepo {
	return &GuestOrderResultsRepo{db: db}
}

// Save records r unposted. First write wins: a row already stored for the
// same GuestOrderID is kept as it is, because a re-delivery must report the
// FIRST result. It also prunes (Prune) in the same call, so the table stays
// bounded without a sweeper of its own.
func (r *GuestOrderResultsRepo) Save(ctx context.Context, res GuestOrderResult) error {
	if res.GuestOrderID == "" {
		return errors.New("save guest order result: empty guest order id")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO guest_order_results (guest_order_id, directive_id, status, message, display_no, total_minor, currency, posted, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)
ON CONFLICT(guest_order_id) DO NOTHING`,
		res.GuestOrderID, res.DirectiveID, res.Status, res.Message, res.DisplayNo, res.TotalMinor, res.Currency, now); err != nil {
		return fmt.Errorf("save guest order result: %w", err)
	}
	return r.Prune(ctx)
}

// Get returns the stored result for guestOrderID; found=false when none.
func (r *GuestOrderResultsRepo) Get(ctx context.Context, guestOrderID string) (GuestOrderResult, bool, error) {
	var g GuestOrderResult
	var posted int
	err := r.db.QueryRowContext(ctx, `
SELECT guest_order_id, directive_id, status, message, display_no, total_minor, currency, posted, created_at
FROM guest_order_results WHERE guest_order_id = ?`, guestOrderID).
		Scan(&g.GuestOrderID, &g.DirectiveID, &g.Status, &g.Message, &g.DisplayNo, &g.TotalMinor, &g.Currency, &posted, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return GuestOrderResult{}, false, nil
	}
	if err != nil {
		return GuestOrderResult{}, false, fmt.Errorf("get guest order result: %w", err)
	}
	g.Posted = posted == 1
	return g, true, nil
}

// ListUnposted returns up to limit results the cloud has not acknowledged,
// oldest first.
func (r *GuestOrderResultsRepo) ListUnposted(ctx context.Context, limit int) ([]GuestOrderResult, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT guest_order_id, directive_id, status, message, display_no, total_minor, currency, created_at
FROM guest_order_results WHERE posted = 0 ORDER BY created_at, guest_order_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list unposted guest order results: %w", err)
	}
	defer rows.Close()
	var out []GuestOrderResult
	for rows.Next() {
		var g GuestOrderResult
		if err := rows.Scan(&g.GuestOrderID, &g.DirectiveID, &g.Status, &g.Message, &g.DisplayNo, &g.TotalMinor, &g.Currency, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan unposted guest order result: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// MarkPosted records that the cloud acknowledged guestOrderID's result
// (200, or 404 for an order it no longer holds).
func (r *GuestOrderResultsRepo) MarkPosted(ctx context.Context, guestOrderID string) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE guest_order_results SET posted = 1 WHERE guest_order_id = ?`, guestOrderID); err != nil {
		return fmt.Errorf("mark guest order result posted: %w", err)
	}
	return nil
}

// Prune drops posted rows older than a day and unposted rows older than
// two (see the retention constants).
func (r *GuestOrderResultsRepo) Prune(ctx context.Context) error {
	now := time.Now().UTC()
	if _, err := r.db.ExecContext(ctx, `
DELETE FROM guest_order_results
WHERE (posted = 1 AND created_at < ?) OR (posted = 0 AND created_at < ?)`,
		now.Add(-guestOrderResultPostedRetention).Format(time.RFC3339),
		now.Add(-guestOrderResultUnpostedRetention).Format(time.RFC3339)); err != nil {
		return fmt.Errorf("prune guest order results: %w", err)
	}
	return nil
}
