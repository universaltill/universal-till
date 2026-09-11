package pos

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/universaltill/universal-till/internal/data"
)

// ResolveStockLocationID returns the stock location a register-tied write
// (sale, refund, catalog-import opening stock, sync-applied sale) lands
// against (ut-docs#2067). Resolution order:
//
//  1. registerID names a register whose assigned location (Settings →
//     Registers, registers.location_id) is set and still active: that
//     location — the mapping that already existed end-to-end but was never
//     read by any write path.
//  2. Otherwise — no register named, an unknown register, an unassigned
//     one, or one whose location has since been deactivated — the existing
//     EnsureStockLocation default ("Main"), byte-for-byte the pre-#2067
//     behaviour, so a shop that never assigns a register location sees no
//     change at all.
//
// Deliberately never errors on a missing mapping and never self-heals the
// mapping itself: only the fallback's own DB failures surface.
func ResolveStockLocationID(ctx context.Context, sqlDB *sql.DB, registerID string) (string, error) {
	repo := data.NewPOSRepo(sqlDB)
	if registerID != "" {
		locID, ok, err := repo.RegisterLocationID(ctx, registerID)
		if err != nil {
			return "", fmt.Errorf("resolve stock location: %w", err)
		}
		if ok {
			return locID, nil
		}
	}
	return repo.EnsureStockLocation(ctx)
}
