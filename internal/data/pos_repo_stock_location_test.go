package data

// Stock-location management (universaltill/ut-docs#49): create/rename/
// deactivate stock_locations, and a guard so a location with existing
// inventory/movement/register history can't be silently orphaned.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

func openLocTestDB(t *testing.T) (*db.DB, *POSRepo) {
	t.Helper()
	dbo, err := db.Open(filepath.Join(t.TempDir(), "loc.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbo.Close() })
	return dbo, NewPOSRepo(dbo.DB)
}

func TestCreateStockLocation(t *testing.T) {
	_, repo := openLocTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateStockLocation(ctx, "Front Yard")
	if err != nil {
		t.Fatalf("CreateStockLocation: %v", err)
	}
	if id == "" {
		t.Fatal("CreateStockLocation returned empty id")
	}

	locs, err := repo.ListStockLocations(ctx)
	if err != nil {
		t.Fatalf("ListStockLocations: %v", err)
	}
	found := false
	for _, l := range locs {
		if l.ID == id && l.Name == "Front Yard" {
			found = true
		}
	}
	if !found {
		t.Fatalf("new location not present in ListStockLocations: %+v", locs)
	}
}

func TestCreateStockLocation_DuplicateNameRejected(t *testing.T) {
	_, repo := openLocTestDB(t)
	ctx := context.Background()

	if _, err := repo.CreateStockLocation(ctx, "Loading Dock"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := repo.CreateStockLocation(ctx, "Loading Dock"); err == nil {
		t.Fatal("duplicate name must error (stock_locations.name is UNIQUE)")
	}
}

func TestRenameStockLocation(t *testing.T) {
	_, repo := openLocTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateStockLocation(ctx, "Old Name")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := repo.RenameStockLocation(ctx, id, "New Name"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	locs, err := repo.ListStockLocations(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, l := range locs {
		if l.ID == id {
			if l.Name != "New Name" {
				t.Fatalf("rename did not take effect: got %q", l.Name)
			}
			return
		}
	}
	t.Fatalf("renamed location %s not found", id)
}

func TestRenameStockLocation_UnknownID(t *testing.T) {
	_, repo := openLocTestDB(t)
	ctx := context.Background()

	if err := repo.RenameStockLocation(ctx, "ghost", "whatever"); err == nil {
		t.Fatal("RenameStockLocation(unknown) must error")
	}
}

func TestSetStockLocationActive(t *testing.T) {
	d, repo := openLocTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateStockLocation(ctx, "Cellar")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := repo.SetStockLocationActive(ctx, id, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	var active int
	if err := d.DB.QueryRow(`SELECT is_active FROM stock_locations WHERE id = ?`, id).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatal("location not deactivated in DB")
	}

	if err := repo.SetStockLocationActive(ctx, id, true); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if err := d.DB.QueryRow(`SELECT is_active FROM stock_locations WHERE id = ?`, id).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatal("location not reactivated in DB")
	}

	if err := repo.SetStockLocationActive(ctx, "ghost", false); err == nil {
		t.Fatal("SetStockLocationActive(unknown) must error")
	}
}

func TestStockLocationInUse(t *testing.T) {
	d, repo := openLocTestDB(t)
	ctx := context.Background()

	freeID, err := repo.CreateStockLocation(ctx, "Empty Shelf")
	if err != nil {
		t.Fatalf("create free: %v", err)
	}
	inUse, err := repo.StockLocationInUse(ctx, freeID)
	if err != nil {
		t.Fatalf("StockLocationInUse(free): %v", err)
	}
	if inUse {
		t.Fatal("brand-new location must not be reported in-use")
	}

	// A location with an inventory row counts as in-use. (Until ut-docs#539
	// this leaned on 001's demo seed; the catalogue is opt-in now, so seed
	// the item + balance explicitly.)
	mustExec(t, d, `INSERT INTO items (id, name, base_price) VALUES ('itm-t49', 'Test Item', 100)`)
	mustExec(t, d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES ('inv-t49', 'itm-t49', NULL, 'loc_main', 5)`)
	inUse, err = repo.StockLocationInUse(ctx, "loc_main")
	if err != nil {
		t.Fatalf("StockLocationInUse(loc_main): %v", err)
	}
	if !inUse {
		t.Fatal("loc_main has an inventory row and must be reported in-use")
	}

	// A location referenced only by a register must also count as in-use.
	viaRegisterID, err := repo.CreateStockLocation(ctx, "Till-only Location")
	if err != nil {
		t.Fatalf("create via-register: %v", err)
	}
	mustExec(t, d, `INSERT INTO registers (id, name, location_id) VALUES ('reg-t49', 'Test Register', ?)`, viaRegisterID)
	inUse, err = repo.StockLocationInUse(ctx, viaRegisterID)
	if err != nil {
		t.Fatalf("StockLocationInUse(via register): %v", err)
	}
	if !inUse {
		t.Fatal("location referenced by a register must be reported in-use")
	}

	// A location referenced only by a stock_movements row (no inventory
	// balance row, no register) must also count as in-use — this is the
	// history the guard exists to protect against orphaning.
	viaMovementID, err := repo.CreateStockLocation(ctx, "Movement-only Location")
	if err != nil {
		t.Fatalf("create via-movement: %v", err)
	}
	mustExec(t, d, `INSERT INTO stock_movements (id, item_id, variant_id, location_id, type, quantity, created_at)
		VALUES ('mv-t49', 'itm-t49', NULL, ?, 'adjust', 1, datetime('now'))`, viaMovementID)
	inUse, err = repo.StockLocationInUse(ctx, viaMovementID)
	if err != nil {
		t.Fatalf("StockLocationInUse(via movement): %v", err)
	}
	if !inUse {
		t.Fatal("location referenced only by a stock_movements row must be reported in-use")
	}
}
