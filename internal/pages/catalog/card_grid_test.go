package catalog

// ut-docs#1951: the catalog list moved from a table to a SumUp
// Library-style card grid, and the per-card edit/delete buttons moved into
// the item-edit dialog. These tests pin the new shape directly (as
// distinct from thumbnail_column_test.go's thumbnail-specific coverage
// and tax_code_display_test.go/catalog_lookup_display_test.go's
// no-raw-id coverage).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func TestCatalogPage_ItemRendersAsACardNotATableRow(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Latte", BasePrice: 350, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// A real <button>, reusing the sale-screen's own tile classes — not a
	// <tr>/<td> table row, and not a bare unfocusable <div>.
	if !strings.Contains(body, `<button type="button" class="catalog-row btn-tile`) {
		t.Fatalf("expected the item to render as a .catalog-row.btn-tile <button>; got:\n%s", body)
	}
	// Scoped to the grid container itself, not the whole page — the item-edit
	// dialog or other page chrome could legitimately grow an unrelated
	// <table> of its own someday without that being this test's concern.
	// catalog.html's own layout puts the grid immediately before the item
	// dialog, so that dialog's own opening tag is a stable right boundary.
	gridStart := strings.Index(body, `id="catalog-table"`)
	dialogStart := strings.Index(body, `<dialog id="item-form-modal"`)
	if gridStart == -1 || dialogStart == -1 || dialogStart < gridStart {
		t.Fatalf("expected the #catalog-table grid container ahead of the item dialog; got:\n%s", body)
	}
	if strings.Contains(body[gridStart:dialogStart], "<table") {
		t.Fatalf("expected no <table> inside the card grid; got:\n%s", body[gridStart:dialogStart])
	}
	// No per-card edit/delete controls — selecting the card opens the
	// dialog, which now owns both actions.
	if strings.Contains(body, "catalog-edit") {
		t.Fatalf("expected the old per-row edit button to be gone; got:\n%s", body)
	}
	// SKU/Tax/Unit/Category are edit-dialog-only now — the card itself
	// shows name and price only.
	if strings.Contains(body, "S1</td>") {
		t.Fatalf("expected no SKU column in the card grid; got:\n%s", body)
	}
}

func TestCatalogPage_ItemDialogHasADeleteButtonHiddenByDefault(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// ut-docs#1956 (independent of #1951's own card-grid work, landed on
	// main while this branch was open) already added the dialog's own
	// icon-only delete button — id="item-form-delete" — covering the same
	// need #1951 had (a home for deactivate once the card grid dropped its
	// per-card actions), so #1951's rebase took that implementation rather
	// than shipping a second one.
	if !strings.Contains(body, `id="item-form-delete"`) {
		t.Fatalf("expected the item dialog to carry a delete button; got:\n%s", body)
	}
	// Hidden on page load (a fresh dialog starts in create mode, same as
	// item-form-reset) — JS's setMode() reveals it only when editing.
	if !strings.Contains(body, `id="item-form-delete" hidden`) {
		t.Fatalf("expected the delete button hidden by default; got:\n%s", body)
	}
}

func TestCatalogPage_EmptyStateUsesItsOwnKeyNotBasketEmpty(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No items yet") {
		t.Fatalf("expected the catalog's own empty-state copy; got:\n%s", body)
	}
}
