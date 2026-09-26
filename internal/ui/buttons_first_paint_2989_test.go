package ui

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ut-docs#2989: the sale screen's tiles must be in GET /'s first paint, and
// the fragment must be small enough to inline — measured on a real tablet,
// 52% of a 230-tile /ui/buttons was the jiggle-mode badges, rendered on every
// tile although they only show after a long-press. The sale screen now ships
// them ONCE, as <template id="tile-badges-tpl">, and app.js's utTileJiggle
// materialises them on entering edit mode. The Designer (EditMode) keeps
// server-rendering them per tile.

const tileBadgesTplOpen = `<template id="tile-badges-tpl"`

// splitBadgeTemplate returns the body with the badge template cut out, and
// the template's own markup.
func splitBadgeTemplate(t *testing.T, body string) (rest, tpl string) {
	t.Helper()
	start := strings.Index(body, tileBadgesTplOpen)
	if start < 0 {
		return body, ""
	}
	end := strings.Index(body[start:], `</template>`)
	if end < 0 {
		t.Fatalf("unterminated badge template: %.500s", body[start:])
	}
	end += start + len(`</template>`)
	if strings.Count(body, tileBadgesTplOpen) != 1 {
		t.Fatalf("want exactly one badge template per grid, got %d", strings.Count(body, tileBadgesTplOpen))
	}
	return body[:start] + body[end:], body[start:end]
}

func seedTwoTilesOneHidden(t *testing.T, h *ButtonsHTTP, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','S2','Bread', 200, 1)`)
	if err := h.Store.Hide(t.Context(), "i1"); err != nil {
		t.Fatal(err)
	}
}

func listBody(t *testing.T, h *ButtonsHTTP) (string, http.Header) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String(), rec.Header()
}

func TestButtonsHTTPList_SaleScreenShipsBadgesOnceAsTemplate(t *testing.T) {
	for _, granted := range []bool{true, false} {
		h, db := newButtonsHTTPWithDB(t, "buttons.html")
		h.Granted = granted
		seedTwoTilesOneHidden(t, h, db)
		body, _ := listBody(t, h)
		rest, tpl := splitBadgeTemplate(t, body)
		if tpl == "" {
			t.Fatalf("granted=%v: sale screen has no %s: %.1500s", granted, tileBadgesTplOpen, body)
		}
		if strings.Contains(rest, "tile-badge") {
			t.Fatalf("granted=%v: sale-screen tiles must carry no tile-badge markup outside the template: %.3000s", granted, rest)
		}
		// Every tile cell carries what the client needs to fill the template.
		apple := tileCell(rest, "i1")
		bread := tileCell(rest, "i2")
		if !regexp.MustCompile(`<div class="tile-cell[^"]*"[^>]*data-item-id="i1"[^>]*data-hidden`).MatchString(apple) {
			t.Fatalf("granted=%v: hidden tile cell needs data-item-id + data-hidden: %s", granted, apple)
		}
		if !regexp.MustCompile(`<div class="tile-cell"[^>]*data-item-id="i2"`).MatchString(bread) ||
			regexp.MustCompile(`<div class="tile-cell"[^>]*data-hidden`).MatchString(bread) {
			t.Fatalf("granted=%v: visible tile cell needs data-item-id and no data-hidden: %s", granted, bread)
		}
		// One badge set with both hide variants; placeholders, never item data.
		for _, want := range []string{
			`class="tile-badges"`,
			`data-testid="tile-badge-edit"`,
			`href="/catalog?item=` + TileBadgeItemPlaceholder + `&return=/"`,
			`data-testid="tile-badge-remove"`,
			`hx-post="/api/buttons/remove-from-grid"`,
			`data-testid="tile-badge-hide"`,
			`hx-post="/api/buttons/hide"`,
			`data-testid="tile-badge-unhide"`,
			`hx-post="/api/buttons/unhide"`,
			TileBadgeLabelPlaceholder,
		} {
			if !strings.Contains(tpl, want) {
				t.Fatalf("granted=%v: badge template missing %q: %s", granted, want, tpl)
			}
		}
		if strings.Contains(tpl, "Apple") || strings.Contains(tpl, "Bread") || strings.Contains(tpl, `"i1"`) || strings.Contains(tpl, `"i2"`) {
			t.Fatalf("granted=%v: badge template must hold placeholders only, no item data: %s", granted, tpl)
		}
		// Lock state is per session, the same for every tile, so it lives in
		// the template itself (ut-docs#2361 markup, unchanged).
		locked := strings.Contains(tpl, `data-testid="tile-badge-lock-edit"`)
		if locked == granted {
			t.Fatalf("granted=%v: template lock dots present=%v: %s", granted, locked, tpl)
		}
		if hasConfirm := strings.Contains(tpl, "hx-confirm="); hasConfirm != granted {
			t.Fatalf("granted=%v: remove hx-confirm present=%v (none when locked): %s", granted, hasConfirm, tpl)
		}
	}
}

