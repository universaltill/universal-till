package ui

// ut-docs#2209: tapping a sale-screen tile whose item has variants
// (small/regular/large) must open the picker, exactly like a tile whose
// item has modifiers — never straight to the basket at the parent item's
// own base price. Mirrors TestButtonStoreLoad_ThumbnailFallbackPriceOrderAndModifiers's
// pattern for HasModifiers.

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

func TestButtonStoreLoad_FlagsVariants(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-sized','SKU1','Coffee', 320, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-plain','SKU2','Water', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-only-retired','SKU3','Mug', 300, 1)`)
	mustExec(t, db, `INSERT INTO item_variants(id, item_id, sku, name, price, is_active) VALUES('v-small','itm-sized','SIZED-S','Small', 280, 1)`)
	mustExec(t, db, `INSERT INTO item_variants(id, item_id, sku, name, price, is_active) VALUES('v-large','itm-sized','SIZED-L','Large', 380, 1)`)
	// itm-only-retired's only variant is INACTIVE — must not flag HasVariants.
	mustExec(t, db, `INSERT INTO item_variants(id, item_id, sku, name, price, is_active) VALUES('v-old','itm-only-retired','RET-1','Old', 300, 0)`)

	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('C1','Coffee Tile','itm-sized',0)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('W1','Water Tile','itm-plain',1)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('M1','Mug Tile','itm-only-retired',2)`)

	store := NewButtonStore(db)
	btns, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(btns) != 3 {
		t.Fatalf("len = %d, want 3", len(btns))
	}
	byLabel := map[string]Button{}
	for _, b := range btns {
		byLabel[b.Label] = b
	}
	if !byLabel["Coffee Tile"].HasVariants {
		t.Fatal("coffee should flag HasVariants (active variants)")
	}
	if byLabel["Water Tile"].HasVariants {
		t.Fatal("water must not flag HasVariants (no variants at all)")
	}
	if byLabel["Mug Tile"].HasVariants {
		t.Fatal("mug must not flag HasVariants (its only variant is retired)")
	}
}

func TestToVM_MapsHasVariants(t *testing.T) {
	out := ToVM([]Button{{Label: "Coffee", HasVariants: true}})
	if len(out) != 1 || !out[0].HasVariants {
		t.Fatalf("expected HasVariants to survive ToVM: %+v", out)
	}
}

// Full Button -> ButtonVM -> product-tile (buttons.html) pipeline: a
// variant-only item (no modifiers) must render the hx-get picker branch,
// not the one-tap hx-post-straight-to-basket branch — this is the actual
// bug ut-docs#2209 reports (every sized drink rang at one price).
func TestButtonsHTTPList_VariantOnlyItemOpensPickerNotStraightToBasket(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Latte', 320, 1)`)
	mustExec(t, db, `INSERT INTO item_variants(id, item_id, sku, name, price, is_active) VALUES('v1','i1','I1-S','Small', 280, 1)`)
	store := NewButtonStore(db)
	renderer, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", "buttons.html"),
		httpx.FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	h := &ButtonsHTTP{Store: *store, View: renderer}
	if err := store.Add(Button{Label: "Latte Tile", Code: "C1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-get="/ui/pos/modifiers?item=i1&code=C1"`) {
		t.Fatalf("expected the variant-only tile to open the picker via hx-get: %s", body)
	}
	if strings.Contains(body, `hx-post="/api/pos/scan"`) {
		t.Fatalf("a variant-bearing tile must NOT also render the straight-to-basket branch: %s", body)
	}
}

// The control case: an item with neither modifiers nor variants must keep
// the existing one-tap behavior.
func TestButtonsHTTPList_PlainItemStillScansStraightToBasket(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Water', 100, 1)`)
	store := NewButtonStore(db)
	renderer, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", "buttons.html"),
		httpx.FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	h := &ButtonsHTTP{Store: *store, View: renderer}
	if err := store.Add(Button{Label: "Water Tile", Code: "C1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-post="/api/pos/scan"`) {
		t.Fatalf("a plain item's tile must still scan straight to the basket: %s", body)
	}
	if strings.Contains(body, "/ui/pos/modifiers") {
		t.Fatalf("a plain item's tile must not open any picker: %s", body)
	}
}
