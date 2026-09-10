package catalog

// ut-docs#1956: the add/edit-item dialog is a genuinely full-screen form
// with a pinned action bar (Save / Close / + New / icon-only Delete), the
// three former <details> accordions (item image, print labels, keypad
// mapper) and the page-level variants panel all become tabs INSIDE the
// dialog. These pin the server-rendered shape of /catalog; the Playwright
// spec catalog-item-form-1956.spec.ts covers the behaviour a browser adds
// (scrolling, tab switching, the delete round-trip, viewport floors).

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func catalogPageBody(t *testing.T) string {
	t.Helper()
	mux, _ := newCatalogMux(t)
	rec := get(t, mux, "/catalog")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// The section between <dialog id="item-form-modal"> and its closing tag.
func itemFormDialog(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `id="item-form-modal"`)
	if start < 0 {
		t.Fatalf("/catalog has no #item-form-modal dialog")
	}
	end := strings.Index(body[start:], "</dialog>")
	if end < 0 {
		t.Fatalf("#item-form-modal never closes")
	}
	return body[start : start+end]
}

func TestItemForm_HasFiveTabs(t *testing.T) {
	dialog := itemFormDialog(t, catalogPageBody(t))
	if !strings.Contains(dialog, `role="tablist"`) {
		t.Fatalf("item form dialog has no role=tablist tab strip")
	}
	for _, tab := range []struct{ id, label string }{
		{"item-form-tab-details", "Details"},
		{"item-form-tab-variants", "Variants"},
		{"item-form-tab-image", "Item image"},
		{"item-form-tab-labels", "Print labels"},
		{"item-form-tab-keypad", "Map a physical keypad"},
	} {
		re := regexp.MustCompile(`<button[^>]*id="` + tab.id + `"[^>]*role="tab"[^>]*>\s*` + regexp.QuoteMeta(tab.label))
		if !re.MatchString(dialog) {
			t.Errorf("missing tab button #%s labelled %q inside the item form dialog", tab.id, tab.label)
		}
	}
}

func TestItemForm_NoAccordionsRemain(t *testing.T) {
	body := catalogPageBody(t)
	if strings.Contains(body, `class="catalog-extra"`) || strings.Contains(body, `<details class="catalog-extra"`) {
		t.Fatalf("/catalog still renders a <details class=\"catalog-extra\"> accordion — these became tabs (ut-docs#1956)")
	}
}

func TestItemForm_VariantsPanelLivesInsideDialog(t *testing.T) {
	body := catalogPageBody(t)
	dialog := itemFormDialog(t, body)
	if !strings.Contains(dialog, `id="catalog-variants"`) {
		t.Fatalf("#catalog-variants is not inside #item-form-modal")
	}
	if strings.Count(body, `id="catalog-variants"`) != 1 {
		t.Fatalf("#catalog-variants rendered %d times, want exactly one (inside the dialog)", strings.Count(body, `id="catalog-variants"`))
	}
}

func TestItemForm_DeleteIsIconOnlyAndHiddenInCreateMode(t *testing.T) {
	dialog := itemFormDialog(t, catalogPageBody(t))
	re := regexp.MustCompile(`<button[^>]*id="item-form-delete"[^>]*>`)
	tag := re.FindString(dialog)
	if tag == "" {
		t.Fatalf("no #item-form-delete button in the item form dialog")
	}
	if !strings.Contains(tag, `aria-label="`) {
		t.Errorf("#item-form-delete has no aria-label (icon-only control needs an accessible name): %s", tag)
	}
	if !regexp.MustCompile(`\shidden[\s>]`).MatchString(tag) {
		t.Errorf("#item-form-delete must be hidden in the server-rendered create-mode markup: %s", tag)
	}
	if !strings.Contains(tag, `hx-post="/api/catalog/item/deactivate"`) {
		t.Errorf("#item-form-delete must reuse the deactivate endpoint (soft delete, history preserved): %s", tag)
	}
	// Icon-only: the button's content is the drawn icon, no visible text.
	inner := regexp.MustCompile(`(?s)<button[^>]*id="item-form-delete"[^>]*>(.*?)</button>`).FindStringSubmatch(dialog)
	if len(inner) < 2 || !strings.Contains(inner[1], `data-icon="trash-2"`) {
		t.Errorf("#item-form-delete should render the shared trash icon, got: %q", inner)
	}
}

func TestItemForm_SubmitLivesInPinnedHead(t *testing.T) {
	dialog := itemFormDialog(t, catalogPageBody(t))
	head := strings.Index(dialog, `class="catalog-form-head"`)
	bodyIdx := strings.Index(dialog, `class="catalog-form-body"`)
	if head < 0 || bodyIdx < 0 || head > bodyIdx {
		t.Fatalf("dialog needs a .catalog-form-head followed by a .catalog-form-body (head=%d body=%d)", head, bodyIdx)
	}
	submits := regexp.MustCompile(`<button[^>]*id="item-form-submit"[^>]*>`).FindAllStringIndex(dialog, -1)
	if len(submits) != 1 {
		t.Fatalf("want exactly one #item-form-submit, got %d", len(submits))
	}
	tag := dialog[submits[0][0]:submits[0][1]]
	if !strings.Contains(tag, `form="item-form"`) {
		t.Errorf("#item-form-submit outside the form needs form=\"item-form\": %s", tag)
	}
	if submits[0][0] < head || submits[0][0] > bodyIdx {
		t.Errorf("#item-form-submit must sit inside .catalog-form-head, not the scrolling body")
	}
	msg := strings.Index(dialog, `id="item-form-msg"`)
	if msg < head || msg > bodyIdx {
		t.Errorf("#item-form-msg must sit inside .catalog-form-head so save feedback is visible at any scroll position")
	}
}

func TestCatalogRow_DeleteConfirmIsTranslated(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := postForm(t, mux, "/api/catalog/item", "name=Confirm+Probe&price=100&sku=CP1&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	body := get(t, mux, "/catalog").Body.String()
	if strings.Contains(body, `hx-confirm="Deactivate Confirm Probe?"`) {
		t.Fatalf("catalog_row.html still ships the hardcoded English confirm")
	}
	if !strings.Contains(body, `hx-confirm="Remove Confirm Probe from the catalog?`) {
		t.Fatalf("row delete confirm should render catalog.delete_confirm_named with the item name; body has: %s",
			regexp.MustCompile(`hx-confirm="[^"]*"`).FindString(body))
	}
}