func TestButtonsHTTPList_DesignerKeepsServerRenderedBadges(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")
	h.Granted = true
	h.EditMode = true
	seedTwoTilesOneHidden(t, h, db)
	body, _ := listBody(t, h)
	if strings.Contains(body, tileBadgesTplOpen) {
		t.Fatalf("the Designer renders badges per tile; it needs no badge template: %.1500s", body)
	}
	apple := tileCell(body, "i1")
	bread := tileCell(body, "i2")
	if !strings.Contains(apple, `data-testid="tile-badge-unhide"`) || !strings.Contains(apple, `href="/catalog?item=i1&return=/designer"`) {
		t.Fatalf("Designer hidden tile lost its server-rendered badges: %s", apple)
	}
	if !strings.Contains(bread, `data-testid="tile-badge-hide"`) || !strings.Contains(bread, `data-testid="tile-badge-remove"`) {
		t.Fatalf("Designer visible tile lost its server-rendered badges: %s", bread)
	}
}

// The fragment root carries the sell version it was rendered at, the same
// value as the X-UT-Sell-Version header, on a fresh render AND a cache hit —
// so the watcher can seed from GET /'s inline grid, which has no header.
func TestButtonsHTTPList_RootCarriesSellVersion(t *testing.T) {
	f := newSellScreenFixture(t)
	h := f.handler(t, true, false)
	rootVersion := regexp.MustCompile(`<div class="products"[^>]*data-sell-version="(\d+)"`)
	check := func(label string) string {
		rec := httptest.NewRecorder()
		h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
		m := rootVersion.FindStringSubmatch(rec.Body.String())
		if m == nil {
			t.Fatalf("%s: fragment root has no data-sell-version: %.600s", label, rec.Body.String())
		}
		if hdr := rec.Header().Get(SellVersionHeader); hdr != m[1] {
			t.Fatalf("%s: data-sell-version=%s but %s=%s", label, m[1], SellVersionHeader, hdr)
		}
		return m[1]
	}
	first := check("miss")
	if again := check("hit"); again != first {
		t.Fatalf("hit carried version %s, miss %s", again, first)
	}
	// A catalog write moves the version; the next render must carry the new
	// one, never the cached body's old one.
	f.exec(t, `UPDATE items SET name = 'Cola Tin' WHERE id = 'itm-cola'`)
	v, _, err := f.store.SellGeneration(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after := check("after write"); after != strconv.FormatInt(v, 10) || after == first {
		t.Fatalf("after a catalog write: data-sell-version=%s, SellGeneration=%d, before=%s", after, v, first)
	}
}

func TestButtonsHTTP_ListFragment(t *testing.T) {
	f := newSellScreenFixture(t)
	h := f.handler(t, true, false)
	body, ok := h.ListFragment(httptest.NewRequest("GET", "/", nil))
	if !ok || !strings.Contains(string(body), `id="buttons-grid"`) || !strings.Contains(string(body), "Cola Can") {
		t.Fatalf("ListFragment ok=%v body=%.600s", ok, body)
	}
	// Same cache entry as GET /ui/buttons: the fragment is served from it.
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Body.String() != string(body) {
		t.Fatal("ListFragment and List rendered different bytes")
	}
	if _, q := f.list(t, h); q != 1 {
		t.Fatalf("List after ListFragment ran %d SELECTs — not served from the entry ListFragment stored", q)
	}

	failing := *h
	failing.Cache = nil
	failing.View = failingView{}
	if body, ok := failing.ListFragment(httptest.NewRequest("GET", "/", nil)); ok {
		t.Fatalf("a failed render must report !ok, got body %.200s", body)
	}
}
