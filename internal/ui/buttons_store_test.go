package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

// setupFullTestDB extends the base schema with the variant + modifier
// tables the resolver chain and Load's modifier flag actually query.
func setupFullTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := setupTestDB(t)
	stmts := []string{
		`CREATE TABLE item_variants (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, name TEXT, price INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE item_modifier_groups (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, name TEXT, is_active INTEGER NOT NULL DEFAULT 1);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	return db
}

func TestToVM_MapsAllFieldsAndEmptyInput(t *testing.T) {
	in := []Button{{
		Label:        "Coffee",
		Code:         "C1",
		ItemID:       "itm1",
		ImageURL:     "/public/images/coffee.png",
		Price:        350,
		HasModifiers: true,
	}}
	out := ToVM(in)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	got := out[0]
	if got.Label != "Coffee" || got.Code != "C1" || got.ItemID != "itm1" ||
		got.ImageURL != "/public/images/coffee.png" || got.Price != 350 || !got.HasModifiers {
		t.Fatalf("unexpected VM: %+v", got)
	}

	empty := ToVM(nil)
	if empty == nil || len(empty) != 0 {
		t.Fatalf("ToVM(nil) = %#v, want empty non-nil slice", empty)
	}
}

// TestSearchResultAddVals pins the server-side hx-vals payload: it must be
// valid JSON even when fields contain quotes/backslashes (the template used
// to interpolate raw fields into a JSON literal, which broke JSON.parse in
// the Designer for any quoted item name).
func TestSearchResultAddVals_QuotesAndBackslashesSurvive(t *testing.T) {
	r := SearchResult{ItemID: "i1", Name: `5" Blade \ "special"`, Barcode: `B"1`, Image: `img"x.png`}
	var vals map[string]string
	if err := json.Unmarshal([]byte(r.AddVals()), &vals); err != nil {
		t.Fatalf("AddVals not valid JSON: %v (%s)", err, r.AddVals())
	}
	if vals["label"] != r.Name || vals["code"] != r.Barcode || vals["itemId"] != "i1" || vals["imageUrl"] != r.Image {
		t.Fatalf("unexpected vals: %#v", vals)
	}
}

// TestSearchResultAddVals_FallsBackToSKUWhenBarcodeEmpty (ut-docs#1220): a
// SKU-only item (loose produce, services — no barcode row) previously
// carried Barcode == "" into "code" here, and ButtonStore.Add rejects an
// empty code outright, so the Designer's add-as-button flow 400'd for any
// item found only via SKU search. The button-code resolution chain
// (PriceResolverAdapter) already accepts a SKU as "code", so falling back
// to it here is a pure fix, not a behavior change to that chain.
func TestSearchResultAddVals_FallsBackToSKUWhenBarcodeEmpty(t *testing.T) {
	r := SearchResult{ItemID: "i2", Name: "Loose Screw", Barcode: "", SKU: "SKU-ONLY-1", Image: ""}
	var vals map[string]string
	if err := json.Unmarshal([]byte(r.AddVals()), &vals); err != nil {
		t.Fatalf("AddVals not valid JSON: %v (%s)", err, r.AddVals())
	}
	if vals["code"] != "SKU-ONLY-1" {
		t.Fatalf("code = %q, want SKU fallback %q", vals["code"], "SKU-ONLY-1")
	}
}

// TestSearchResultAddVals_PrefersBarcodeOverSKU pins that the fallback only
// kicks in when Barcode is empty — an item with both must still post its
// barcode as "code" (unchanged behavior for the common case).
func TestSearchResultAddVals_PrefersBarcodeOverSKU(t *testing.T) {
	r := SearchResult{ItemID: "i1", Name: "Apple", Barcode: "BAR1", SKU: "SKU1"}
	var vals map[string]string
	if err := json.Unmarshal([]byte(r.AddVals()), &vals); err != nil {
		t.Fatalf("AddVals not valid JSON: %v (%s)", err, r.AddVals())
	}
	if vals["code"] != "BAR1" {
		t.Fatalf("code = %q, want barcode %q (barcode must win over SKU)", vals["code"], "BAR1")
	}
}

