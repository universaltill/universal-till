package ui

import (
	"context"
	"testing"
)

// ut-docs#2541: ButtonStore.Load() now returns explicit shortcut_buttons
// rows (in sort_order) followed by every OTHER active, non-hidden catalog
// item, reusing LoadAllActive's own code/price/thumbnail/mods/variants
// logic -- these tests pin the card's own acceptance bar directly at the
// store level (BA/Architect's "0 rows -> all active items; some rows ->
// rows first in sort_order then the rest, no duplicates; a barcode-less
// item gets the SKU (else item:<id>) code"; plus hidden exclusion and the
// SearchSellable/scan-resolver carve-out).

// TestButtonStoreLoad_ZeroRowsReturnsEveryActiveItem is the "0 rows -> all
// active items" acceptance case: a shop that has never touched the
// Designer still gets a full sell-screen grid.
func TestButtonStoreLoad_ZeroRowsReturnsEveryActiveItem(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','S2','Bread', 200, 1)`)
	// Inactive item must never appear.
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i3','S3','Cola', 150, 0)`)

	store := NewButtonStore(db)
	btns, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(btns) != 2 {
		t.Fatalf("expected every active item as an implicit tile, got %d: %+v", len(btns), btns)
	}
	// LoadAllActive's own order: by name (ListItems' ORDER BY name).
	if btns[0].Label != "Apple" || btns[1].Label != "Bread" {
		t.Fatalf("expected name order Apple, Bread — got %+v", btns)
	}
	if btns[0].Code != "S1" || btns[1].Code != "S2" {
		t.Fatalf("expected each implicit tile's code to fall back to its SKU, got %+v", btns)
	}
}

// TestButtonStoreLoad_ExplicitRowsFirstThenImplicitRemainderNoDuplicates is
// the "some rows -> rows first in sort_order then the rest, no duplicates"
// acceptance case.
func TestButtonStoreLoad_ExplicitRowsFirstThenImplicitRemainderNoDuplicates(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','S2','Bread', 200, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i3','S3','Cola', 150, 1)`)
	// Only Cola gets an explicit row -- deliberately sort_order 0 even
	// though "Cola" would otherwise sort last alphabetically, so the test
	// actually proves the explicit row's OWN sort_order wins, not that it
	// happened to already be first.
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B1','Cola Tile','i3',0)`)

	store := NewButtonStore(db)
	btns, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(btns) != 3 {
		t.Fatalf("expected 1 explicit + 2 implicit tiles, no duplicates, got %d: %+v", len(btns), btns)
	}
	if btns[0].Code != "B1" || btns[0].ItemID != "i3" {
		t.Fatalf("expected the explicit row (Cola) first, got %+v", btns[0])
	}
	// The implicit remainder follows in LoadAllActive's own name order.
	if btns[1].Label != "Apple" || btns[2].Label != "Bread" {
		t.Fatalf("expected the implicit remainder in name order (Apple, Bread), got %+v", btns[1:])
	}
	// i3 (Cola) must appear exactly once -- the explicit row, not also as
	// an implicit one.
	seen := map[string]int{}
	for _, b := range btns {
		seen[b.ItemID]++
	}
	if seen["i3"] != 1 {
		t.Fatalf("expected Cola exactly once (explicit row wins over its own implicit slot), got %d: %+v", seen["i3"], btns)
	}
}

// TestButtonStoreLoad_ImplicitTileNoSKUFallsBackToItemIDCode is the
// "barcode-less item ... no-SKU -> item:<id>" acceptance case, for an
// IMPLICIT tile (no shortcut_buttons row at all) — ButtonStore.Add's own
// synthesized-code path (TestButtonStoreAdd_SynthesizesCodeWhenNeitherBarcodeNorSKU)
// covers the explicit-add case; this covers Load's implicit merge, which
// goes through LoadAllActive's resolvableTileCode instead.
func TestButtonStoreLoad_ImplicitTileNoSKUFallsBackToItemIDCode(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','','Loose Bun', 150, 1)`)

	store := NewButtonStore(db)
	btns, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(btns) != 1 {
		t.Fatalf("expected 1 implicit tile, got %+v", btns)
	}
	if want := synthesizedButtonCodePrefix + "i1"; btns[0].Code != want {
		t.Fatalf("Code = %q, want synthesized %q", btns[0].Code, want)
	}
}

