package catalog

import (
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

const variantInactiveMsg = "This variant is switched off. Tick Active for it and save, then add the barcode."

// ut-docs#4343 (a): the attach endpoint names the real reason.
func TestBarcodeAttach_InactiveVariantShowsSpecificMessage(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Shirt", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v-off", ItemID: "itm1", SKU: "S1-L", Name: "L", Price: 300, IsActive: false})

	rec := postForm(t, mux, "/api/catalog/barcode", "barcode=5004343&variantId=v-off&panelItem=itm1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, variantInactiveMsg) {
		t.Fatalf("want the variant-inactive message, got: %s", body)
	}
	if strings.Contains(body, "barcode attach failed") {
		t.Fatalf("must not show the generic message: %s", body)
	}
}

// ut-docs#4343 (b): no attach form for an inactive variant row.
func TestVariantsPanel_InactiveVariantHasNoBarcodeAttachForm(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Shirt", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v-off", ItemID: "itm1", SKU: "S1-L", Name: "L", Price: 300, IsActive: false})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v-on", ItemID: "itm1", SKU: "S1-M", Name: "M", Price: 300, IsActive: true})

	rec := get(t, mux, "/api/catalog/item-variants?item_id=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `name="variantId" value="v-off"`) {
		t.Fatal("inactive variant must not render a barcode attach form")
	}
	if !strings.Contains(body, `name="variantId" value="v-on"`) {
		t.Fatal("active variant must still render a barcode attach form")
	}
	if !strings.Contains(body, `data-testid="variant-barcode-inactive"`) || !strings.Contains(body, variantInactiveMsg) {
		t.Fatal("inactive variant row must say why in place of the form")
	}
}
