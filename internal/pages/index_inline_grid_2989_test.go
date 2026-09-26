package pages

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2989: GET / used to ship .products as an empty
// hx-get="/ui/buttons" hx-trigger="load" placeholder, so the tiles only
// arrived in a second request after the page painted. The sale screen's grid
// is now inlined into GET / itself, from the same #2501 cache entry
// /ui/buttons serves; a failed render falls back to the lazy placeholder.

const lazyProductsPlaceholder = `<div class="products" hx-get="/ui/buttons" hx-trigger="load" hx-swap="outerHTML"></div>`

func newIndexAndButtonsMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, d := newButtonsMux(t)
	registerIndex(mux, d)
	return mux, d
}

func getOK(t *testing.T, mux *http.ServeMux, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %.500s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestIndex_InlinesSaleScreenGrid(t *testing.T) {
	mux, d := newIndexAndButtonsMux(t)
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES ('J1','First','itm1',0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Warm once: the very first sell-screen render lazily seeds a setting,
	// which moves the version (see newSellScreenFixture in internal/ui).
	getOK(t, mux, "/ui/buttons")

	home := getOK(t, mux, "/")
	if strings.Contains(home, lazyProductsPlaceholder) {
		t.Fatalf("GET / still ships the lazy products placeholder")
	}
	for _, want := range []string{`id="buttons-grid"`, `class="btn-tile`, `data-code="J1"`, `<template id="tile-badges-tpl"`, `data-sell-version="`} {
		if !strings.Contains(home, want) {
			t.Fatalf("GET / missing inline grid marker %q", want)
		}
	}
	// Byte-for-byte the fragment /ui/buttons serves (same renderer inputs,
	// same cache entry), so a later buttons-changed refetch swaps in an
	// identical root.
	frag := getOK(t, mux, "/ui/buttons")
	if !strings.Contains(home, strings.TrimSpace(frag)) {
		t.Fatalf("GET /'s inline grid differs from GET /ui/buttons' fragment")
	}
	if d.SellScreenCache() != d.SellScreenCache() || d.SellScreenCache() == nil {
		t.Fatal("Deps.SellScreenCache must be one shared cache")
	}
}

func TestIndex_InlineGridRenderFailureFallsBackToPlaceholder(t *testing.T) {
	mux, _ := newIndexAndButtonsMux(t)
	orig := saleGridFirstPaint
	saleGridFirstPaint = func(*common.Deps, http.ResponseWriter, *http.Request) template.HTML { return "" }
	t.Cleanup(func() { saleGridFirstPaint = orig })

	home := getOK(t, mux, "/")
	if !strings.Contains(home, lazyProductsPlaceholder) {
		t.Fatalf("a failed inline render must fall back to the lazy placeholder, never a blank grid")
	}
	if strings.Contains(home, `id="buttons-grid"`) {
		t.Fatal("fallback page must not carry a (partial) grid")
	}
}

// A Deps without a BtnStore (many index tests build one) renders the
// placeholder rather than panicking.
func TestIndex_InlineGridNoButtonStoreFallsBack(t *testing.T) {
	mux, d := newButtonsMux(t)
	d.BtnStore = nil
	registerIndex(mux, d)
	if home := getOK(t, mux, "/"); !strings.Contains(home, lazyProductsPlaceholder) {
		t.Fatal("no BtnStore: GET / must keep the lazy placeholder")
	}
}