// TestButtonStoreLoad_MarksHiddenItems (ut-docs#2541, #2698): a hidden
// item stays in the grid load (Load), marked Hidden -- edit mode shows it
// greyed -- while the at-rest All grid source (LoadAllActive) leaves it out.
func TestButtonStoreLoad_MarksHiddenItems(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, sell_screen_hidden) VALUES('i1','S1','Apple', 100, 1, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','S2','Bread', 200, 1)`)

	store := NewButtonStore(db)
	btns, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(btns) != 2 || btns[0].Label != "Apple" || !btns[0].Hidden || btns[1].Hidden {
		t.Fatalf("expected Apple (hidden) then Bread, got %+v", btns)
	}

	all, err := store.LoadAllActive(context.Background())
	if err != nil {
		t.Fatalf("LoadAllActive: %v", err)
	}
	if len(all) != 1 || all[0].Label != "Bread" {
		t.Fatalf("expected LoadAllActive (the All grid) to exclude the hidden item, got %+v", all)
	}
}

// TestButtonStoreSearchSellable_StillReturnsHiddenItems (ut-docs#2541): a
// hidden item is off the tile grids, but must still be found by the sell
// screen's own live search -- it still sells via scan/search.
func TestButtonStoreSearchSellable_StillReturnsHiddenItems(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, sell_screen_hidden) VALUES('i1','APL-01','Apple Juice', 100, 1, 1)`)

	store := NewButtonStore(db)
	res, err := store.SearchSellable(context.Background(), "Apple", 10)
	if err != nil {
		t.Fatalf("SearchSellable: %v", err)
	}
	if len(res) != 1 || res[0].ItemID != "i1" {
		t.Fatalf("expected the hidden item to still be found by search, got %+v", res)
	}
}

// TestButtonStoreAdd_UnhidesItem (ut-docs#2541): explicitly adding a
// shortcut for a hidden item clears the flag, so it doesn't stay off the
// grid despite the operator just configuring a tile for it.
func TestButtonStoreAdd_UnhidesItem(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, sell_screen_hidden) VALUES('i1','S1','Apple', 100, 1, 1)`)

	store := NewButtonStore(db)
	if err := store.Add(Button{Label: "Apple", Code: "B1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	var hidden int
	if err := db.QueryRow(`SELECT sell_screen_hidden FROM items WHERE id='i1'`).Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	if hidden != 0 {
		t.Fatalf("expected Add to clear the hidden flag, still hidden=%d", hidden)
	}
}

// TestButtonStoreHideUnhide_RoundTrip and TestButtonStoreRemove_HidesTheItem
// pin ButtonStore's own Hide/Unhide/Remove wrappers (the handlers in
// buttons_api.go call these directly).
func TestButtonStoreHideUnhide_RoundTrip(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
	store := NewButtonStore(db)
	ctx := context.Background()

	if err := store.Hide(ctx, "i1"); err != nil {
		t.Fatalf("Hide: %v", err)
	}
	hidden, err := store.ListHidden(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden) != 1 || hidden[0].ItemID != "i1" {
		t.Fatalf("expected i1 listed as hidden, got %+v", hidden)
	}

	if err := store.Unhide(ctx, "i1"); err != nil {
		t.Fatalf("Unhide: %v", err)
	}
	hidden, err = store.ListHidden(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden) != 0 {
		t.Fatalf("expected no hidden items after Unhide, got %+v", hidden)
	}
}

func TestButtonStoreHide_RequiresItemID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewButtonStore(db)
	if err := store.Hide(context.Background(), ""); err == nil {
		t.Fatal("expected an error for a blank itemId")
	}
	if err := store.Unhide(context.Background(), "  "); err == nil {
		t.Fatal("expected an error for a blank itemId")
	}
}

