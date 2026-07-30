package ui

import (
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// newButtonsHTTP wires ButtonsHTTP exactly as internal/pages does: the
// real embedded templates via NewRenderer, the real store over sqlite.
func newButtonsHTTP(t *testing.T, partial string) (*ButtonsHTTP, *ButtonStore) {
	t.Helper()
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := NewButtonStore(db)
	renderer, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", partial),
		httpx.FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Coffee', 350, 1)`)
	return &ButtonsHTTP{Store: *store, View: renderer}, store
}

func TestButtonsHTTPList_RendersTiles(t *testing.T) {
	h, store := newButtonsHTTP(t, "buttons.html")
	if err := store.Add(Button{Label: "Coffee Tile", Code: "C1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Coffee Tile") {
		t.Fatalf("rendered fragment missing tile label: %s", rec.Body.String())
	}
}

func TestButtonsHTTPAdd_NormalizesImageAndRendersGrid(t *testing.T) {
	h, store := newButtonsHTTP(t, "buttons_admin.html")

	cases := []struct {
		in, want string
	}{
		{"coffee.png", "/public/images/coffee.png"},                // bare filename -> local images folder
		{"/public/uploads/c.png", "/public/uploads/c.png"},         // already public path, untouched
		{"https://cdn.example/c.png", "https://cdn.example/c.png"}, // absolute URL, untouched
	}
	for i, tc := range cases {
		form := url.Values{"label": {"Coffee"}, "code": {"C" + string(rune('1'+i))}, "itemId": {"i1"}, "imageUrl": {tc.in}}
		req := httptest.NewRequest("POST", "/api/buttons/add", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.Add(rec, req)
		if rec.Code != 200 {
			t.Fatalf("Add(%q) = %d (%s)", tc.in, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "buttons-grid-admin") {
			t.Fatalf("Add did not re-render admin grid: %s", rec.Body.String())
		}
	}
	btns, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(btns) != len(cases) {
		t.Fatalf("buttons = %d, want %d", len(btns), len(cases))
	}
	got := map[string]string{}
	for _, b := range btns {
		got[b.Code] = b.ImageURL
	}
	for i, tc := range cases {
		code := "C" + string(rune('1'+i))
		if got[code] != tc.want {
			t.Fatalf("image for %s = %q, want %q", code, got[code], tc.want)
		}
	}
}

func TestButtonsHTTPAdd_ErrorPaths(t *testing.T) {
	h, _ := newButtonsHTTP(t, "buttons_admin.html")

	// Store validation error -> 400 (missing itemId).
	form := url.Values{"label": {"X"}, "code": {"C9"}}
	req := httptest.NewRequest("POST", "/api/buttons/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Add(rec, req)
	if rec.Code != 400 {
		t.Fatalf("Add missing fields = %d, want 400", rec.Code)
	}

	// Malformed percent-encoding -> ParseForm error -> 400.
	req = httptest.NewRequest("POST", "/api/buttons/add", strings.NewReader("label=%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.Add(rec, req)
	if rec.Code != 400 {
		t.Fatalf("Add bad form = %d, want 400", rec.Code)
	}
}

func TestButtonsHTTPRemove_DeletesAndRerenders(t *testing.T) {
	h, store := newButtonsHTTP(t, "buttons_admin.html")
	if err := store.Add(Button{Label: "Coffee", Code: "C1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	form := url.Values{"code": {"C1"}}
	req := httptest.NewRequest("POST", "/api/buttons/remove", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Remove(rec, req)
	if rec.Code != 200 {
		t.Fatalf("Remove = %d (%s)", rec.Code, rec.Body.String())
	}
	if btns, _ := store.Load(); len(btns) != 0 {
		t.Fatalf("button not removed: %+v", btns)
	}

	// Malformed body -> 400.
	req = httptest.NewRequest("POST", "/api/buttons/remove", strings.NewReader("code=%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.Remove(rec, req)
	if rec.Code != 400 {
		t.Fatalf("Remove bad form = %d, want 400", rec.Code)
	}
}

func TestButtonsHTTPRemove_StoreErrorIs400(t *testing.T) {
	db := setupFullTestDB(t)
	store := NewButtonStore(db)
	renderer, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", "buttons_admin.html"),
		httpx.FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	h := &ButtonsHTTP{Store: *store, View: renderer}
	db.Close() // repo delete will fail

	form := url.Values{"code": {"C1"}}
	req := httptest.NewRequest("POST", "/api/buttons/remove", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Remove(rec, req)
	if rec.Code != 400 {
		t.Fatalf("Remove with failing store = %d, want 400", rec.Code)
	}
}

func TestNewRenderer_ErrorOnMissingTemplate(t *testing.T) {
	_, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "does-not-exist.html"),
		filepath.Join("web", "ui", "partials", "buttons.html"),
		httpx.FuncsFor("en"),
	)
	if err == nil {
		t.Fatalf("expected error for missing template")
	}
}