func TestButtonStoreLoad_ThumbnailFallbackPriceOrderAndModifiers(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Coffee', 350, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm2','SKU2','Tea', 250, 1)`)
	// itm1 has no explicit button image but a catalog thumbnail -> fallback.
	mustExec(t, db, `INSERT INTO item_images(id, item_id, role, path) VALUES('img1','itm1','thumbnail','/public/images/coffee.png')`)
	// itm1 is customizable (active modifier group); itm2 has only an INACTIVE group.
	mustExec(t, db, `INSERT INTO item_modifier_groups(id, item_id, name, is_active) VALUES('g1','itm1','Milk',1)`)
	mustExec(t, db, `INSERT INTO item_modifier_groups(id, item_id, name, is_active) VALUES('g2','itm2','Old',0)`)
	// Deliberately inserted out of display order: sort_order must win.
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,image_path,sort_order) VALUES('T1','Tea Tile','itm2','/public/images/tea-explicit.png',0)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,image_path,sort_order) VALUES('C1','Coffee Tile','itm1',NULL,1)`)

	store := NewButtonStore(db)
	btns, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(btns) != 2 {
		t.Fatalf("len = %d, want 2", len(btns))
	}
	tea, coffee := btns[0], btns[1]
	if tea.Label != "Tea Tile" || coffee.Label != "Coffee Tile" {
		t.Fatalf("sort_order not honored: %+v", btns)
	}
	if tea.ImageURL != "/public/images/tea-explicit.png" {
		t.Fatalf("explicit image lost: %q", tea.ImageURL)
	}
	if coffee.ImageURL != "/public/images/coffee.png" {
		t.Fatalf("thumbnail fallback missing: %q", coffee.ImageURL)
	}
	if coffee.Price != 350 || tea.Price != 250 {
		t.Fatalf("prices wrong: %+v", btns)
	}
	if !coffee.HasModifiers {
		t.Fatalf("coffee should flag modifiers (active group)")
	}
	if tea.HasModifiers {
		t.Fatalf("tea must not flag modifiers (group inactive)")
	}
}

