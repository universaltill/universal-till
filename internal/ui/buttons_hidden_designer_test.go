package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#2541: the Designer's "Hidden from sell screen (N)" section
// (EditMode only) — an empty state when nothing is hidden, and a row per
// hidden item with an Unhide button once something is.
func TestButtonsHTTPList_EditModeRendersHiddenSection(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")
	h.Granted = true
	h.EditMode = true

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons?mode=edit", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-testid="designer-hidden"`) {
		t.Fatalf("expected the designer-hidden section, got: %s", body)
	}
	if !strings.Contains(body, `data-testid="designer-hidden-empty"`) {
		t.Fatalf("expected the empty state with nothing hidden, got: %s", body)
	}
	if strings.Contains(body, "designer-hidden-unhide-i1") {
		t.Fatalf("expected no unhide row before anything is hidden, got: %s", body)
	}

	if err := h.Store.Hide(t.Context(), "i1"); err != nil {
		t.Fatalf("Hide: %v", err)
	}
	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons?mode=edit", nil))
	body = rec.Body.String()
	if strings.Contains(body, `data-testid="designer-hidden-empty"`) {
		t.Fatalf("expected the empty state gone once an item is hidden, got: %s", body)
	}
	if !strings.Contains(body, `data-testid="designer-hidden-item-i1"`) {
		t.Fatalf("expected a hidden-item row for i1, got: %s", body)
	}
	if !strings.Contains(body, `data-testid="designer-hidden-unhide-i1"`) {
		t.Fatalf("expected an Unhide button for i1, got: %s", body)
	}
	if !strings.Contains(body, `hx-post="/api/buttons/unhide"`) {
		t.Fatalf("expected the Unhide button to post to /api/buttons/unhide, got: %s", body)
	}
	// jsonVals's output is HTML-attribute-escaped by html/template, so the
	// literal quotes render as &#34; — matches the same shape every other
	// hx-vals assertion in this package checks.
	if !strings.Contains(body, `&#34;itemId&#34;:&#34;i1&#34;`) {
		t.Fatalf("expected the Unhide button's hx-vals to carry itemId i1, got: %s", body)
	}
}

// TestButtonsHTTPList_SaleScreenOmitsHiddenSection pins that the hidden
// section is EditMode-only -- the sale screen itself never renders it.
func TestButtonsHTTPList_SaleScreenOmitsHiddenSection(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
	if err := h.Store.Hide(t.Context(), "i1"); err != nil {
		t.Fatalf("Hide: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	body := rec.Body.String()
	if strings.Contains(body, `data-testid="designer-hidden"`) {
		t.Fatalf("expected no hidden section on the sale screen, got: %s", body)
	}
}
