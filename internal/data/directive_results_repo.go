package data

// Directive results (ut-docs#4797, migration 088): the outcome of each
// generic cloud directive the till applied, keyed by the cloud's directive
// id. cloudsync saves a result before posting it and, for a directive the
// cloud delivers again because the post failed, re-posts the stored result
// instead of applying a second time (a stock adjustment must book once).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// directiveResultPostedRetention / directiveResultUnpostedRetention bound
// the table (Prune). The cloud stops delivering a directive once its result
// is acknowledged, so a posted row is only a safety margin; an unposted row
// is one the cloud never acknowledged, and 30 days bounds it.
const (
	directiveResultPostedRetention   = 7 * 24 * time.Hour
	directiveResultUnpostedRetention = 30 * 24 * time.Hour
)

// DirectiveResult is one stored directive outcome. Status is "applied" or
// "failed"; Message the hook's message or refusal.
type DirectiveResult struct {
	DirectiveID   string
	DirectiveType string
	Status        string
	Message       string
	Posted        bool
	CreatedAt     string
}

// DirectiveResultsRepo owns all SQL for directive_results.
type DirectiveResultsRepo struct {
	db *sql.DB
}

func NewDirectiveResultsRepo(db *sql.DB) *DirectiveResultsRepo {
	return &DirectiveResultsRepo{db: db}
}

// Save records r unposted. First write wins: a row already stored for the
// same DirectiveID is kept, because a re-delivery must report the FIRST
// result. It also prunes, so the table stays bounded without a sweeper.
func (r *DirectiveResultsRepo) Save(ctx context.Context, res DirectiveResult) error {
	if res.DirectiveID == "" {
		return errors.New("save directive result: empty directive id")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO directive_results (directive_id, directive_type, status, message, posted, created_at)
VALUES (?, ?, ?, ?, 0, ?)
ON CONFLICT(directive_id) DO NOTHING`,
		res.DirectiveID, res.DirectiveType, res.Status, res.Message, now); err != nil {
		return fmt.Errorf("save directive result: %w", err)
	}
	// The row is stored; a Prune failure must not read as a lost result
	// (the caller would log a false "re-delivery would re-apply it").
	// Pruning is retried on the next Save.
	if err := r.Prune(ctx); err != nil {
		logging.L().Warnf("directive results: prune: %v", err)
	}
	return nil
}

// Get returns the stored result for directiveID; found=false when none.
func (r *DirectiveResultsRepo) Get(ctx context.Context, directiveID string) (DirectiveResult, bool, error) {
	var g DirectiveResult
	var posted int
	err := r.db.QueryRowContext(ctx, `
SELECT directive_id, directive_type, status, message, posted, created_at
FROM directive_results WHERE directive_id = ?`, directiveID).
		Scan(&g.DirectiveID, &g.DirectiveType, &g.Status, &g.Message, &posted, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DirectiveResult{}, false, nil
	}
	if err != nil {
		return DirectiveResult{}, false, fmt.Errorf("get directive result: %w", err)
	}
	g.Posted = posted == 1
	return g, true, nil
}

// MarkPosted records that the cloud acknowledged directiveID's result.
func (r *DirectiveResultsRepo) MarkPosted(ctx context.Context, directiveID string) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE directive_results SET posted = 1 WHERE directive_id = ?`, directiveID); err != nil {
		return fmt.Errorf("mark directive result posted: %w", err)
	}
	return nil
}

// Prune drops posted rows older than 7 days and unposted rows older than 30
// (see the retention constants).
func (r *DirectiveResultsRepo) Prune(ctx context.Context) error {
	now := time.Now().UTC()
	if _, err := r.db.ExecContext(ctx, `
DELETE FROM directive_results
WHERE (posted = 1 AND created_at < ?) OR (posted = 0 AND created_at < ?)`,
		now.Add(-directiveResultPostedRetention).Format(time.RFC3339),
		now.Add(-directiveResultUnpostedRetention).Format(time.RFC3339)); err != nil {
		return fmt.Errorf("prune directive results: %w", err)
	}
	return nil
}
