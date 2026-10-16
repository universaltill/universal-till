package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CloudLink is one cloud_links row (ADR-0162 §4, ut-docs#4463): which local
// record the cloud catalogue row (Entity, CloudID) became, the feed version
// last applied to it and the resync generation that last saw it.
type CloudLink struct {
	Entity  string
	CloudID string
	LocalID string
	Version int64
	Gen     int64
}

// CloudLinkRepo reads and writes cloud_links and cloud_apply_failures
// (migration 082). Stateless like ImportSourceLinkRepo — every call names
// its executor, so the pull's chunk transaction sees its own writes.
type CloudLinkRepo struct{}

// NewCloudLinkRepo returns the repository.
func NewCloudLinkRepo() *CloudLinkRepo { return &CloudLinkRepo{} }

// LocalID returns the local record linked to (entity, cloudID).
func (*CloudLinkRepo) LocalID(ctx context.Context, q SyncQuerier, entity, cloudID string) (string, bool, error) {
	var id string
	err := q.QueryRowContext(ctx,
		`SELECT local_id FROM cloud_links WHERE entity = ? AND cloud_id = ?`, entity, cloudID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("cloud link: %w", err)
	}
	return id, true, nil
}

// Upsert records (or re-points and re-stamps) a link.
func (*CloudLinkRepo) Upsert(ctx context.Context, q SyncQuerier, l CloudLink) error {
	if l.Entity == "" || l.CloudID == "" || l.LocalID == "" {
		return errors.New("cloud link: entity, cloud_id and local_id are required")
	}
	_, err := q.ExecContext(ctx, `
INSERT INTO cloud_links (entity, cloud_id, local_id, version, gen, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(entity, cloud_id) DO UPDATE SET
    local_id = excluded.local_id, version = excluded.version,
    gen = excluded.gen, updated_at = excluded.updated_at`,
		l.Entity, l.CloudID, l.LocalID, l.Version, l.Gen, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("cloud link upsert: %w", err)
	}
	return nil
}

// Touch stamps an existing link with gen (a row the pull saw but did not
// apply, e.g. a refused one, still exists in the cloud). No link: no-op.
func (*CloudLinkRepo) Touch(ctx context.Context, q SyncQuerier, entity, cloudID string, gen int64) error {
	if _, err := q.ExecContext(ctx, `UPDATE cloud_links SET gen = ? WHERE entity = ? AND cloud_id = ?`, gen, entity, cloudID); err != nil {
		return fmt.Errorf("cloud link touch: %w", err)
	}
	return nil
}

// Delete removes a link (no-op when absent).
func (*CloudLinkRepo) Delete(ctx context.Context, q SyncQuerier, entity, cloudID string) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM cloud_links WHERE entity = ? AND cloud_id = ?`, entity, cloudID); err != nil {
		return fmt.Errorf("cloud link delete: %w", err)
	}
	return nil
}

// Stale returns up to limit links whose gen is older than gen — after a
// resync's full pull, the rows the cloud no longer returned.
func (*CloudLinkRepo) Stale(ctx context.Context, q SyncQuerier, gen int64, limit int) ([]CloudLink, error) {
	rows, err := q.QueryContext(ctx, `
SELECT entity, cloud_id, local_id, version, gen FROM cloud_links
WHERE gen < ? ORDER BY entity, cloud_id LIMIT ?`, gen, limit)
	if err != nil {
		return nil, fmt.Errorf("cloud links stale: %w", err)
	}
	defer rows.Close()
	var out []CloudLink
	for rows.Next() {
		var l CloudLink
		if err := rows.Scan(&l.Entity, &l.CloudID, &l.LocalID, &l.Version, &l.Gen); err != nil {
			return nil, fmt.Errorf("cloud links stale: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// RecordFailure upserts the latest refusal of (entity, cloudID).
func (*CloudLinkRepo) RecordFailure(ctx context.Context, q SyncQuerier, entity, cloudID string, version int64, reason string) error {
	_, err := q.ExecContext(ctx, `
INSERT INTO cloud_apply_failures (entity, cloud_id, version, reason, at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(entity, cloud_id) DO UPDATE SET
    version = excluded.version, reason = excluded.reason, at = excluded.at`,
		entity, cloudID, version, reason, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("cloud apply failure: %w", err)
	}
	return nil
}

// ClearFailure drops a recorded refusal once a later version applied.
func (*CloudLinkRepo) ClearFailure(ctx context.Context, q SyncQuerier, entity, cloudID string) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM cloud_apply_failures WHERE entity = ? AND cloud_id = ?`, entity, cloudID); err != nil {
		return fmt.Errorf("cloud apply failure clear: %w", err)
	}
	return nil
}
