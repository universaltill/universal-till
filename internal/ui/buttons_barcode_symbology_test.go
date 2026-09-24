package ui

// ut-docs#2497 regression: "Sell screen: some items in All / search fail
// with 'item does not exist' when tapped". LoadAllActive and SearchSellable
// used to set a tile's Code to the item's raw barcode whenever one
// existed, with no check that /api/pos/scan's resolver
// (POSRepo.ResolveShortcutLineDecoded -> ResolveScanLine ->
// barcode.Default().Match) could actually resolve it. A barcode stored
// under a symbology the shop's settings don't currently enable matches
// none of the resolver's tiers, so tapping the tile 404'd even though the
// item was active and listed right there. These tests seed a shop with
// only EAN13 enabled and an item whose only barcode is CODE128-shaped
// (structurally valid CODE128, not a 13-digit EAN13), then prove the tile
// Code falls back away from that unresolvable barcode AND that the
// resulting Code genuinely round-trips through the real scan resolver —
// not just that the raw barcode was avoided.

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// unresolvableSymbologyBarcode is shaped like a CODE128 scan (arbitrary
// printable ASCII) but is not a 13-digit, checksum-valid EAN13 — so with
// only EAN13 enabled shop-wide, barcode.Default().Match rejects it under
// every enabled symbology, exactly ut-docs#2497's reported shape (a
// catalog barcode entered under a symbology the shop has since disabled,
// or never enabled).
const unresolvableSymbologyBarcode = "PLU-CATALOG-9001"

// resolvableEAN13Barcode is a real, widely-published valid EAN13 example
// (also used by internal/barcode's own tests) — resolves under the
// EAN13-only enabled set used throughout this file.
const resolvableEAN13Barcode = "4006381333931"

// TestButtonStoreLoadAllActive_UnresolvableBarcodeSymbologyFallsBackAndRoundTrips
// is Test 1 from the ut-docs#2497 design: an active item whose only
// barcode is in a symbology NOT in the shop's enabled set must not get
// that raw barcode as its tile Code -- it must fall back to the item's SKU
// (if any) or the synthesized "item:<id>" code, and that Code must
// actually resolve back to the same item through
// POSRepo.ResolveShortcutLineDecoded, the exact function /api/pos/scan
// calls on tap.
func TestButtonStoreLoadAllActive_UnresolvableBarcodeSymbologyFallsBackAndRoundTrips(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	settingsRepo := data.NewSettingsRepo(db)
	if err := settingsRepo.SetEnabledBarcodeSymbologies(ctx, []string{"EAN13"}); err != nil {
		t.Fatalf("SetEnabledBarcodeSymbologies: %v", err)
	}

	// No SKU: the item genuinely has nothing resolvable except its (now
	// unresolvable) barcode, forcing the fallback all the way to the
	// synthesized item:<id> tier -- the strongest exercise of the fix.
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-disabled-sym', '', 'Odd Import', 199, 1)`)
	mustExec(t, db, `INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES(?, 'itm-disabled-sym', 1)`, unresolvableSymbologyBarcode)

	store := NewButtonStore(db)
	btns, err := store.LoadAllActive(ctx)
	if err != nil {
		t.Fatalf("LoadAllActive: %v", err)
	}
	var got *Button
	for i := range btns {
		if btns[i].ItemID == "itm-disabled-sym" {
			got = &btns[i]
		}
	}
	if got == nil {
		t.Fatalf("expected a tile for itm-disabled-sym, got: %+v", btns)
	}
	if got.Code == unresolvableSymbologyBarcode {
		t.Fatalf("Code = %q, must NOT be the raw barcode (unresolvable under the shop's enabled symbologies)", got.Code)
	}
	wantCode := synthesizedButtonCodePrefix + "itm-disabled-sym"
	if got.Code != wantCode {
		t.Fatalf("Code = %q, want synthesized fallback %q (item has no SKU)", got.Code, wantCode)
	}

	posRepo := data.NewPOSRepo(db)
	line, _, ok := posRepo.ResolveShortcutLineDecoded(ctx, got.Code)
	if !ok {
		t.Fatalf("ResolveShortcutLineDecoded(%q) failed to resolve -- tapping this tile would still 404", got.Code)
	}
	if line.ItemID != "itm-disabled-sym" {
		t.Fatalf("resolved ItemID = %q, want itm-disabled-sym", line.ItemID)
	}
}

