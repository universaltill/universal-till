package catalog

import (
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#2815: editing an EXISTING variant's price in the item editor's
// Variants tab and pressing Save "went back to the original price". The
// root cause was client-side (catalog.html's htmx:configRequest listener
// read `f.id`, which the form's own <input name="id"> shadows, so it never
// found the visible price input and the stale hidden `price` went on the
// wire — driven for real in e2e/tests/variant-price-save-2815.spec.ts).
// These pin the server half of the fix: the save is visibly confirmed, a
// value that is not an amount is refused with a localized message (never a
// silent revert or a silent 0), and the change reaches replicas via the
// admin-sync generation counter (ADR-0114's link nudge follows from it).

func seedAmericano(t *testing.T) (*http.ServeMux, func() int64, func() int64) {
	t.Helper()
	// The REAL migrated schema: sync_admin_version and its item_variants
	// triggers (migration 023) only exist there, not in the hand-rolled
	// testsupport.NewCatalogTestDB schema.
	mux, d := newCatalogMuxRealSession(t)
	db := d.Db
	testsupport.SeedTaxCode(t, db, "tax_2815", "Standard", 1900)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "AM", Name: "Americano", BasePrice: 300, TaxCodeID: "tax_2815", IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "AM-L", Name: "Large", Price: 300, IsActive: true})
	price := func() int64 {
		var p int64
		if err := db.QueryRow(`SELECT price FROM item_variants WHERE id = 'v1'`).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	gen := func() int64 {
		var g int64
		if err := db.QueryRow(`SELECT generation FROM sync_admin_version WHERE id = 1`).Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	return mux, price, gen
}

// The exact body the fixed Variants-tab row posts for "3,50" (de) or
// "3.50" (en): the client converts the typed major amount to minor units.
const americanoRowSave = "panelItem=itm1&id=v1&itemId=itm1&isActive=0&isActive=1&price=350&name=Large&sku=AM-L"

func TestVariantPriceSave_2815_PersistsConfirmsAndBumpsSync(t *testing.T) {
	for _, lang := range []string{"en", "de"} {
		t.Run(lang, func(t *testing.T) {
			mux, price, gen := seedAmericano(t)
			before := gen()
			rec := postForm(t, mux, "/api/catalog/variant?lang="+lang, americanoRowSave)
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if got := price(); got != 350 {
				t.Fatalf("variant price not stored: got %d, want 350", got)
			}
			// Visible confirmation inside the re-rendered panel (the htmx
			// target), not a silent swap that looks like nothing happened.
			if !strings.Contains(rec.Body.String(), `data-testid="variant-saved"`) {
				t.Fatalf("re-rendered panel carries no saved confirmation:\n%s", rec.Body.String())
			}
			if after := gen(); after <= before {
				t.Fatalf("admin sync generation did not move (%d -> %d): replicas would never see the new price", before, after)
			}
		})
	}
}

// A price that is not a minor-unit integer (e.g. a raw "3,50" if the client
// conversion is ever bypassed again) is refused with the localized message,
// and the stored price is untouched.
func TestVariantPriceSave_2815_UnparseablePriceIsLocalized400(t *testing.T) {
	mux, price, _ := seedAmericano(t)
	rec := postForm(t, mux, "/api/catalog/variant", strings.Replace(americanoRowSave, "price=350", "price=3%2C50", 1))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid request") {
		t.Fatalf("want the localized catalog.error.invalid_request message, got %q", rec.Body.String())
	}
	if got := price(); got != 300 {
		t.Fatalf("price must be untouched on a refused save, got %d", got)
	}
}
