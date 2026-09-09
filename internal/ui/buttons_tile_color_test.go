package ui

// ut-docs#1901: a photo-less item's saved tile color must reach the
// rendered sale-screen tile — the full Button -> ButtonVM ->
// product-tile (buttons.html) pipeline, not just the intermediate Go
// structs. An item WITH an image must keep showing its photo regardless
// of Color (Color is a photo-less fallback, never an override).

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"path/filepath"
)

func TestButtonsHTTPList_RendersTileColorForPhotolessItem(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, color) VALUES('i1','S1','Latte', 320, 1, '#0f172a')`)
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
	if !strings.Contains(body, "tile-colored") {
		t.Fatalf("expected the photo-less tile to carry the tile-colored class: %s", body)
	}
	if !strings.Contains(body, "--tile-color: #0f172a") {
		t.Fatalf("expected the tile's --tile-color custom property to be set: %s", body)
	}
}

// An item WITH an image must render its photo, never the color
// background — Color is a photo-less fallback only.
func TestButtonsHTTPList_ImageWinsOverColor(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, color) VALUES('i1','S1','Latte', 320, 1, '#0f172a')`)
	mustExec(t, db, `INSERT INTO item_images(id, item_id, role, path) VALUES('img1','i1','thumbnail','/public/assets/items/i1/thumb.png')`)
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
	if strings.Contains(body, "tile-colored") || strings.Contains(body, "--tile-color") {
		t.Fatalf("an imaged item must never render the color background: %s", body)
	}
	if !strings.Contains(body, `class="thumb"`) {
		t.Fatalf("expected the item's photo to render: %s", body)
	}
}
