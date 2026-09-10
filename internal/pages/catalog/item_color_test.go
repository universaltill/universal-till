package catalog

// ut-docs#1901: end-to-end (HTTP handler + template) coverage for an
// item's tile color — creating/updating an item with a valid palette
// color succeeds and round-trips into the row-level OOB fragment's
// data-color attribute (catalog_row.html), which the row-click JS in
// catalog.html reads back to prefill the edit dialog's swatch picker.

import (
	"net/http"
	"strings"
	"testing"
)

func TestItemCreate_WithValidColor_Succeeds(t *testing.T) {
	mux, db := newCatalogMux(t)

	rec := postForm(t, mux, "/api/catalog/item", "name=Latte&price=320&color=%230f172a")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `data-color="#0f172a"`) {
		t.Fatalf("row fragment missing data-color: %s", rec.Body.String())
	}
	var color string
	if err := db.QueryRow(`SELECT color FROM items WHERE name = 'Latte'`).Scan(&color); err != nil {
		t.Fatalf("query color: %v", err)
	}
	if color != "#0f172a" {
		t.Fatalf("expected stored color #0f172a, got %q", color)
	}
}

// An item with no color set renders no data-color value and no color-dot
// in the list — never a literal "<nil>"/"null" artifact.
func TestItemCreate_WithoutColor_RowHasEmptyDataColor(t *testing.T) {
	mux, _ := newCatalogMux(t)

	rec := postForm(t, mux, "/api/catalog/item", "name=Plain&price=100")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `data-color=""`) {
		t.Fatalf("expected an empty data-color attribute, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `class="color-dot"`) {
		t.Fatalf("expected no color-dot for a colorless item: %s", rec.Body.String())
	}
}

// Updating an existing item's color must be reflected in the next
// row-level OOB fragment.
func TestItemUpdate_ChangesColor_ReflectedInRowOOB(t *testing.T) {
	mux, db := newCatalogMux(t)
	rec := postForm(t, mux, "/api/catalog/item", "name=Tea&price=150&color=%230f766e")
	if rec.Code != http.StatusOK {
		t.Fatalf("create: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM items WHERE name = 'Tea'`).Scan(&id); err != nil {
		t.Fatalf("query id: %v", err)
	}

	rec = postForm(t, mux, "/api/catalog/item/update", "id="+id+"&name=Tea&price=150&color=%237c3aed")
	if rec.Code != http.StatusOK {
		t.Fatalf("update: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `data-color="#7c3aed"`) {
		t.Fatalf("row fragment missing updated data-color: %s", rec.Body.String())
	}
}

// TestCatalogPage_ItemFormDialogClosedByDefault pins the full-screen
// <dialog> conversion (ut-docs#1901): the form must exist as a real
// <dialog id="item-form-modal"> (not the old 360px sticky side-panel
// <div>), and it must NOT carry the `open` attribute on first render — a
// dialog that opened itself on every /catalog page load would defeat the
// whole point (an operator just browsing the list would be interrupted by
// a full-screen form every time).
func TestCatalogPage_ItemFormDialogClosedByDefault(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := get(t, mux, "/catalog")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="item-form-modal"`) {
		t.Fatalf("expected the item-form dialog in the page:\n%s", body)
	}
	if !strings.Contains(body, `class="item-form-modal"`) {
		t.Fatalf("expected the item-form-modal class (near-full-screen sizing, NOT the 28rem .modifier-modal cap):\n%s", body)
	}
	// The dialog tag itself must not carry `open` — find the opening
	// <dialog id="item-form-modal" ...> tag and check it doesn't include
	// the boolean `open` attribute before its closing '>'.
	start := strings.Index(body, `<dialog id="item-form-modal"`)
	if start == -1 {
		t.Fatalf("expected a <dialog id=\"item-form-modal\"> tag:\n%s", body)
	}
	end := strings.Index(body[start:], ">")
	if end == -1 {
		t.Fatalf("unterminated dialog tag")
	}
	tag := body[start : start+end]
	if strings.Contains(tag, " open") || strings.HasSuffix(tag, "open") {
		t.Fatalf("item-form dialog must be closed by default (no `open` attribute), got tag: %s", tag)
	}
	// Fields that used to live in the sticky side panel must now be
	// reachable inside the dialog markup. (ut-docs#1956: the keypad mapper
	// is the dialog's Keypad tab panel now, no longer a <details
	// id="keypad-mapper">.)
	for _, want := range []string{`id="item-name"`, `id="item-price"`, `id="item-category"`, `id="item-color-grid"`, `id="builtin-icon-grid"`, `id="item-form-panel-keypad"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %s inside the converted dialog:\n%s", want, body)
		}
	}
	// A trigger to actually open the dialog for a NEW item must exist —
	// otherwise there is no way to reach the create-item form at all.
	if !strings.Contains(body, `id="item-form-add-btn"`) {
		t.Fatalf("expected an explicit add-item trigger button:\n%s", body)
	}
	// The old two-column, 360px-side-panel layout must be gone: the form
	// now lives INSIDE the dialog, not as a grid sibling of .catalog-list
	// under .catalog-layout — i.e. .catalog-layout's own closing tag comes
	// BEFORE the dialog opens, not after.
	layoutClose := strings.Index(body, `class="catalog-list"`)
	dialogOpen := strings.Index(body, `<dialog id="item-form-modal"`)
	if layoutClose == -1 || dialogOpen == -1 || dialogOpen < layoutClose {
		t.Fatalf("expected the item-form dialog to render after .catalog-list, not nested inside the old two-column layout:\n%s", body)
	}
}
