package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SyncQuerier is what the catalogue-sync repository methods read through:
// a *sql.DB for a preview (reads only), the run's *sql.Tx for a commit, so
// every read sees the writes earlier rows of the same run made (a test DB
// has one connection — a read on the pool inside the tx would deadlock).
type SyncQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ImportSourceLinkRepo reads and writes import_source_links (ADR-0161 §5,
// ut-docs#4429): which local record a foreign (source, entity,
// external_id) became. Stateless — every call names its executor.
type ImportSourceLinkRepo struct{}

// NewImportSourceLinkRepo returns the repository.
func NewImportSourceLinkRepo() *ImportSourceLinkRepo { return &ImportSourceLinkRepo{} }

// LocalID returns the local record linked to (source, entity, externalID).
func (*ImportSourceLinkRepo) LocalID(ctx context.Context, q SyncQuerier, source, entity, externalID string) (string, bool, error) {
	var id string
	err := q.QueryRowContext(ctx,
		`SELECT local_id FROM import_source_links WHERE source = ? AND entity = ? AND external_id = ?`,
		source, entity, externalID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("import link: %w", err)
	}
	return id, true, nil
}

// ExternalIDFor is the reverse lookup: the external id of source that is
// already linked to localID, if any.
func (*ImportSourceLinkRepo) ExternalIDFor(ctx context.Context, q SyncQuerier, source, entity, localID string) (string, bool, error) {
	var ext string
	err := q.QueryRowContext(ctx,
		`SELECT external_id FROM import_source_links WHERE source = ? AND entity = ? AND local_id = ? ORDER BY external_id LIMIT 1`,
		source, entity, localID).Scan(&ext)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("import link reverse: %w", err)
	}
	return ext, true, nil
}

// Upsert records (or re-points) the link inside the run's transaction.
func (*ImportSourceLinkRepo) Upsert(ctx context.Context, tx *sql.Tx, source, entity, externalID, localID string) error {
	if source == "" || entity == "" || externalID == "" || localID == "" {
		return errors.New("import link: every field is required")
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO import_source_links (source, entity, external_id, local_id, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(source, entity, external_id) DO UPDATE SET local_id = excluded.local_id, updated_at = excluded.updated_at`,
		source, entity, externalID, localID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("import link upsert: %w", err)
	}
	return nil
}
