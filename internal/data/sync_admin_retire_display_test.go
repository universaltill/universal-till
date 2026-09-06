package data

import (
	"context"
	"testing"
)

// ut-docs#1610: deleteMissing's FK-blocked retire-in-place frees a UNIQUE
// column by mangling it to "<value>~<id>". Six admin tables use the SAME
// column for identity-uniqueness and display (brands.name, tax_codes.name,
// payment_methods.name, users.username, stock_locations.name,
// registers.name), so without a strip at the read side the raw mangled value
// ("Acme Foods~b1") reaches shop staff. These tests drive the real retire
// path through ApplyAdmin (never a hand-written UPDATE) and then assert the
// RESOLVED display value each repository reader returns — not just the DB
// row — mirroring TestCatalogPage_InactiveTaxCodeSurvivesUnrelatedSave's
// "check what the operator actually sees" pattern.
//
// The item fixtures below follow TestAdminApplyRetiresFKPinnedSKUSquatter: a
// satellite-local item, FK-pinned by a stock movement so ApplyAdmin can never
// hard-delete it, pointing at the brand/tax code under test so THAT prune is
// FK-blocked too.

func TestAdminApply_BrandRetiredInPlace_DisplayNameUnmangled(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO brands (id, name) VALUES ('b1', 'Acme Foods')`)
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// A satellite-local item references the brand and is itself FK-pinned by
	// local stock history, so neither it nor the brand can be hard-deleted.
	mustExec(t, replica, `INSERT INTO items (id, sku, name, base_price, brand_id) VALUES ('itm-local', 'LOCAL-1', 'Local Item', 100, 'b1')`)
	mustExec(t, replica, `INSERT INTO stock_locations (id, name) VALUES ('loc-local', 'Local Store')`)
	mustExec(t, replica, `INSERT INTO stock_movements (id, item_id, location_id, type, quantity) VALUES ('mv-1', 'itm-local', 'loc-local', 'sale', -1)`)

	mustExec(t, primary, `DELETE FROM brands WHERE id = 'b1'`)
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("second dump: %v", err)
	}
	// Twice: the second pass proves the retire is idempotent and that a
	// re-pull doesn't double-mangle the name into something the strip can't
	// undo.
	for i := range 2 {
		if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle2)); err != nil {
			t.Fatalf("apply #%d after primary delete: %v", i+1, err)
		}
	}

	// Raw row: retired (new column, migration 004) AND mangled — the premise
	// this whole card rests on.
	var rawName string
	var active int
	if err := replica.QueryRow(`SELECT name, is_active FROM brands WHERE id = 'b1'`).Scan(&rawName, &active); err != nil {
		t.Fatalf("query retired brand (brands must now carry is_active): %v", err)
	}
	if active != 0 {
		t.Errorf("an FK-blocked brand prune must retire in place: is_active=%d, want 0", active)
	}
	if rawName != "Acme Foods~b1" {
		t.Errorf("raw brands.name after retire = %q, want the uniqueness-freeing mangle %q", rawName, "Acme Foods~b1")
	}

	// Resolved display values: the mangle must never reach the /catalog
	// brand <select> or the brandName row resolver (both fed by ReadLookup),
	// nor the per-row re-render (GetLookup).
	repo := NewCatalogRepo(replica.DB)
	lookups, err := repo.ReadLookup(ctx, "brands")
	if err != nil {
		t.Fatalf("ReadLookup brands: %v", err)
	}
	found := false
	for _, l := range lookups {
		if l.ID == "b1" {
			found = true
			if l.Name != "Acme Foods" {
				t.Errorf("ReadLookup(brands) name for retired-but-referenced brand = %q, want %q", l.Name, "Acme Foods")
			}
		}
	}
	if !found {
		t.Errorf("ReadLookup(brands) dropped the retired-but-referenced brand entirely — /catalog's brand <select> could no longer re-submit the local item's own brand (same class of bug ListAllTaxCodes exists to prevent); got %+v", lookups)
	}
	one, err := repo.GetLookup(ctx, "brands", "b1")
	if err != nil {
		t.Fatalf("GetLookup brands b1: %v", err)
	}
	if one.Name != "Acme Foods" {
		t.Errorf("GetLookup(brands, b1).Name = %q, want %q", one.Name, "Acme Foods")
	}
}

func TestAdminApply_TaxCodeRetiredInPlace_DisplayNameUnmangled(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO tax_codes (id, name, rate_basis_points) VALUES ('tc-red', 'Reduced 7%', 700)`)
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	mustExec(t, replica, `INSERT INTO items (id, sku, name, base_price, tax_code_id) VALUES ('itm-local', 'LOCAL-1', 'Local Item', 100, 'tc-red')`)
	mustExec(t, replica, `INSERT INTO stock_locations (id, name) VALUES ('loc-local', 'Local Store')`)
	mustExec(t, replica, `INSERT INTO stock_movements (id, item_id, location_id, type, quantity) VALUES ('mv-1', 'itm-local', 'loc-local', 'sale', -1)`)

	mustExec(t, primary, `DELETE FROM tax_codes WHERE id = 'tc-red'`)
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("second dump: %v", err)
	}
	for i := range 2 {
		if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle2)); err != nil {
			t.Fatalf("apply #%d after primary delete: %v", i+1, err)
		}
	}

	var rawName string
	var active int
	if err := replica.QueryRow(`SELECT name, is_active FROM tax_codes WHERE id = 'tc-red'`).Scan(&rawName, &active); err != nil {
		t.Fatalf("query retired tax code: %v", err)
	}
	if active != 0 || rawName != "Reduced 7%~tc-red" {
		t.Fatalf("tax code not retired the way this test expects: name=%q is_active=%d", rawName, active)
	}

	repo := NewCatalogRepo(replica.DB)
	tc, err := repo.GetTaxCode(ctx, "tc-red")
	if err != nil {
		t.Fatalf("GetTaxCode: %v", err)
	}
	if tc.Name != "Reduced 7%" {
		t.Errorf("GetTaxCode(tc-red).Name = %q, want %q", tc.Name, "Reduced 7%")
	}
	if tc.IsActive {
		t.Errorf("GetTaxCode(tc-red).IsActive = true, want false (retired)")
	}
	all, err := repo.ListAllTaxCodes(ctx)
	if err != nil {
		t.Fatalf("ListAllTaxCodes: %v", err)
	}
	found := false
	for _, v := range all {
		if v.ID == "tc-red" {
			found = true
			if v.Name != "Reduced 7%" {
				t.Errorf("ListAllTaxCodes name for retired tax code = %q, want %q (this feeds /catalog's tax <select> and taxCodeName)", v.Name, "Reduced 7%")
			}
		}
	}
	if !found {
		t.Errorf("ListAllTaxCodes must still include the retired code (it lists active AND inactive); got %+v", all)
	}
}

