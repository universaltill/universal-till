package data

// Stock-location management (universaltill/ut-docs#49): create/rename/
// deactivate stock_locations, and a guard so a location that still holds
// stock or has an active register can't be deactivated out from under live
// use. Historical activity alone does not block deactivation
// (universaltill/ut-docs#2066) — is_active is never used to filter a
// location's past inventory/movement/register rows out of any report,
// export or audit query, so nothing is orphaned by deactivating it.

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

	// A location with a nonzero inventory row counts as in-use. (Until
	// ut-docs#539 this leaned on 001's demo seed; the catalogue is opt-in
	// now, so seed the item + balance explicitly.)
	mustExec(t, d, `INSERT INTO items (id, name, base_price) VALUES ('itm-t49', 'Test Item', 100)`)
	mustExec(t, d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES ('inv-t49', 'itm-t49', NULL, 'loc_main', 5)`)
	inUse, err = repo.StockLocationInUse(ctx, "loc_main")
	if err != nil {
		t.Fatalf("StockLocationInUse(loc_main): %v", err)
	}
	if !inUse {
		t.Fatal("loc_main has a nonzero inventory row and must be reported in-use")
	}

	// ut-docs#2066: once that same inventory row is cleared to zero (sold
	// through or moved out), the location is no longer in-use — "clear the
	// stock and try again" must be a real, working path, not a dead end.
	mustExec(t, d, `UPDATE inventory SET quantity = 0 WHERE id = 'inv-t49'`)
	inUse, err = repo.StockLocationInUse(ctx, "loc_main")
	if err != nil {
		t.Fatalf("StockLocationInUse(loc_main, cleared): %v", err)
	}
	if inUse {
		t.Fatal("loc_main's inventory row is now quantity=0 and must not be reported in-use")
	}

	// A location referenced by a currently-active register counts as in-use.
	viaRegisterID, err := repo.CreateStockLocation(ctx, "Till-only Location")
	if err != nil {
		t.Fatalf("create via-register: %v", err)
	}
	mustExec(t, d, `INSERT INTO registers (id, name, location_id) VALUES ('reg-t49', 'Test Register', ?)`, viaRegisterID)
	inUse, err = repo.StockLocationInUse(ctx, viaRegisterID)
	if err != nil {
		t.Fatalf("StockLocationInUse(via active register): %v", err)
	}
	if !inUse {
		t.Fatal("location referenced by a currently-active register must be reported in-use")
	}

	// Once that register is itself retired (deactivated), it no longer
	// blocks the location.
	mustExec(t, d, `UPDATE registers SET is_active = 0 WHERE id = 'reg-t49'`)
	inUse, err = repo.StockLocationInUse(ctx, viaRegisterID)
	if err != nil {
		t.Fatalf("StockLocationInUse(via retired register): %v", err)
	}
	if inUse {
		t.Fatal("location whose only register is now retired must not be reported in-use")
	}

	// ut-docs#2066: a location referenced only by historical stock_movements
	// rows (no current nonzero inventory, no active register) must NOT
	// count as in-use — stock_movements is a pure append-only audit trail
	// that no query filters by the location's is_active state, so nothing
	// is orphaned by deactivating it.
	viaMovementID, err := repo.CreateStockLocation(ctx, "Movement-only Location")
	if err != nil {
		t.Fatalf("create via-movement: %v", err)
	}
	mustExec(t, d, `INSERT INTO stock_movements (id, item_id, variant_id, location_id, type, quantity, created_at)
		VALUES ('mv-t49', 'itm-t49', NULL, ?, 'adjust', 1, datetime('now'))`, viaMovementID)
	inUse, err = repo.StockLocationInUse(ctx, viaMovementID)
	if err != nil {
		t.Fatalf("StockLocationInUse(via movement only): %v", err)
	}
	if inUse {
		t.Fatal("location referenced only by a historical stock_movements row must not be reported in-use")
	}
}

// TestStockLocationInUse_FractionalStockClearedToZero (review of
// ut-docs#2066): stock is enterable to 2dp and inventory.quantity is a REAL
// accumulated in place by `quantity = quantity + ?`, so clearing a weighed
// line leaves a float residue (0.1 + 0.2 - 0.3 = 5.55e-17) rather than a
// clean 0. That residue displays as 0.00 on every screen the manager can
// see, so an exact `quantity <> 0` compare would refuse the deactivation
// with no visible cause and no way to clear it — #2066's own dead end, in
// miniature. Driven through RecordStockMovement, not a hand-written
// quantity, so it keeps testing the arithmetic that actually produces the
// residue.
func TestStockLocationInUse_FractionalStockClearedToZero(t *testing.T) {
	d, repo := openLocTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateStockLocation(ctx, "Deli Counter")
	if err != nil {
		t.Fatalf("create deli counter: %v", err)
	}
	mustExec(t, d, `INSERT INTO items (id, name, base_price) VALUES ('itm-t2066', 'Loose Cheese', 100)`)

	// Take in 0.1 kg, then 0.2 kg, then sell/adjust the whole 0.3 kg away.
	for _, qty := range []float64{0.1, 0.2, -0.3} {
		if _, err := repo.RecordStockMovement(ctx, nil, StockMovementInput{
			ItemID: "itm-t2066", LocationID: id, Type: "adjust", Quantity: qty,
		}); err != nil {
			t.Fatalf("record movement %v: %v", qty, err)
		}
	}

	var stored float64
	if err := d.QueryRow(`SELECT quantity FROM inventory WHERE location_id = ?`, id).Scan(&stored); err != nil {
		t.Fatalf("read stored quantity: %v", err)
	}
	if stored == 0 {
		t.Skip("SQLite produced an exact 0 here, so there is no residue left to guard against")
	}

	inUse, err := repo.StockLocationInUse(ctx, id)
	if err != nil {
		t.Fatalf("StockLocationInUse(deli counter): %v", err)
	}
	if inUse {
		t.Fatalf("stock is %g — it reads 0.00 to the manager, so the location must be deactivatable", stored)
	}
}
