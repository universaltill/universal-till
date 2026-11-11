package data

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// SyncStockRepo serves LAN sync increment D3b (ADR-0011): the primary is the
// authoritative stock ledger (every till's sale journal lands there), and
// replicas periodically adopt its on-hand LEVELS so all tills display the
// same shop-wide numbers. Levels, not movements: a level snapshot is
// idempotent to apply and needs no cross-till movement dedup.
type SyncStockRepo struct{ db *POSRepo }

func NewSyncStockRepo(pos *POSRepo) *SyncStockRepo { return &SyncStockRepo{db: pos} }

// StockLevelRow is one item's (or variant's) shop-wide on-hand quantity.
type StockLevelRow struct {
	ItemID    string  `json:"item_id,omitempty"`
	VariantID string  `json:"variant_id,omitempty"`
	Quantity  float64 `json:"quantity"`
}

// StockBundle is the wire payload of GET /api/sync/stock.
type StockBundle struct {
	Rows []StockLevelRow `json:"rows"`
}

// Fingerprint identifies the bundle content (rows are ordered by key).
func (b StockBundle) Fingerprint() string {
	raw, _ := json.Marshal(b.Rows)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// DumpStock aggregates on-hand per item/variant across every location —
// the shop-wide truth on the primary, and the local view on a replica.
func (r *SyncStockRepo) DumpStock(ctx context.Context) (StockBundle, error) {
	return dumpStock(ctx, r.db.db)
}

type rowsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func dumpStock(ctx context.Context, q rowsQuerier) (StockBundle, error) {
	rows, err := q.QueryContext(ctx, `
SELECT COALESCE(item_id, ''), COALESCE(variant_id, ''), SUM(quantity)
FROM inventory
GROUP BY COALESCE(item_id, ''), COALESCE(variant_id, '')
ORDER BY COALESCE(item_id, ''), COALESCE(variant_id, '')`)
	if err != nil {
		return StockBundle{}, fmt.Errorf("dump stock: %w", err)
	}
	defer rows.Close()
	bundle := StockBundle{Rows: []StockLevelRow{}}
	for rows.Next() {
		var row StockLevelRow
		if err := rows.Scan(&row.ItemID, &row.VariantID, &row.Quantity); err != nil {
			return StockBundle{}, fmt.Errorf("dump stock: %w", err)
		}
		bundle.Rows = append(bundle.Rows, row)
	}
	return bundle, rows.Err()
}

// ApplyStockLevels reconciles this till's on-hand to the primary's: for each
// key where the local total differs, one corrective `adjust` movement lands
// in locationID (the till's default location), through the normal movement
// path so inventory, the movement ledger, and the audit log stay consistent.
// Keys the primary doesn't mention are corrected to zero. Idempotent: a
// second apply of the same bundle produces no corrections. The CALLER must
// ensure the till's own sale journal has been fully pushed first — otherwise
// the primary hasn't seen our latest sales and the correction would briefly
// re-add sold stock.
//
// Everything runs in ONE transaction, atomic with the sync.stock_version
// upsert (when version != ""), and refuses with ErrNotReplica, writing
// nothing, when this till has no sync.primary_url: a promote that commits
// while the stock answer is in flight must win (ut-docs#5903). On any error
// nothing is kept and 0 is returned.
func (r *SyncStockRepo) ApplyStockLevels(ctx context.Context, bundle StockBundle, locationID, version string) (int, error) {
	tx, err := r.db.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("stock sync: %w", err)
	}
	defer tx.Rollback()
	if err := requireReplica(ctx, tx); err != nil {
		return 0, err
	}
	local, err := dumpStock(ctx, tx)
	if err != nil {
		return 0, err
	}
	key := func(row StockLevelRow) string { return row.ItemID + "\x1f" + row.VariantID }
	want := make(map[string]StockLevelRow, len(bundle.Rows))
	for _, row := range bundle.Rows {
		want[key(row)] = row
	}
	corrections := 0
	apply := func(itemID, variantID string, delta float64) error {
		if delta == 0 {
			return nil
		}
		_, err := r.db.RecordStockMovement(ctx, tx, StockMovementInput{
			ItemID:     itemID,
			VariantID:  variantID,
			LocationID: locationID,
			Type:       "adjust",
			Quantity:   delta,
			Reason:     "sync: match primary stock level",
		})
		if err != nil {
			return fmt.Errorf("stock sync correction (%s/%s): %w", itemID, variantID, err)
		}
		corrections++
		return nil
	}
	seen := map[string]bool{}
	for _, loc := range local.Rows {
		seen[key(loc)] = true
		target := want[key(loc)].Quantity // absent from the primary = 0
		if err := apply(loc.ItemID, loc.VariantID, target-loc.Quantity); err != nil {
			return 0, err
		}
	}
	for _, row := range bundle.Rows {
		if seen[key(row)] {
			continue
		}
		if err := apply(row.ItemID, row.VariantID, row.Quantity); err != nil {
			return 0, err
		}
	}
	if version != "" {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at) VALUES ('sync.stock_version', ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			version, time.Now().UTC()); err != nil {
			return 0, fmt.Errorf("stock sync: record version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("stock sync: commit: %w", err)
	}
	return corrections, nil
}