// TestButtonStoreSearchSellable_UnresolvableBarcodeSymbologyFallsBackAndRoundTrips
// is Test 2: the same regression via the sell-screen's live search path
// (SearchSellable), which sources its raw barcode from
// POSRepo.SearchItemsForShortcuts rather than CatalogRepo.ItemBarcodes.
func TestButtonStoreSearchSellable_UnresolvableBarcodeSymbologyFallsBackAndRoundTrips(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	settingsRepo := data.NewSettingsRepo(db)
	if err := settingsRepo.SetEnabledBarcodeSymbologies(ctx, []string{"EAN13"}); err != nil {
		t.Fatalf("SetEnabledBarcodeSymbologies: %v", err)
	}

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-search-disabled-sym', '', 'Searchable Odd Import', 249, 1)`)
	mustExec(t, db, `INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES(?, 'itm-search-disabled-sym', 1)`, unresolvableSymbologyBarcode)

	store := NewButtonStore(db)
	btns, err := store.SearchSellable(ctx, "Searchable Odd Import", 10)
	if err != nil {
		t.Fatalf("SearchSellable: %v", err)
	}
	if len(btns) != 1 {
		t.Fatalf("len(btns) = %d, want 1: %+v", len(btns), btns)
	}
	got := btns[0]
	if got.Code == unresolvableSymbologyBarcode {
		t.Fatalf("Code = %q, must NOT be the raw barcode (unresolvable under the shop's enabled symbologies)", got.Code)
	}
	wantCode := synthesizedButtonCodePrefix + "itm-search-disabled-sym"
	if got.Code != wantCode {
		t.Fatalf("Code = %q, want synthesized fallback %q (item has no SKU)", got.Code, wantCode)
	}

	posRepo := data.NewPOSRepo(db)
	line, _, ok := posRepo.ResolveShortcutLineDecoded(ctx, got.Code)
	if !ok {
		t.Fatalf("ResolveShortcutLineDecoded(%q) failed to resolve -- tapping this tile would still 404", got.Code)
	}
	if line.ItemID != "itm-search-disabled-sym" {
		t.Fatalf("resolved ItemID = %q, want itm-search-disabled-sym", line.ItemID)
	}
}

// TestButtonStoreLoadAllActive_ResolvableBarcodeSymbologyUnchanged is Test
// 3, the control: an active item whose barcode IS in an enabled symbology
// must keep getting that real barcode as its Code, unchanged -- the fix
// must not start synthesizing codes for items that already worked.
func TestButtonStoreLoadAllActive_ResolvableBarcodeSymbologyUnchanged(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	settingsRepo := data.NewSettingsRepo(db)
	if err := settingsRepo.SetEnabledBarcodeSymbologies(ctx, []string{"EAN13"}); err != nil {
		t.Fatalf("SetEnabledBarcodeSymbologies: %v", err)
	}

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-enabled-sym', 'SKU-ENABLED', 'Normal Item', 399, 1)`)
	mustExec(t, db, `INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES(?, 'itm-enabled-sym', 1)`, resolvableEAN13Barcode)

	store := NewButtonStore(db)
	btns, err := store.LoadAllActive(ctx)
	if err != nil {
		t.Fatalf("LoadAllActive: %v", err)
	}
	var got *Button
	for i := range btns {
		if btns[i].ItemID == "itm-enabled-sym" {
			got = &btns[i]
		}
	}
	if got == nil {
		t.Fatalf("expected a tile for itm-enabled-sym, got: %+v", btns)
	}
	if got.Code != resolvableEAN13Barcode {
		t.Fatalf("Code = %q, want the real barcode %q unchanged (it resolves under the shop's enabled symbologies)", got.Code, resolvableEAN13Barcode)
	}

	posRepo := data.NewPOSRepo(db)
	line, _, ok := posRepo.ResolveShortcutLineDecoded(ctx, got.Code)
	if !ok {
		t.Fatalf("ResolveShortcutLineDecoded(%q) failed to resolve", got.Code)
	}
	if line.ItemID != "itm-enabled-sym" {
		t.Fatalf("resolved ItemID = %q, want itm-enabled-sym", line.ItemID)
	}
}

