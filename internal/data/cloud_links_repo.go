package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

// HeldByOther reports whether a link of entity other than cloudID points
// at localID (idx_cloud_links_local) — two cloud rows claiming one local
// record, which the cloud pull refuses for a barcode (ut-docs#4521).
func (*CloudLinkRepo) HeldByOther(ctx context.Context, q SyncQuerier, entity, localID, cloudID string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM cloud_links WHERE entity = ? AND local_id = ? AND cloud_id <> ? LIMIT 1`, entity, localID, cloudID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cloud link: %w", err)
	}
	return true, nil
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

// RecordRetired notes a local record the pull could only retire (an option
// set a cloud tombstone found in use, ut-docs#4522): its delete is retried
// on every chunk commit until the record is gone (migration 083).
func (*CloudLinkRepo) RecordRetired(ctx context.Context, q SyncQuerier, entity, localID, cloudID string) error {
	_, err := q.ExecContext(ctx, `
INSERT INTO cloud_retired (entity, local_id, cloud_id, retired_at) VALUES (?, ?, ?, ?)
ON CONFLICT(entity, local_id) DO UPDATE SET cloud_id = excluded.cloud_id, retired_at = excluded.retired_at`,
		entity, localID, cloudID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("cloud retired record: %w", err)
	}
	return nil
}

// CloudRetired is one cloud_retired row.
type CloudRetired struct {
	LocalID, CloudID string
}

// Retired returns entity's retired records, oldest first.
func (*CloudLinkRepo) Retired(ctx context.Context, q SyncQuerier, entity string) ([]CloudRetired, error) {
	rows, err := q.QueryContext(ctx, `SELECT local_id, cloud_id FROM cloud_retired WHERE entity = ? ORDER BY retired_at, local_id`, entity)
	if err != nil {
		return nil, fmt.Errorf("cloud retired: %w", err)
	}
	defer rows.Close()
	var out []CloudRetired
	for rows.Next() {
		var r CloudRetired
		if err := rows.Scan(&r.LocalID, &r.CloudID); err != nil {
			return nil, fmt.Errorf("cloud retired: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DropRetired removes a retired record (no-op when absent).
func (*CloudLinkRepo) DropRetired(ctx context.Context, q SyncQuerier, entity, localID string) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM cloud_retired WHERE entity = ? AND local_id = ?`, entity, localID); err != nil {
		return fmt.Errorf("cloud retired drop: %w", err)
	}
	return nil
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

// CloudApplyFailure is one cloud_apply_failures row: the latest refusal of
// a pulled cloud row (Entity is the feed's kind; At is RFC 3339 UTC).
type CloudApplyFailure struct {
	Entity  string
	CloudID string
	Version int64
	Reason  string
	At      string
}

// ListFailures returns up to limit recorded refusals, newest At first
// (ties by entity, cloud_id) — the set the till reports to the cloud
// (ut-docs#4493).
func (*CloudLinkRepo) ListFailures(ctx context.Context, q SyncQuerier, limit int) ([]CloudApplyFailure, error) {
	rows, err := q.QueryContext(ctx, `
SELECT entity, cloud_id, version, reason, at FROM cloud_apply_failures
ORDER BY at DESC, entity, cloud_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("cloud apply failures: %w", err)
	}
	defer rows.Close()
	var out []CloudApplyFailure
	for rows.Next() {
		var f CloudApplyFailure
		if err := rows.Scan(&f.Entity, &f.CloudID, &f.Version, &f.Reason, &f.At); err != nil {
			return nil, fmt.Errorf("cloud apply failures: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SeedTaxCodeIDs are the tax codes migration 001 seeds into every new till
// (Standard VAT, Reduced VAT, Zero-rated). A cloud-master store's pull
// adopts one of them when the cloud sends a tax code of the same name
// (ADR-0162 §4, ut-docs#4494): the setup wizard runs before the till can
// be paired, so the seed rows are already there, and tax_codes.name is
// UNIQUE. Only these ids are ever adopted by name — any other local row of
// the same name stays a refusal (general adoption is ut-docs#4465).
var SeedTaxCodeIDs = []string{"tax_std", "tax_red", "tax_zero"}

// AdoptableSeedTaxCode returns the seed tax code (SeedTaxCodeIDs) named
// exactly name that no cloud_links row of entity points at, so a cloud row
// of that name can take it over instead of being refused on the UNIQUE
// name. The comparison is the column's own (BINARY collation): exactly the
// names the UNIQUE constraint would call equal. ok=false: none.
func (*CloudLinkRepo) AdoptableSeedTaxCode(ctx context.Context, q SyncQuerier, entity, name string) (string, bool, error) {
	args := []any{name}
	for _, id := range SeedTaxCodeIDs {
		args = append(args, id)
	}
	args = append(args, entity)
	var id string
	err := q.QueryRowContext(ctx, `
SELECT t.id FROM tax_codes t
WHERE t.name = ? AND t.id IN (?`+strings.Repeat(", ?", len(SeedTaxCodeIDs)-1)+`)
  AND NOT EXISTS (SELECT 1 FROM cloud_links l WHERE l.entity = ? AND l.local_id = t.id)`,
		args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("adoptable seed tax code: %w", err)
	}
	return id, true, nil
}
