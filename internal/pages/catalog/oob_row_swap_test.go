package catalog

import (
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#1363: catalog admin mutations used to answer every single-row
// change with the ENTIRE re-queried item/barcode/variant/tax-code table
// (renderCatalogTable). These tests pin the replacement protocol: each
// mutation answers with just the affected row as HTMX out-of-band swaps —
// replace-in-place (hx-swap-oob="true"), append (beforeend:#catalog-rows)
// for creation, delete for deactivation — plus the empty-state placeholder
// bookkeeping for the first/last item edge cases.

const (
	oobUpdateAttr = `hx-swap-oob="true"`
	// The beforeend attribute lives on a carrier <tbody>, never on the <tr>
	// itself: htmx 1.9.12's selector-style OOB swap inserts the oob
	// element's CHILDREN (a bare oob <tr> would append its loose <td>s),
	// and a response starting with <tbody> is the one table-fragment shape
	// its parser keeps intact alongside sibling fragments.
	oobInsertCarrier = `<tbody hx-swap-oob="beforeend:#catalog-rows">`
	oobDeleteAttr    = `hx-swap-oob="delete"`
)

// mustNotCarryFullTable asserts a mutation response no longer ships the
// whole re-rendered table — the entire point of ut-docs#1363.
func mustNotCarryFullTable(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, `id="catalog-table"`) {
		t.Fatalf("mutation response still carries the full catalog table:\n%s", body)
	}
}

func TestItemCreate_AnswersWithOOBRowInsert(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm0", SKU: "S0", Name: "Existing", BasePrice: 100, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/item", "name=Espresso&price=250&sku=ESP&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("create: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	mustNotCarryFullTable(t, body)
	if !strings.Contains(body, oobInsertCarrier) {
		t.Fatalf("create must answer with an OOB beforeend carrier tbody; got:\n%s", body)
	}
	if !strings.Contains(body, "Espresso") {
		t.Fatalf("inserted row must carry the new item; got:\n%s", body)
	}
	// The table was NOT empty before this create, so the response must not
	// carry an empty-state placeholder that doesn't belong in the DOM
	// anymore — an OOB swap against a missing id is a silent no-op in
	// htmx 1.9.12, but a stray placeholder id next to real rows is still
	// a real, visible DOM bug worth pinning here.
	if strings.Contains(body, `id="catalog-empty-row"`) {
		t.Fatalf("non-first create must not touch the empty-state placeholder; got:\n%s", body)
	}
}

func TestItemCreate_FirstItemRemovesEmptyPlaceholder(t *testing.T) {
	mux, _ := newCatalogMux(t)

	rec := postForm(t, mux, "/api/catalog/item", "name=First+Item&price=100&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("create: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	mustNotCarryFullTable(t, body)
	if !strings.Contains(body, oobInsertCarrier) {
		t.Fatalf("create must answer with an OOB row insert carrier; got:\n%s", body)
	}
	if !strings.Contains(body, `id="catalog-empty-row" `+oobDeleteAttr) {
		t.Fatalf("first create must OOB-delete the empty-state placeholder row; got:\n%s", body)
	}
}

func TestItemUpdate_AnswersWithOOBRowReplace(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard 19%", 1900)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, TaxCodeID: "tax_std", IsActive: true})
	testsupport.SeedBarcode(t, db, "5000001", "itm1", true)
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})
	testsupport.SeedVariantBarcode(t, db, "5000330", "v1", true)

	rec := postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=Coffee+Beans&price=320&taxCode=tax_std&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("update: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	mustNotCarryFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1" `+oobUpdateAttr) {
		t.Fatalf("update must answer with an OOB replace of the item's own row; got:\n%s", body)
	}
	// The row must preserve exactly what the full-table render showed:
	// name, barcode summary, variant summary (with the variant's own
	// barcode), and the tax code's display NAME (ut-docs#1178).
	for _, want := range []string{"Coffee Beans", "5000001", "Large", "[5000330]", "Standard 19%"} {
		if !strings.Contains(body, want) {
			t.Fatalf("row missing %q — barcode/variant/tax display must survive the row-scoped render; got:\n%s", want, body)
		}
	}
}

func TestItemDeactivate_AnswersWithOOBRowDelete(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm2", SKU: "S2", Name: "Tea", BasePrice: 200, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/item/deactivate", "id=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("deactivate: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	mustNotCarryFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1" `+oobDeleteAttr) {
		t.Fatalf("deactivate must answer with an OOB delete of the item's row; got:\n%s", body)
	}
	// Other items remain — no empty-state placeholder must be appended.
	if strings.Contains(body, `id="catalog-empty-row"`) {
		t.Fatalf("deactivate with items remaining must not append the empty placeholder; got:\n%s", body)
	}
}