func TestAdminApply_UserRetiredInPlace_DisplayUsernameUnmangled(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO users (id, username, display_name, pin_hash) VALUES ('u-alice', 'alice', 'Alice', 'hash')`)
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// The satellite rang a sale under this cashier before the primary
	// removed the account: sales.cashier_id FK-blocks the hard delete.
	mustExec(t, replica, `INSERT INTO sales (id, receipt_no, subtotal, total, cashier_id) VALUES ('sale-1', 'R-0001', 100, 100, 'u-alice')`)

	mustExec(t, primary, `DELETE FROM users WHERE id = 'u-alice'`)
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("second dump: %v", err)
	}
	for i := range 2 {
		if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle2)); err != nil {
			t.Fatalf("apply #%d after primary delete: %v", i+1, err)
		}
	}

	var rawUsername string
	var active int
	if err := replica.QueryRow(`SELECT username, is_active FROM users WHERE id = 'u-alice'`).Scan(&rawUsername, &active); err != nil {
		t.Fatalf("query retired user: %v", err)
	}
	if active != 0 || rawUsername != "alice~u-alice" {
		t.Fatalf("user not retired the way this test expects: username=%q is_active=%d", rawUsername, active)
	}

	auth := NewAuthRepo(replica.DB)
	users, err := auth.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	found := false
	for _, u := range users {
		if u.ID == "u-alice" {
			found = true
			if u.Username != "alice" {
				t.Errorf("ListUsers username for retired user = %q, want %q (the /users admin page shows inactive accounts too)", u.Username, "alice")
			}
			if u.IsActive {
				t.Errorf("ListUsers IsActive for retired user = true, want false")
			}
		}
	}
	if !found {
		t.Errorf("ListUsers must still include the retired user; got %+v", users)
	}
	u, ok, err := auth.GetUser(ctx, "u-alice")
	if err != nil || !ok {
		t.Fatalf("GetUser: ok=%v err=%v", ok, err)
	}
	if u.Username != "alice" {
		t.Errorf("GetUser(u-alice).Username = %q, want %q", u.Username, "alice")
	}
}

// ut-docs#1610 REVIEW finding: the first pass patched the readers it went
// looking for (the lookup/admin listings) but missed four that reach a
// display path through a JOIN rather than a direct SELECT — and for
// stock_locations those are the MOST likely place the corruption shows up,
// because a location whose prune is FK-blocked is FK-blocked precisely
// BECAUSE inventory rows point at it, which is exactly what these queries
// return:
//
//	POSRepo.ListStockLevels      -> the inventory page, and StockForExport
//	POSRepo.GetLowStockItems     -> the reorder list
//	POSRepo.variantStockForExport -> StockForExport's variant rows
//	POSRepo.ListRegisterLocations -> FiscalRegisterDEStore.List, the §146a
//	                                 AO register page (which deliberately
//	                                 drops the is_active filter so a
//	                                 decommissioned till still shows its name)
func TestAdminApply_RetiredLocationAndRegisterNamesUnmangledInStockAndFiscalReaders(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO stock_locations (id, name, is_active) VALUES ('loc-1', 'Back Room', 1)`)
	mustExec(t, primary, `INSERT INTO registers (id, name, location_id, is_active) VALUES ('reg-1', 'Front Till', 'loc-1', 1)`)
	// The item and its variant live on the PRIMARY and stay in every
	// bundle: `items`/`item_variants` are themselves admin-synced, so a
	// satellite-LOCAL item would be pruned-then-retired (is_active = 0) by
	// this same apply and then filtered out of the very readers under test.
	mustExec(t, primary, `INSERT INTO items (id, sku, name, base_price, reorder_level) VALUES ('itm-1', 'SKU-1', 'Widget', 100, 10)`)
	mustExec(t, primary, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('var-1', 'itm-1', 'SKU-1-V', 'Large', 120)`)
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Satellite-local history that FK-blocks both prunes: stock at the
	// location (item- AND variant-scoped, so both export readers are
	// covered) and a shift on the register. `inventory` is deliberately not
	// an admin-synced table, so these stay local.
	mustExec(t, replica, `INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-1', 'itm-1', 'loc-1', 5)`)
	mustExec(t, replica, `INSERT INTO inventory (id, variant_id, location_id, quantity) VALUES ('inv-2', 'var-1', 'loc-1', 3)`)
	mustExec(t, replica, `INSERT INTO users (id, username, display_name) VALUES ('u1', 'cashier1', 'Cashier One')`)
	mustExec(t, replica, `INSERT INTO shifts (id, register_id, cashier_id) VALUES ('shift-1', 'reg-1', 'u1')`)

	mustExec(t, primary, `DELETE FROM registers WHERE id = 'reg-1'`)
	mustExec(t, primary, `DELETE FROM stock_locations WHERE id = 'loc-1'`)
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("second dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle2)); err != nil {
		t.Fatalf("apply after primary delete: %v", err)
	}

	// Premise: both rows retired in place, both names mangled.
	var rawLoc, rawReg string
	if err := replica.QueryRow(`SELECT name FROM stock_locations WHERE id = 'loc-1'`).Scan(&rawLoc); err != nil {
		t.Fatalf("query retired location: %v", err)
	}
	if err := replica.QueryRow(`SELECT name FROM registers WHERE id = 'reg-1'`).Scan(&rawReg); err != nil {
		t.Fatalf("query retired register: %v", err)
	}
	if rawLoc != "Back Room~loc-1" || rawReg != "Front Till~reg-1" {
		t.Fatalf("rows not retire-mangled the way this test expects: location=%q register=%q", rawLoc, rawReg)
	}

	pos := NewPOSRepo(replica.DB)

	levels, err := pos.ListStockLevels(ctx)
	if err != nil {
		t.Fatalf("ListStockLevels: %v", err)
	}
	if len(levels) == 0 {
		t.Fatalf("ListStockLevels returned nothing; fixture is not exercising the join")
	}
	for _, l := range levels {
		if l.LocationID == "loc-1" && l.LocationName != "Back Room" {
			t.Errorf("ListStockLevels (inventory page) LocationName = %q, want %q", l.LocationName, "Back Room")
		}
	}

	low, err := pos.GetLowStockItems(ctx, "")
	if err != nil {
		t.Fatalf("GetLowStockItems: %v", err)
	}
	if len(low) == 0 {
		t.Fatalf("GetLowStockItems returned nothing; fixture is not exercising the join")
	}
	for _, l := range low {
		if l.LocationID == "loc-1" && l.LocationName != "Back Room" {
			t.Errorf("GetLowStockItems (reorder list) LocationName = %q, want %q", l.LocationName, "Back Room")
		}
	}

	exported, err := pos.StockForExport(ctx)
	if err != nil {
		t.Fatalf("StockForExport: %v", err)
	}
	sawVariant := false
	for _, e := range exported {
		if e.VariantID != "" {
			sawVariant = true
		}
		if e.LocationID == "loc-1" && e.LocationName != "Back Room" {
			t.Errorf("StockForExport location_name = %q, want %q (variant_id=%q)", e.LocationName, "Back Room", e.VariantID)
		}
	}
	if !sawVariant {
		t.Errorf("StockForExport returned no variant row; variantStockForExport's join is not being exercised")
	}

	regLocs, err := pos.ListRegisterLocations(ctx)
	if err != nil {
		t.Fatalf("ListRegisterLocations: %v", err)
	}
	found := false
	for _, rl := range regLocs {
		if rl.RegisterID != "reg-1" {
			continue
		}
		found = true
		if rl.RegisterName != "Front Till" {
			t.Errorf("ListRegisterLocations (fiscal register page) RegisterName = %q, want %q", rl.RegisterName, "Front Till")
		}
		if rl.LocationName != "Back Room" {
			t.Errorf("ListRegisterLocations (fiscal register page) LocationName = %q, want %q", rl.LocationName, "Back Room")
		}
	}
	if !found {
		t.Errorf("ListRegisterLocations must still list the retired register (it deliberately has no is_active filter); got %+v", regLocs)
	}
}