// TestButtonStoreRemove_RemovesFromQuickButtons (ut-docs#2698): the legacy
// /api/buttons/remove (code or itemId payload) maps to the trash badge's
// remove-from-quick-buttons -- never a hide, never a deactivation.
func TestButtonStoreRemove_RemovesFromQuickButtons(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
	store := NewButtonStore(db)
	state := func() (hidden, removed, active int) {
		t.Helper()
		if err := db.QueryRow(`SELECT sell_screen_hidden, sell_screen_removed, is_active FROM items WHERE id='i1'`).Scan(&hidden, &removed, &active); err != nil {
			t.Fatal(err)
		}
		return
	}

	// By itemId directly.
	if err := store.Remove("", "i1"); err != nil {
		t.Fatalf("Remove by itemId: %v", err)
	}
	if h, r, a := state(); h != 0 || r != 1 || a != 1 {
		t.Fatalf("expected removed (not hidden, still active), got hidden=%d removed=%d active=%d", h, r, a)
	}

	// Add back, then remove by code alone (resolved via the shortcut row).
	if err := store.Add(Button{Label: "Apple", Code: "B1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, r, _ := state(); r != 0 {
		t.Fatalf("Add must clear removed, got %d", r)
	}
	if err := store.Remove("B1", ""); err != nil {
		t.Fatalf("Remove by code: %v", err)
	}
	if h, r, a := state(); h != 0 || r != 1 || a != 1 {
		t.Fatalf("expected removed again, got hidden=%d removed=%d active=%d", h, r, a)
	}
}

func TestButtonStoreRemove_UnknownCodeErrors(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewButtonStore(db)
	if err := store.Remove("never-existed", ""); err == nil {
		t.Fatal("expected an error resolving an unknown code with no itemId")
	}
	if err := store.Remove("", ""); err == nil {
		t.Fatal("expected an error for both code and itemId blank")
	}
}

// TestButtonStoreUpdateOrder_MaterializesImplicitTile (ut-docs#2541): a
// drag on a tile with no shortcut_buttons row of its own (every active,
// non-hidden item is an implicit quick button by default) must persist —
// UpdateOrder materializes a real row for it first.
func TestButtonStoreUpdateOrder_MaterializesImplicitTile(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','S2','Bread', 200, 1)`)
	store := NewButtonStore(db)
	ctx := context.Background()

	// Both i1 and i2 start with no explicit row -- Load() returns them in
	// name order (Apple, Bread), codes S1/S2 (SKU fallback).
	before, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(before) != 2 || before[0].Code != "S1" || before[1].Code != "S2" {
		t.Fatalf("unexpected pre-reorder state: %+v", before)
	}

	// Drag Bread (S2) ahead of Apple (S1) -- exactly what the sell screen's
	// jiggle Done posts: the FULL global list in the new order.
	if err := store.UpdateOrder(ctx, []string{"S2", "S1"}); err != nil {
		t.Fatalf("UpdateOrder: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM shortcut_buttons`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected both implicit tiles materialized into real rows, got %d", n)
	}

	after, err := store.Load()
	if err != nil {
		t.Fatalf("Load after reorder: %v", err)
	}
	if len(after) != 2 || after[0].Code != "S2" || after[1].Code != "S1" {
		t.Fatalf("expected the new order (S2, S1) to persist, got %+v", after)
	}
}

// TestButtonStoreUpdateOrder_UnresolvableCodeSkippedNotError (ut-docs#2541):
// a stale/tampered code with nothing behind it must not fail the whole
// reorder -- same "unknown code is a silent no-op for that one code" shape
// ShortcutsRepo.UpdateOrder already had before this card.
func TestButtonStoreUpdateOrder_UnresolvableCodeSkippedNotError(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
	store := NewButtonStore(db)

	if err := store.UpdateOrder(context.Background(), []string{"S1", "does-not-exist"}); err != nil {
		t.Fatalf("UpdateOrder with one unresolvable code: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM shortcut_buttons`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected only the resolvable code materialized, got %d rows", n)
	}
}

// TestButtonStoreUpdateOrder_OnlyMaterializesTouchedTiles (ut-docs#2541
// review finding 1): the client (app.js's utTileJiggle) posts the FULL
// global code list on every drag, not just the tiles that actually moved --
// so materializing every implicit code in that list (the pre-fix behavior)
// would turn the FIRST drag on a large catalog into a real shortcut_buttons
// row for every single implicit item, most of which the operator never
// touched. UpdateOrder must materialize only the codes at or before the
// LAST index where the posted order differs from Load()'s current order --
// trailing tiles the drag never actually reordered stay implicit.
func TestButtonStoreUpdateOrder_OnlyMaterializesTouchedTiles(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','S2','Bread', 200, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i3','S3','Cola', 150, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i4','S4','Date', 175, 1)`)
	store := NewButtonStore(db)
	ctx := context.Background()

	// All 4 are implicit -- Load()'s name order is Apple,Bread,Cola,Date
	// (S1,S2,S3,S4).
	before, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(before) != 4 || before[0].Code != "S1" || before[3].Code != "S4" {
		t.Fatalf("unexpected pre-reorder state: %+v", before)
	}

	// The drag only swapped Apple/Bread (positions 0-1); Cola/Date (2-3)
	// are posted in their SAME positions -- exactly what a drag that never
	// touched them produces, since the client always posts the full list.
	if err := store.UpdateOrder(ctx, []string{"S2", "S1", "S3", "S4"}); err != nil {
		t.Fatalf("UpdateOrder: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM shortcut_buttons`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected only the 2 swapped tiles (S1, S2) materialized, got %d rows", n)
	}
	for _, code := range []string{"S3", "S4"} {
		var exists int
		if err := db.QueryRow(`SELECT count(*) FROM shortcut_buttons WHERE barcode = ?`, code).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != 0 {
			t.Fatalf("expected %s to stay implicit (untouched by the drag), but it was materialized", code)
		}
	}

	after, err := store.Load()
	if err != nil {
		t.Fatalf("Load after reorder: %v", err)
	}
	if len(after) != 4 || after[0].Code != "S2" || after[1].Code != "S1" || after[2].Code != "S3" || after[3].Code != "S4" {
		t.Fatalf("expected order S2,S1,S3,S4 to persist, got %+v", after)
	}
}

// TestButtonStoreUpdateOrder_MaterializedRowShowsLiveItemName (ut-docs#2541
// review finding 1): a materialized row must never freeze the item's name
// (or thumbnail) at drag time -- renaming the item afterwards must still
// show the new name on its tile, exactly like an item that was never
// dragged at all. LoadButtons stores/reads the row with an empty label and
// falls back to the item's own (live) name.
func TestButtonStoreUpdateOrder_MaterializedRowShowsLiveItemName(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','S2','Bread', 200, 1)`)
	store := NewButtonStore(db)
	ctx := context.Background()

	if err := store.UpdateOrder(ctx, []string{"S2", "S1"}); err != nil {
		t.Fatalf("UpdateOrder: %v", err)
	}

	// Rename the item directly in the catalog (as the catalog page's own
	// edit would) -- no re-add, no touching shortcut_buttons at all.
	mustExec(t, db, `UPDATE items SET name = 'Granny Smith Apple' WHERE id = 'i1'`)

	after, err := store.Load()
	if err != nil {
		t.Fatalf("Load after rename: %v", err)
	}
	var got string
	for _, b := range after {
		if b.ItemID == "i1" {
			got = b.Label
		}
	}
	if got != "Granny Smith Apple" {
		t.Fatalf("materialized tile Label = %q, want the item's LIVE (renamed) name %q -- it must not have frozen at materialization time", got, "Granny Smith Apple")
	}
}

// TestButtonStoreUnhideAll (ut-docs#2614): the Designer's "Show all N on
// the sell screen" unhides every active hidden item and returns the count.
func TestButtonStoreUnhideAll(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','S2','Bread', 200, 1)`)
	store := NewButtonStore(db)
	ctx := context.Background()
	for _, id := range []string{"i1", "i2"} {
		if err := store.Hide(ctx, id); err != nil {
			t.Fatalf("Hide %s: %v", id, err)
		}
	}

	n, err := store.UnhideAll(ctx)
	if err != nil {
		t.Fatalf("UnhideAll: %v", err)
	}
	if n != 2 {
		t.Fatalf("UnhideAll n = %d, want 2", n)
	}
	hidden, err := store.ListHidden(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden) != 0 {
		t.Fatalf("expected no hidden items after UnhideAll, got %+v", hidden)
	}
}
