package catalog

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// uuidLike matches the shape of the real tax_code_id values this bug was
// filed against (e.g. 4ca66fd2-8379-4f6b-90a7-63c959d0e44b) — a regression
// test for "never render the raw id" has to catch UUID-shaped ids, not just
// this package's own short test fixture ids ("tax_std").
var uuidLike = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// ut-docs#1178: the catalog list's TAX column and the item-edit "Tax code"
// field both used to render the raw tax_code_id (a UUID in production,
// looking like an internal error to a shop owner checking their VAT setup)
// instead of the tax code's name. Both surfaces must show the name; neither
// may ever emit anything UUID-shaped.
func TestCatalogPage_TaxCodeShowsNameNotID(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedTaxCode(t, db, "4ca66fd2-8379-4f6b-90a7-63c959d0e44b", "Standard 19%", 1900)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{
		ID: "itm1", SKU: "COLA", Name: "Cola", BasePrice: 250,
		TaxCodeID: "4ca66fd2-8379-4f6b-90a7-63c959d0e44b", IsActive: true,
	})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{
		ID: "itm2", SKU: "WATER", Name: "Still Water", BasePrice: 150, IsActive: true,
	}) // no tax code at all

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, "Standard 19%") {
		t.Fatalf("expected tax code name %q to render somewhere on the page; got:\n%s", "Standard 19%", body)
	}
	if uuidLike.MatchString(stripDataAttrs(body)) {
		t.Fatalf("catalog page rendered a UUID-shaped string outside data-* attributes — the raw tax_code_id leaked into visible content:\n%s", body)
	}

	// The item-edit "Tax code" control must be a <select> populated from
	// the seeded tax code, not a free-text/datalist input.
	if !strings.Contains(body, `<select name="taxCode"`) {
		t.Fatalf("expected the item-edit Tax code field to be a <select name=\"taxCode\">; got:\n%s", body)
	}
	if !strings.Contains(body, `<option value="4ca66fd2-8379-4f6b-90a7-63c959d0e44b">Standard 19%</option>`) {
		t.Fatalf("expected the tax-code <select> to offer the seeded tax code by name; got:\n%s", body)
	}

	// An item with no tax code at all shows a placeholder, never a blank
	// cell that could be mistaken for missing data.
	if !strings.Contains(body, ">—<") {
		t.Fatalf("expected a placeholder for the item with no tax code; got:\n%s", body)
	}
}

// A mutation also answers with just the affected item's row, as an HTMX
// out-of-band fragment (ut-docs#1363, writeCatalogRowOOB) — that path must
// carry the same fix, not just the full /catalog page load.
func TestCatalogTablePartial_TaxCodeShowsNameNotID(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedTaxCode(t, db, "4ca66fd2-8379-4f6b-90a7-63c959d0e44b", "Standard 19%", 1900)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "TEA", Name: "Tea", BasePrice: 200, TaxCodeID: "4ca66fd2-8379-4f6b-90a7-63c959d0e44b", IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	// /api/catalog/item/update answers with just this item's row as an OOB
	// fragment (writeCatalogRowOOB), independently of the full /catalog
	// page load above — that render path needs its own coverage.
	rec := postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=Tea&price=200&taxCode=4ca66fd2-8379-4f6b-90a7-63c959d0e44b")
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Standard 19%") {
		t.Fatalf("expected tax code name %q in the re-rendered row fragment; got:\n%s", "Standard 19%", body)
	}
	if uuidLike.MatchString(stripDataAttrs(body)) {
		t.Fatalf("re-rendered table partial leaked the raw tax_code_id outside data-* attributes:\n%s", body)
	}
}

// stripDataAttrs removes HTML attribute values (data-tax="...", id="...",
// value="...", etc.) so the UUID check below only inspects rendered visible
// content, not the ids HTMX/JS legitimately need in markup attributes.
func stripDataAttrs(html string) string {
	attr := regexp.MustCompile(`(?:data-[a-z-]+|id|value)="[^"]*"`)
	return attr.ReplaceAllString(html, "")
}

// ut-docs#1178 review finding F1: converting the item-edit Tax code field
// from a free-text/datalist input to a <select> introduced a real
// regression — per the HTML spec, setting a <select>'s value to a string
// matching no <option> leaves nothing selected, and an unselected <select>
// contributes NOTHING to the submitted form. A text input has no such
// failure mode; it just holds whatever string it's given. So if the
// dropdown only ever offered ACTIVE tax codes, saving any other field on an
// item still assigned a since-deactivated code would silently null out its
// tax_code_id on the very next save — a shop owner renaming an item would
// unknowingly wipe its VAT assignment, no warning, HTTP 200. This test
// guards the fix: a deactivated tax code must still appear as a selectable
// (labeled) option, and must survive a save untouched.
func TestCatalogPage_InactiveTaxCodeSurvivesUnrelatedSave(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedTaxCode(t, db, "retired-1", "Old Reduced Rate", 500)
	if _, err := db.Exec(`UPDATE tax_codes SET is_active = 0 WHERE id = 'retired-1'`); err != nil {
		t.Fatalf("deactivate seeded tax code: %v", err)
	}
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "PIE", Name: "Pie", BasePrice: 300, TaxCodeID: "retired-1", IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	// The full /catalog page's item-edit <select> must still offer the
	// item's own (now-inactive) tax code — otherwise the browser can never
	// submit it back, regardless of what the server does with the value.
	page := get(t, mux, "/catalog")
	if page.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d, want 200: %s", page.Code, page.Body.String())
	}
	if !strings.Contains(page.Body.String(), `<option value="retired-1">Old Reduced Rate`) {
		t.Fatalf("expected the inactive tax code to still be a selectable option; got:\n%s", page.Body.String())
	}

	// Renaming the item (a save that legitimately re-submits taxCode, exactly
	// as the browser's populated <select> would) must NOT drop the tax code.
	rec := postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=Renamed+Pie&price=300&taxCode=retired-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var gotTaxCodeID *string
	if err := db.QueryRow(`SELECT tax_code_id FROM items WHERE id = 'itm1'`).Scan(&gotTaxCodeID); err != nil {
		t.Fatalf("query item after save: %v", err)
	}
	if gotTaxCodeID == nil || *gotTaxCodeID != "retired-1" {
		got := "nil"
		if gotTaxCodeID != nil {
			got = *gotTaxCodeID
		}
		t.Fatalf("tax_code_id after a save that re-submitted the inactive code: got %v, want \"retired-1\" — the item's VAT assignment was silently wiped", got)
	}

	// The re-rendered table partial must still resolve the retired code's
	// name too — a "—" here would be indistinguishable from "no tax code".
	if !strings.Contains(rec.Body.String(), "Old Reduced Rate") {
		t.Fatalf("expected the retired tax code's name in the re-rendered table; got:\n%s", rec.Body.String())
	}
}