// TestButtonStoreLoadAllActive_UnresolvableBarcodeFallsBackToSKU is the
// middle branch of resolvableTileCode: an item with BOTH an unresolvable
// barcode AND a SKU must fall back to the SKU (not go all the way to the
// synthesized item:<id> tier) -- untested by the "no SKU" cases above,
// which only exercise the outermost fallback.
func TestButtonStoreLoadAllActive_UnresolvableBarcodeFallsBackToSKU(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	settingsRepo := data.NewSettingsRepo(db)
	if err := settingsRepo.SetEnabledBarcodeSymbologies(ctx, []string{"EAN13"}); err != nil {
		t.Fatalf("SetEnabledBarcodeSymbologies: %v", err)
	}

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-disabled-sym-with-sku', 'SKU-WITH-BAD-BARCODE', 'Odd Import With SKU', 299, 1)`)
	mustExec(t, db, `INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES(?, 'itm-disabled-sym-with-sku', 1)`, unresolvableSymbologyBarcode)

	store := NewButtonStore(db)
	btns, err := store.LoadAllActive(ctx)
	if err != nil {
		t.Fatalf("LoadAllActive: %v", err)
	}
	var got *Button
	for i := range btns {
		if btns[i].ItemID == "itm-disabled-sym-with-sku" {
			got = &btns[i]
		}
	}
	if got == nil {
		t.Fatalf("expected a tile for itm-disabled-sym-with-sku, got: %+v", btns)
	}
	if got.Code != "SKU-WITH-BAD-BARCODE" {
		t.Fatalf("Code = %q, want the SKU fallback %q (not the raw barcode, and not the synthesized item: tier since a SKU exists)", got.Code, "SKU-WITH-BAD-BARCODE")
	}

	posRepo := data.NewPOSRepo(db)
	line, _, ok := posRepo.ResolveShortcutLineDecoded(ctx, got.Code)
	if !ok {
		t.Fatalf("ResolveShortcutLineDecoded(%q) failed to resolve -- tapping this tile would still 404", got.Code)
	}
	if line.ItemID != "itm-disabled-sym-with-sku" {
		t.Fatalf("resolved ItemID = %q, want itm-disabled-sym-with-sku", line.ItemID)
	}
}

// TestButtonStoreSearchSellable_ResolvableBarcodeSymbologyUnchanged is
// SearchSellable's own control test, mirroring
// TestButtonStoreLoadAllActive_ResolvableBarcodeSymbologyUnchanged --
// SearchSellable sources its raw barcode from a different query
// (POSRepo.SearchItemsForShortcuts, not CatalogRepo.ItemBarcodes) and had
// no control coverage of its own.
func TestButtonStoreSearchSellable_ResolvableBarcodeSymbologyUnchanged(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	settingsRepo := data.NewSettingsRepo(db)
	if err := settingsRepo.SetEnabledBarcodeSymbologies(ctx, []string{"EAN13"}); err != nil {
		t.Fatalf("SetEnabledBarcodeSymbologies: %v", err)
	}

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-search-enabled-sym', 'SKU-SEARCH-ENABLED', 'Searchable Normal Item', 449, 1)`)
	mustExec(t, db, `INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES(?, 'itm-search-enabled-sym', 1)`, resolvableEAN13Barcode)

	store := NewButtonStore(db)
	btns, err := store.SearchSellable(ctx, "Searchable Normal Item", 10)
	if err != nil {
		t.Fatalf("SearchSellable: %v", err)
	}
	if len(btns) != 1 {
		t.Fatalf("len(btns) = %d, want 1: %+v", len(btns), btns)
	}
	got := btns[0]
	if got.Code != resolvableEAN13Barcode {
		t.Fatalf("Code = %q, want the real barcode %q unchanged (it resolves under the shop's enabled symbologies)", got.Code, resolvableEAN13Barcode)
	}

	posRepo := data.NewPOSRepo(db)
	line, _, ok := posRepo.ResolveShortcutLineDecoded(ctx, got.Code)
	if !ok {
		t.Fatalf("ResolveShortcutLineDecoded(%q) failed to resolve", got.Code)
	}
	if line.ItemID != "itm-search-enabled-sym" {
		t.Fatalf("resolved ItemID = %q, want itm-search-enabled-sym", line.ItemID)
	}
}