func TestItemDeactivate_LastItemRestoresEmptyPlaceholder(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/item/deactivate", "id=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("deactivate: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	mustNotCarryFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1" `+oobDeleteAttr) {
		t.Fatalf("deactivate must OOB-delete the row; got:\n%s", body)
	}
	if !strings.Contains(body, oobInsertCarrier) || !strings.Contains(body, `id="catalog-empty-row"`) {
		t.Fatalf("deactivating the last item must OOB-append the empty-state placeholder (carrier tbody + placeholder row); got:\n%s", body)
	}
}

// Unchecking "Active" in the edit form is a deactivation ridden through the
// update endpoint — the row must disappear, same as the deactivate button.
func TestItemUpdate_SetInactiveRemovesRow(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=Coffee&price=300&isActive=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("update: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	mustNotCarryFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1" `+oobDeleteAttr) {
		t.Fatalf("update-to-inactive must answer with an OOB row delete; got:\n%s", body)
	}
	if !strings.Contains(body, oobInsertCarrier) || !strings.Contains(body, `id="catalog-empty-row"`) {
		t.Fatalf("last item going inactive must restore the empty placeholder; got:\n%s", body)
	}
}

// The non-panel fallbacks (no panelItem): barcode attach/delete and variant
// deactivate must answer row-scoped too, resolving the owning item
// themselves.
func TestBarcodeAttach_NoPanel_AnswersWithOwningItemRow(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/barcode", "barcode=5000001&itemId=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("attach: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	mustNotCarryFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1" `+oobUpdateAttr) {
		t.Fatalf("attach must answer with the owning item's row OOB; got:\n%s", body)
	}
	if !strings.Contains(body, "5000001") {
		t.Fatalf("row must show the freshly attached barcode; got:\n%s", body)
	}
}

func TestBarcodeAttach_NoPanel_VariantBarcodeResolvesParentItemRow(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/barcode", "barcode=5000330&variantId=v1")
	if rec.Code != http.StatusOK {
		t.Fatalf("attach: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	mustNotCarryFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1" `+oobUpdateAttr) {
		t.Fatalf("variant barcode attach must resolve and answer with the PARENT item's row; got:\n%s", body)
	}
	if !strings.Contains(body, "[5000330]") {
		t.Fatalf("row's variant summary must show the new variant barcode; got:\n%s", body)
	}
}

func TestBarcodeDelete_NoPanel_AnswersWithOwningItemRow(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedBarcode(t, db, "5000001", "itm1", true)

	rec := postForm(t, mux, "/api/catalog/barcode/delete", "barcode=5000001")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	mustNotCarryFullTable(t, body)
	// The owner has to be resolved BEFORE the row is gone from the barcode
	// tables — after deletion nothing links the code to itm1 any more.
	if !strings.Contains(body, `id="catalog-row-itm1" `+oobUpdateAttr) {
		t.Fatalf("barcode delete must answer with the (former) owning item's row; got:\n%s", body)
	}
	if strings.Contains(body, "5000001") {
		t.Fatalf("re-rendered row must no longer show the deleted barcode; got:\n%s", body)
	}
}

func TestVariantDeactivate_NoPanel_AnswersWithParentItemRow(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/variant/deactivate", "id=v1")
	if rec.Code != http.StatusOK {
		t.Fatalf("deactivate: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	mustNotCarryFullTable(t, body)
	if !strings.Contains(body, `id="catalog-row-itm1" `+oobUpdateAttr) {
		t.Fatalf("variant deactivate must answer with the parent item's row; got:\n%s", body)
	}
	if strings.Contains(body, "Large") {
		// The variant summary lists active variants only — same as the
		// full-table render always did.
		t.Fatalf("deactivated variant must no longer appear in the row's variant summary; got:\n%s", body)
	}
}

// Panel-scoped mutations (panelItem set) keep answering with the panel, but
// the ride-along OOB is now the affected ROW, not the whole table.
func TestVariantsPanelMutation_CarriesRowOOBNotTable(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/barcode", "barcode=5000001&itemId=itm1&panelItem=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("panel attach: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	mustNotCarryFullTable(t, body)
	if !strings.Contains(body, `id="catalog-variants"`) {
		t.Fatalf("panel mutation must still answer with the panel; got:\n%s", body)
	}
	if !strings.Contains(body, `id="catalog-row-itm1" `+oobUpdateAttr) {
		t.Fatalf("panel mutation must carry the item's row as the OOB ride-along; got:\n%s", body)
	}
	// htmx 1.9.12 drops a bare <tr> when the response's first element is a
	// <div> (DOMParser discards table elements outside a table), so the OOB
	// row must ride INSIDE a carrier <table> nested in the panel markup.
	rowIdx := strings.Index(body, `id="catalog-row-itm1"`)
	panelEnd := strings.LastIndex(body, "</div>")
	if rowIdx > panelEnd {
		t.Fatalf("OOB row must be nested inside the panel (carrier table), not appended after it — a bare top-level <tr> after a <div> is dropped by htmx's fragment parser")
	}
}

// The full /catalog page still renders the whole table — that's the one
// place the unbounded queries belong — and must now carry the row ids and
// tbody anchor the OOB protocol targets.
func TestCatalogPage_TableCarriesRowAnchors(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})

	rec := get(t, mux, "/catalog")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`id="catalog-rows"`, `id="catalog-row-itm1"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("full page table missing %q; got:\n%s", want, body)
		}
	}
}

func TestCatalogPage_EmptyTableCarriesPlaceholderRow(t *testing.T) {
	mux, _ := newCatalogMux(t)

	rec := get(t, mux, "/catalog")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `id="catalog-empty-row"`) {
		t.Fatalf("empty catalog must render the addressable empty-state placeholder row; got:\n%s", rec.Body.String())
	}
}
