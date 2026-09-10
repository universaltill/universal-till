package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestInventoryPage_StockRowsCarryDialogAttributes covers ut-docs#2011: a
// tapped stock row must open a full-screen popup prefilled with that row's
// item AND location — which needs the location's NAME on the row, not just
// its id (the id alone can't populate the dialog's "which item/location is
// this" confirmation line). Also asserts the dialog shell markup itself
// exists with the ids the page's own JS and this pattern's CSS
// (.record-dialog, ut-docs#2010) key off.
func TestInventoryPage_StockRowsCarryDialogAttributes(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	registerInventoryPage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/inventory", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, `class="stock-row"`) {
		t.Fatalf("expected stock rows to carry class=stock-row, got: %s", body)
	}
	if !strings.Contains(body, `data-location-name=`) {
		t.Fatalf("expected stock rows to carry data-location-name (needed to prefill the dialog's confirmation line), got: %s", body)
	}

	// The full-screen dialog shell (ut-docs#2010 pattern, NOT the shared
	// record_dialog partial/engine -- see inventory.html's own comment for
	// why: /api/inventory/receipt is an htmx swap-in-place endpoint, not a
	// plain-POST-redirect one).
	if !strings.Contains(body, `id="stock-dialog"`) {
		t.Fatalf("expected a #stock-dialog full-screen popup, got: %s", body)
	}
	if !strings.Contains(body, `class="record-dialog"`) {
		t.Fatalf("expected #stock-dialog to reuse the record-dialog CSS shell (ut-docs#2010), got: %s", body)
	}
	if !strings.Contains(body, `aria-labelledby="stock-dialog-title"`) {
		t.Fatalf("expected #stock-dialog to be labelled by its title for a11y, got: %s", body)
	}
	if !strings.Contains(body, `id="stock-dialog-title"`) {
		t.Fatalf("expected a #stock-dialog-title heading, got: %s", body)
	}
	if !strings.Contains(body, `id="stock-dialog-close"`) {
		t.Fatalf("expected an explicit close button (id=stock-dialog-close), got: %s", body)
	}
	if !strings.Contains(body, `id="stock-dialog-context"`) {
		t.Fatalf("expected a #stock-dialog-context confirmation line (which item/location is about to change), got: %s", body)
	}
	if !strings.Contains(body, `id="stock-dialog-open"`) {
		t.Fatalf("expected a header \"Add stock\" button (id=stock-dialog-open) for the no-row-tapped path, got: %s", body)
	}

	// The existing Receive/Adjust form must now live INSIDE the dialog body,
	// not sit as a separately-scrolled-to card -- otherwise the popup is
	// decorative and the merchant still has to scroll to find the fields.
	dialogStart := strings.Index(body, `id="stock-dialog"`)
	formIdx := strings.Index(body, `id="stock-form"`)
	dialogEnd := strings.Index(body[dialogStart:], "</dialog>")
	if dialogStart < 0 || formIdx < 0 || dialogEnd < 0 || formIdx < dialogStart || formIdx > dialogStart+dialogEnd {
		t.Fatalf("expected #stock-form to be nested inside #stock-dialog, got: %s", body)
	}

	// Override/Return stay untouched, outside the dialog (not moved).
	if !strings.Contains(body, `id="override-form"`) || !strings.Contains(body, `id="return-form"`) {
		t.Fatalf("expected override-form/return-form to remain present unchanged, got: %s", body)
	}
	overrideIdx := strings.Index(body, `id="override-form"`)
	if overrideIdx >= dialogStart && overrideIdx <= dialogStart+dialogEnd {
		t.Fatalf("override-form must NOT be moved inside the new stock dialog, got: %s", body)
	}
}

// TestGetLowStock_RowsCarryDialogAttributes covers the same tap-to-open
// wiring for the separate /api/inventory/low-stock htmx fragment (a
// hand-built HTML string, not a Go template) -- ut-docs#2011's AC allows
// stating this out of scope, but the data (ItemID/LocationID) is already
// present on data.LowStockItem, so it's wired instead.
func TestGetLowStock_RowsCarryDialogAttributes(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `UPDATE inventory SET quantity = 1 WHERE item_id = 'itm1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `UPDATE items SET reorder_level = 10 WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/inventory/low-stock", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class='stock-row'`) {
		t.Fatalf("expected low-stock rows to share the stock-row class so the same tap-to-open listener covers them, got: %s", body)
	}
	if !strings.Contains(body, `data-item='itm1'`) {
		t.Fatalf("expected low-stock row to carry data-item, got: %s", body)
	}
	if !strings.Contains(body, "data-location-name=") {
		t.Fatalf("expected low-stock row to carry data-location-name, got: %s", body)
	}
}