// Save has no production caller today (the Designer adds/removes/reorders
// one button at a time) — this pins its replace-all contract so wiring it
// up later doesn't inherit surprises.
func TestButtonStoreSave_ReplacesAllAndPersistsOrder(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()
	store := NewButtonStore(db)

	if err := store.Save([]Button{
		{Label: "B", Code: "B1", ItemID: "i1"},
		{Label: "A", Code: "A1", ItemID: "i2"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	btns, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(btns) != 2 || btns[0].Code != "B1" || btns[1].Code != "A1" {
		t.Fatalf("list order not persisted: %+v", btns)
	}

	// Second Save fully replaces, never merges.
	if err := store.Save([]Button{{Label: "C", Code: "C1", ItemID: "i3"}}); err != nil {
		t.Fatalf("Save replace: %v", err)
	}
	btns, _ = store.Load()
	if len(btns) != 1 || btns[0].Code != "C1" {
		t.Fatalf("Save did not replace: %+v", btns)
	}
}

func TestButtonStoreUpdateOrderAndRemove(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()
	store := NewButtonStore(db)

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','One', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','S2','Two', 200, 1)`)
	if err := store.Add(Button{Label: "One", Code: "B1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.Add(Button{Label: "Two", Code: "B2", ItemID: "i2"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := store.UpdateOrder(context.Background(), []string{"B2", "B1"}); err != nil {
		t.Fatalf("UpdateOrder: %v", err)
	}
	btns, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(btns) != 2 || btns[0].Code != "B2" || btns[1].Code != "B1" {
		t.Fatalf("reorder not applied: %+v", btns)
	}

	if err := store.Remove("B2"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	btns, _ = store.Load()
	if len(btns) != 1 || btns[0].Code != "B1" {
		t.Fatalf("remove not applied: %+v", btns)
	}
}

func TestButtonStoreAdd_TrimsAndUpserts(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()
	store := NewButtonStore(db)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','One', 100, 1)`)

	if err := store.Add(Button{Label: "  Padded  ", Code: " B1 ", ItemID: " i1 "}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	btns, _ := store.Load()
	if len(btns) != 1 || btns[0].Label != "Padded" || btns[0].Code != "B1" || btns[0].ItemID != "i1" {
		t.Fatalf("fields not trimmed: %+v", btns)
	}

	// Same code again updates in place (repo upserts on barcode).
	if err := store.Add(Button{Label: "Renamed", Code: "B1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add upsert: %v", err)
	}
	btns, _ = store.Load()
	if len(btns) != 1 || btns[0].Label != "Renamed" {
		t.Fatalf("upsert did not update label: %+v", btns)
	}
}

func TestSearchItems_MatchesNameSkuBarcodeWithPaging(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()
	store := NewButtonStore(db)

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','APL-01','Apple Juice', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','ORG-01','Orange Juice', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i3','ZZZ-01','Inactive Juice', 100, 0)`)
	mustExec(t, db, `INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('111','i1',0)`)
	mustExec(t, db, `INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('222','i1',1)`)
	mustExec(t, db, `INSERT INTO item_images(id, item_id, role, path) VALUES('im1','i1','thumbnail','/public/images/apple.png')`)

	ctx := context.Background()

	// Name match; primary barcode preferred; thumbnail carried through.
	res, err := store.SearchItems(ctx, "Apple", 0, 10)
	if err != nil {
		t.Fatalf("SearchItems: %v", err)
	}
	if len(res) != 1 || res[0].ItemID != "i1" || res[0].Barcode != "222" || res[0].Image != "/public/images/apple.png" {
		t.Fatalf("unexpected name-match result: %+v", res)
	}

	// SKU match — i2 deliberately has NO barcode rows: an item without a
	// barcode (loose produce, services) must still be searchable, with an
	// empty barcode, not fail the whole result set with a NULL-scan error.
	// It must also carry its SKU through (ut-docs#1220) so AddVals can fall
	// back to it as the button's "code" — without this, adding a
	// barcode-less item as a sale-screen button posts code="" and 400s.
	res, err = store.SearchItems(ctx, "ORG-01", 0, 10)
	if err != nil {
		t.Fatalf("sku search errored (barcode-less item broke the scan): %v", err)
	}
	if len(res) != 1 || res[0].ItemID != "i2" || res[0].Barcode != "" {
		t.Fatalf("sku match failed: %+v", res)
	}
	if res[0].SKU != "ORG-01" {
		t.Fatalf("SKU = %q, want %q (SKU must be carried through even when Barcode is empty)", res[0].SKU, "ORG-01")
	}
	if res, _ = store.SearchItems(ctx, "111", 0, 10); len(res) != 1 || res[0].ItemID != "i1" {
		t.Fatalf("barcode match failed: %+v", res)
	}

	// Inactive items are never offered.
	if res, _ = store.SearchItems(ctx, "Inactive", 0, 10); len(res) != 0 {
		t.Fatalf("inactive item leaked: %+v", res)
	}

	// Paging: "Juice" matches both active items; page size 1, offset walks.
	if res, _ = store.SearchItems(ctx, "Juice", 0, 1); len(res) != 1 || res[0].Name != "Apple Juice" {
		t.Fatalf("page 1 wrong: %+v", res)
	}
	if res, _ = store.SearchItems(ctx, "Juice", 1, 1); len(res) != 1 || res[0].Name != "Orange Juice" {
		t.Fatalf("page 2 wrong: %+v", res)
	}
}

func TestSearchItems_ErrorSurfaces(t *testing.T) {
	db := setupFullTestDB(t)
	store := NewButtonStore(db)
	db.Close() // force the query to fail
	if _, err := store.SearchItems(context.Background(), "anything", 0, 10); err == nil {
		t.Fatalf("expected error from closed DB")
	}
}

func TestLoad_ErrorSurfaces(t *testing.T) {
	db := setupFullTestDB(t)
	store := NewButtonStore(db)
	db.Close()
	if _, err := store.Load(); err == nil {
		t.Fatalf("expected error from closed DB")
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
