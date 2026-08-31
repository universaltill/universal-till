package catalog

// ut-docs#1363: every catalog mutation answers with row-level HTMX
// out-of-band fragments — an updated/inserted/deleted #catalog-row-<id>,
// plus the #catalog-empty-row placeholder when it needs to (dis)appear —
// NEVER the whole re-rendered items table. These tests pin that protocol
// endpoint by endpoint; the client side requests everything with
// swap:none, so the response body's OOB fragments ARE the UI update.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

// A mutation response must never carry the whole table again — that's the
// regression this card exists to remove (full-page re-render + lost scroll
// position after every single mutation).
func assertNoFullTable(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, `id="catalog-table"`) {
		t.Fatalf("mutation response still carries the whole items table:\n%s", body)
	}
}

func TestItemCreate_RespondsWithRowInsertOOB(t *testing.T) {
	mux, _ := newCatalogMux(t)

	rec := postForm(t, mux, "/api/catalog/item", "name=Fresh+Item&price=150&sku=F1&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("create: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertNoFullTable(t, body)
	// The new row rides in as a beforeend append into the tbody…
	if !strings.Contains(body, `hx-swap-oob="beforeend:#catalog-tbody"`) {
		t.Fatalf("create response missing the beforeend row insert:\n%s", body)
	}
	if !strings.Contains(body, "Fresh Item") {
		t.Fatalf("create response missing the new item's row content:\n%s", body)
	}
	// …and the empty-state placeholder is cleared — this WAS the first
	// item, so the placeholder is really in the DOM to clear.
	if !strings.Contains(body, `id="catalog-empty-row" hx-swap-oob="delete"`) {
		t.Fatalf("create response missing the empty-state delete fragment:\n%s", body)
	}
}

// An OOB delete whose target is missing IS a silent no-op in the vendored
// htmx 1.9.12 (verified: oobSwap's "no target" branch is unreachable for a
// valid selector — querySelectorAll never returns falsy — so an
// over-emitted delete costs nothing at runtime). The empty-state delete
// fragment still only rides along when the placeholder is provably in the
// DOM (the created item is the catalog's sole active item) because that's
// simply the correct condition, not to dodge an error: a create into a
// non-empty catalog has no placeholder to remove.
func TestItemCreate_IntoNonEmptyCatalogSkipsEmptyStateDelete(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Existing", BasePrice: 100, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/item", "name=Second+Item&price=150&sku=F2&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("create: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-swap-oob="beforeend:#catalog-tbody"`) {
		t.Fatalf("create response missing the beforeend row insert:\n%s", body)
	}
	if strings.Contains(body, `id="catalog-empty-row"`) {
		t.Fatalf("create into a non-empty catalog must not touch the (absent) empty-state placeholder:\n%s", body)
	}
}

// An item created INACTIVE was never in the table: no row to delete, no
// placeholder change — the response must carry no fragments at all (not
// because a delete for the never-rendered row would misbehave — it
// wouldn't, see above — simply because there is nothing to say).
func TestItemCreate_InactiveEmitsNothing(t *testing.T) {
	mux, _ := newCatalogMux(t)

	rec := postForm(t, mux, "/api/catalog/item", "name=Ghost&price=100&isActive=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("create: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "" {
		t.Fatalf("inactive create must emit no OOB fragments, got:\n%s", body)
	}
}

func TestItemUpdate_RespondsWithSingleRowOOB(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Old Name", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm2", SKU: "S2", Name: "Sibling Item", BasePrice: 200, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=New+Name&price=250&sku=S1&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("update: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertNoFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("update response missing the in-place row swap for itm1:\n%s", body)
	}
	if !strings.Contains(body, "New Name") {
		t.Fatalf("update response missing the updated row content:\n%s", body)
	}
	// Single-row scope: the sibling's row must NOT be re-rendered.
	if strings.Contains(body, "Sibling Item") || strings.Contains(body, "catalog-row-itm2") {
		t.Fatalf("update response touched a sibling row:\n%s", body)
	}
}

func TestItemUpdate_DeactivatingViaFormRemovesRow(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Going Away", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm2", SKU: "S2", Name: "Stays", BasePrice: 200, IsActive: true})

	// The edit form's Active checkbox unchecked: the item drops out of the
	// active list, so its row must be deleted, not re-rendered.
	rec := postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=Going+Away&price=100&sku=S1&isActive=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("update: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertNoFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1" hx-swap-oob="delete"`) {
		t.Fatalf("expected a row delete fragment for the deactivated item:\n%s", body)
	}
}

// An update that REACTIVATES an inactive item must answer with an INSERT
// fragment, not an in-place update: the item was inactive, so its row is
// not in the table, and an hx-swap-oob="true" with no matching id is a
// console-erroring no-op (the row would simply never appear). The
// operator can reach this by deactivating a row while the edit form still
// holds that item, then saving. Previous active state decides the mode.
func TestItemUpdate_ReactivatingInsertsRow(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Comeback", BasePrice: 100, IsActive: false})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm2", SKU: "S2", Name: "Bystander", BasePrice: 200, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=Comeback&price=100&sku=S1&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("update: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertNoFullTable(t, body)
	if !strings.Contains(body, `hx-swap-oob="beforeend:#catalog-tbody"`) {
		t.Fatalf("reactivation must append the row (it isn't in the DOM to update):\n%s", body)
	}
	if strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("reactivation must not emit an in-place update for a missing row:\n%s", body)
	}
	// Another active item exists, so the (absent) placeholder is untouched.
	if strings.Contains(body, `id="catalog-empty-row"`) {
		t.Fatalf("placeholder fragment emitted while other items are visible:\n%s", body)
	}
}

