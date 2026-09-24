package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#2497 (kiosk half, review finding 1): the self-order kiosk grid
// (loadShopItems) had the SAME exposure as the cashier's
// ui.ButtonStore.LoadAllActive/SearchSellable -- a tile's Code preferred
// the item's raw barcode with no check that it decodes under the shop's
// currently-enabled barcode symbologies, and /api/self-order/scan resolves
// through the same POSRepo.ResolveShortcutLineDecoded chain. An item whose
// only barcode used a disabled symbology was listed on the kiosk grid but
// 404'd on tap.
//
// unresolvableKioskBarcode is structurally CODE128-shaped, not a valid
// EAN13 -- same shape internal/ui/buttons_barcode_symbology_test.go uses.
const unresolvableKioskBarcode = "PLU-CATALOG-9001"

func TestLoadShopItems_UnresolvableBarcodeSymbologyFallsBackToSKU(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)

	settingsRepo := data.NewSettingsRepo(d.DB)
	if err := settingsRepo.SetEnabledBarcodeSymbologies(context.Background(), []string{"EAN13"}); err != nil {
		t.Fatalf("SetEnabledBarcodeSymbologies: %v", err)
	}

	seedShopItem(t, d, "itm-kiosk-disabled-sym", "SKU-KIOSK-BAD-BARCODE", unresolvableKioskBarcode, "Odd Import", 199)

	items, err := loadShopItems(context.Background(), dp)
	if err != nil {
		t.Fatalf("loadShopItems: %v", err)
	}
	var got *shopItem
	for i := range items {
		if items[i].ItemID == "itm-kiosk-disabled-sym" {
			got = &items[i]
		}
	}
	if got == nil {
		t.Fatalf("expected a tile for itm-kiosk-disabled-sym, got: %+v", items)
	}
	if got.Code == unresolvableKioskBarcode {
		t.Fatalf("Code = %q, must NOT be the raw barcode (unresolvable under the shop's enabled symbologies)", got.Code)
	}
	if got.Code != "SKU-KIOSK-BAD-BARCODE" {
		t.Fatalf("Code = %q, want the SKU fallback %q (the kiosk has no synthesized item: tier)", got.Code, "SKU-KIOSK-BAD-BARCODE")
	}

	// Real end-to-end proof, not just the Code selection: post that exact
	// Code to the real /api/self-order/scan handler through the real mux,
	// same as a customer tapping the tile would, and confirm it actually
	// adds the item instead of 404ing.
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)
	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code="+got.Code))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	b := dp.KioskEngine.Basket()
	if len(b.Lines) != 1 || b.Lines[0].Name != "Odd Import" {
		t.Fatalf("expected Odd Import added to the kiosk basket, got %+v", b.Lines)
	}
}

// TestLoadShopItems_ResolvableBarcodeSymbologyUnchanged is the kiosk
// control: an item whose barcode IS in an enabled symbology must keep
// getting that real barcode as its tile Code, unchanged.
func TestLoadShopItems_ResolvableBarcodeSymbologyUnchanged(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)

	settingsRepo := data.NewSettingsRepo(d.DB)
	if err := settingsRepo.SetEnabledBarcodeSymbologies(context.Background(), []string{"EAN13"}); err != nil {
		t.Fatalf("SetEnabledBarcodeSymbologies: %v", err)
	}

	seedShopItem(t, d, "itm-kiosk-enabled-sym", "SKU-KIOSK-OK", "4006381333931", "Normal Kiosk Item", 399)

	items, err := loadShopItems(context.Background(), dp)
	if err != nil {
		t.Fatalf("loadShopItems: %v", err)
	}
	var got *shopItem
	for i := range items {
		if items[i].ItemID == "itm-kiosk-enabled-sym" {
			got = &items[i]
		}
	}
	if got == nil {
		t.Fatalf("expected a tile for itm-kiosk-enabled-sym, got: %+v", items)
	}
	if got.Code != "4006381333931" {
		t.Fatalf("Code = %q, want the real barcode %q unchanged (it resolves under the shop's enabled symbologies)", got.Code, "4006381333931")
	}
}
