package pages

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// pinTillRegisterToNewLocation (ut-docs#2067) reproduces the exact state a
// manager reaches through the existing UI: a second, active stock location
// (Settings → Locations), a register assigned to it (Settings → Registers),
// and that register persisted as THIS till's own identity (Settings →
// Tills, pos.SettingsKeyTillRegisterID). Returns both ids so a test can
// assert a stock movement landed against the pinned location, not Main.
func pinTillRegisterToNewLocation(t *testing.T, dp *common.Deps) (registerID, locationID string) {
	t.Helper()
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	locationID, err := repo.CreateStockLocation(ctx, "Loading Bay")
	if err != nil {
		t.Fatalf("create stock location: %v", err)
	}
	registerID, err = repo.CreateRegister(ctx, "Loading Bay Till", &locationID)
	if err != nil {
		t.Fatalf("create register: %v", err)
	}
	if err := dp.Settings.Set(ctx, pos.SettingsKeyTillRegisterID, registerID); err != nil {
		t.Fatalf("persist till register identity: %v", err)
	}
	return registerID, locationID
}

// inventoryQtyAt reads an item's on-hand quantity at one location, 0 when
// the (item, location) pair has never had a stock row — so "Main untouched"
// and "nothing ever landed here" both assert cleanly.
func inventoryQtyAt(t *testing.T, dp *common.Deps, itemID, locationID string) float64 {
	t.Helper()
	var qty float64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(quantity), 0) FROM inventory WHERE item_id = ? AND location_id = ?`, itemID, locationID).Scan(&qty); err != nil {
		t.Fatalf("read inventory for %s at %s: %v", itemID, locationID, err)
	}
	return qty
}