func TestItemDeactivate_RemovesRowWithoutTouchingSiblings(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Doomed", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm2", SKU: "S2", Name: "Survivor", BasePrice: 200, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/item/deactivate", "id=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("deactivate: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertNoFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1" hx-swap-oob="delete"`) {
		t.Fatalf("expected a row delete fragment:\n%s", body)
	}
	if strings.Contains(body, "Survivor") || strings.Contains(body, "catalog-row-itm2") {
		t.Fatalf("deactivate response touched a sibling row:\n%s", body)
	}
	// Items remain, so the empty-state placeholder must NOT come back.
	if strings.Contains(body, `hx-swap-oob="beforeend:#catalog-tbody"`) {
		t.Fatalf("empty-state placeholder appended while items remain:\n%s", body)
	}
}

func TestItemDeactivate_LastItemBringsBackEmptyState(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Last One", BasePrice: 100, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/item/deactivate", "id=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("deactivate: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertNoFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1" hx-swap-oob="delete"`) {
		t.Fatalf("expected a row delete fragment:\n%s", body)
	}
	// The placeholder rides back in as a beforeend append (the swap
	// directive sits on the wrapper tbody — htmx inserts its CHILDREN, so
	// the row itself stays plain) with the same markup the table's own
	// else-branch renders.
	if !strings.Contains(body, `hx-swap-oob="beforeend:#catalog-tbody"`) {
		t.Fatalf("expected a beforeend append fragment:\n%s", body)
	}
	if !strings.Contains(body, `id="catalog-empty-row"`) {
		t.Fatalf("expected the empty-state placeholder row:\n%s", body)
	}
	if !strings.Contains(body, `class="empty"`) {
		t.Fatalf("expected the placeholder's empty-cell markup:\n%s", body)
	}
}

func TestBarcodeAttach_NonPanelRespondsWithRowOOB(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})

	// No panelItem (the keypad-mapper path): the response is ONLY the row
	// fragment, showing the freshly attached code in the summary line.
	rec := postForm(t, mux, "/api/catalog/barcode", "barcode=5000001&itemId=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("attach: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertNoFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("expected the in-place row swap:\n%s", body)
	}
	if !strings.Contains(body, "5000001") {
		t.Fatalf("expected the new barcode in the row summary:\n%s", body)
	}
}

func TestBarcodeAttach_NonPanelVariantResolvesParentItemRow(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})

	// variantId wins over itemId (existing behavior) — the row that must
	// refresh is still the parent item's, its variants summary now shows
	// the code.
	rec := postForm(t, mux, "/api/catalog/barcode", "barcode=5000330&itemId=itm1&variantId=v1")
	if rec.Code != http.StatusOK {
		t.Fatalf("attach: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertNoFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1"`) {
		t.Fatalf("expected the parent item's row fragment:\n%s", body)
	}
	if !strings.Contains(body, "[5000330]") {
		t.Fatalf("expected the variant summary to carry the new barcode:\n%s", body)
	}
}

func TestVariantDeactivate_NonPanelResolvesParentItemRow(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})

	// The non-panel form carries ONLY the variant id — the handler resolves
	// the parent item itself so the row's variants summary drops "Large".
	rec := postForm(t, mux, "/api/catalog/variant/deactivate", "id=v1")
	if rec.Code != http.StatusOK {
		t.Fatalf("deactivate: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertNoFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("expected the parent item's in-place row swap:\n%s", body)
	}
	if strings.Contains(body, "Large") {
		t.Fatalf("deactivated variant must no longer appear in the row summary:\n%s", body)
	}
}

func TestBarcodeDelete_NonPanelResolvesOwningItemRow(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedBarcode(t, db, "5000001", "itm1", true)

	rec := postForm(t, mux, "/api/catalog/barcode/delete", "barcode=5000001")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertNoFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("expected the owning item's in-place row swap:\n%s", body)
	}
	if strings.Contains(body, "5000001") {
		t.Fatalf("deleted barcode must no longer appear in the row summary:\n%s", body)
	}
}

func TestItemImageUpload_RespondsWithRowOOB(t *testing.T) {
	mux, db := imageUploadTestDeps(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Latte", BasePrice: 250, IsActive: true})

	body, ct := multipartUpload(t, map[string]string{"item_id": "itm1"}, "photo.png", validPNG(t))
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item/image", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	assertNoFullTable(t, got)
	if !strings.Contains(got, `id="catalog-row-itm1"`) || !strings.Contains(got, `hx-swap-oob="true"`) {
		t.Fatalf("expected the in-place row swap after an image upload:\n%s", got)
	}
	// The row now resolves the freshly written thumb.
	if !strings.Contains(got, "/public/assets/items/itm1/thumb.png") {
		t.Fatalf("expected the row to reference the uploaded thumbnail:\n%s", got)
	}
}
