package data

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	rows, err := r.db.db.QueryContext(ctx, `
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
func (r *SyncStockRepo) ApplyStockLevels(ctx context.Context, bundle StockBundle, locationID string) (int, error) {
	local, err := r.DumpStock(ctx)
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
		_, err := r.db.RecordStockMovement(ctx, nil, StockMovementInput{
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
			return corrections, err
		}
	}
	for _, row := range bundle.Rows {
		if seen[key(row)] {
			continue
		}
		if err := apply(row.ItemID, row.VariantID, row.Quantity); err != nil {
			return corrections, err
		}
	}
	return corrections, nil
}
