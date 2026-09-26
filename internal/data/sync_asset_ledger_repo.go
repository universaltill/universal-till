package data

import (
	"context"
	"database/sql"
	"fmt"
)

// SyncAssetLedgerRepo is the replica's record of the asset files it
// downloaded from the main till (ut-docs#2785, migration 050). The asset
// pull prunes only files recorded here, so a photo uploaded on the replica
// itself is never touched.
type SyncAssetLedgerRepo struct{ db *sql.DB }

func NewSyncAssetLedgerRepo(db *sql.DB) *SyncAssetLedgerRepo { return &SyncAssetLedgerRepo{db: db} }

// SyncAssetLedgerRow is one downloaded file. Size and Mod are the local
// file as downloaded (Mod in unix seconds); UnreferencedSince is when the
// main's manifest first stopped listing it (unix seconds, 0 = listed) and
// Misses how many consecutive complete manifests have missed it.
type SyncAssetLedgerRow struct {
	Path              string
	Size              int64
	Mod               int64
	UnreferencedSince int64
	Misses            int
}

// SyncAssetLedgerBatch is one pull's changes to one scope, applied in a
// single transaction (Apply): Record upserts a downloaded (or relisted)
// file and clears its miss; Miss counts one more complete manifest that did
// not list the path, stamping MissAt as the first-miss time if none is set;
// Delete forgets a path (pruned, gone, or never ours to touch).
type SyncAssetLedgerBatch struct {
	Record []SyncAssetLedgerRow
	Miss   []string
	MissAt int64
	Delete []string
}

// Empty reports whether the batch has nothing to write.
func (b SyncAssetLedgerBatch) Empty() bool {
	return len(b.Record) == 0 && len(b.Miss) == 0 && len(b.Delete) == 0
}

// List returns every row of one scope, keyed by path.
func (r *SyncAssetLedgerRepo) List(ctx context.Context, scope string) (map[string]SyncAssetLedgerRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT path, size, mod, COALESCE(unreferenced_since, 0), unreferenced_misses FROM sync_asset_ledger WHERE scope = ?`, scope)
	if err != nil {
		return nil, fmt.Errorf("list sync asset ledger: %w", err)
	}
	defer rows.Close()
	out := map[string]SyncAssetLedgerRow{}
	for rows.Next() {
		var row SyncAssetLedgerRow
		if err := rows.Scan(&row.Path, &row.Size, &row.Mod, &row.UnreferencedSince, &row.Misses); err != nil {
			return nil, fmt.Errorf("scan sync asset ledger: %w", err)
		}
		out[row.Path] = row
	}
	return out, rows.Err()
}

const syncAssetLedgerRecordSQL = `
INSERT INTO sync_asset_ledger (scope, path, size, mod, unreferenced_since, unreferenced_misses) VALUES (?, ?, ?, ?, NULL, 0)
ON CONFLICT (scope, path) DO UPDATE SET size = excluded.size, mod = excluded.mod, unreferenced_since = NULL, unreferenced_misses = 0`

// Record upserts a downloaded file and clears its unreferenced mark.
func (r *SyncAssetLedgerRepo) Record(ctx context.Context, scope, path string, size, mod int64) error {
	if _, err := r.db.ExecContext(ctx, syncAssetLedgerRecordSQL, scope, path, size, mod); err != nil {
		return fmt.Errorf("record sync asset: %w", err)
	}
	return nil
}

// Apply writes one pull's changes to a scope in a single transaction.
func (r *SyncAssetLedgerRepo) Apply(ctx context.Context, scope string, b SyncAssetLedgerBatch) error {
	if b.Empty() {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("apply sync asset ledger: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, row := range b.Record {
		if _, err := tx.ExecContext(ctx, syncAssetLedgerRecordSQL, scope, row.Path, row.Size, row.Mod); err != nil {
			return fmt.Errorf("record sync asset: %w", err)
		}
	}
	for _, p := range b.Miss {
		if _, err := tx.ExecContext(ctx, `
UPDATE sync_asset_ledger SET unreferenced_misses = unreferenced_misses + 1,
    unreferenced_since = COALESCE(unreferenced_since, ?)
WHERE scope = ? AND path = ?`, b.MissAt, scope, p); err != nil {
			return fmt.Errorf("mark sync asset unreferenced: %w", err)
		}
	}
	for _, p := range b.Delete {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sync_asset_ledger WHERE scope = ? AND path = ?`, scope, p); err != nil {
			return fmt.Errorf("delete sync asset ledger row: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("apply sync asset ledger: %w", err)
	}
	return nil
}
