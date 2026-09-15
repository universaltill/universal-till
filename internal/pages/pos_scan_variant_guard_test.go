package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ut-docs#2227: the suggestion strip and manual code entry both post a
// PARENT code straight to /api/pos/scan, bypassing the tile's own
// modifier-picker gate (ut-docs#2209). An item with at least one sellable
// variant must never add at its own base price through this endpoint --
// it must redirect to the same picker the tile uses, via
// HX-Retarget/HX-Reswap/HX-Trigger-After-Swap (the requesting element
// targets #basket, not #modifier-modal).
func TestScanAPI_ParentCodeWithVariants_RedirectsToPickerInsteadOfAdding(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	registerPOSModifiersAPI(mux, dp)

	rec := posPostForm(mux, "/api/pos/scan", "code=COFFEE")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Retarget"); got != "#modifier-modal" {
		t.Fatalf("HX-Retarget = %q, want #modifier-modal", got)
	}
	if got := rec.Header().Get("HX-Reswap"); got != "innerHTML" {
		t.Fatalf("HX-Reswap = %q, want innerHTML", got)
	}
	if got := rec.Header().Get("HX-Trigger-After-Swap"); got != "open-modifier-modal" {
		t.Fatalf("HX-Trigger-After-Swap = %q, want open-modifier-modal", got)
	}
	body := rec.Body.String()
	for _, want := range []string{"Small", "Regular", "Large", `name="variantId"`} {
		if !strings.Contains(body, want) {
			t.Errorf("picker missing %q: %s", want, body)
		}
	}
	if len(dp.Engine.Basket().Lines) != 0 {
		t.Fatal("a parent code with sellable variants must not add a line before the variant is chosen")
	}
}

// TestScanAPI_ParentCodeWithVariants_ShowsCurrentPriceHistoryPriceNotConfiguredPrice
// is ut-docs#2228, independent review blocker 1: this /api/pos/scan guard
// renders the SAME picker markup GET /ui/pos/modifiers does (the
// suggestion-strip/manual-code-entry route into the picker, ut-docs#2227),
// so it must show the same price_history-resolved price, not the variant's
// stale configured price.
func TestScanAPI_ParentCodeWithVariants_ShowsCurrentPriceHistoryPriceNotConfiguredPrice(t *testing.T) {
	dp, d := setupVariantModifiersTestDeps(t)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	execAll(t, d, []string{
		`INSERT INTO price_history(id, variant_id, price, starts_at) VALUES('ph1','v-reg',230,'` + past + `')`,
	})
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	registerPOSModifiersAPI(mux, dp)

	rec := posPostForm(mux, "/api/pos/scan", "code=COFFEE")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "£2.30") {
		t.Errorf("scan-guard picker must show the active price_history price £2.30 for Regular: %s", body)
	}
	if strings.Contains(body, "£3.10") {
		t.Errorf("scan-guard picker must NOT show Regular's configured (stale) price £3.10 once a price_history row overrides it: %s", body)
	}
}

// F2 (ut-docs#2227 review): the manual code-entry row submits qty, and
// modifier_picker.html renders no qty field at all -- so a qty=3 parent-code
// entry silently added 1 on submit. The redirected picker must carry the
// submitted qty through as a hidden field.
func TestScanAPI_ParentCodeWithVariants_CarriesQtyIntoPicker(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	registerPOSModifiersAPI(mux, dp)

	rec := posPostForm(mux, "/api/pos/scan", "code=COFFEE&qty=3")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// review finding, BLOCKER 2: a bare `name="qty" value="3"` substring
	// check is a false pass -- basket.html's OWN line qty control (rendered
	// when the guard does NOT fire and the item is added directly instead)
	// matches the identical substring. Anchor to the redirect actually
	// having happened, same as the sibling test above, before trusting the
	// qty assertion at all.
	if got := rec.Header().Get("HX-Retarget"); got != "#modifier-modal" {
		t.Fatalf("HX-Retarget = %q, want #modifier-modal -- response did not redirect to the picker at all", got)
	}
	if !strings.Contains(rec.Body.String(), `name="qty" value="3"`) {
		t.Fatalf("expected qty 3 carried into the picker as a hidden field, got: %s", rec.Body.String())
	}
}

// ut-docs#744, explicit non-regression: a scanned VARIANT's own barcode
// must still add directly with no prompt -- this guard only intercepts a
// PARENT code (VariantID == "").
func TestScanAPI_VariantBarcodeStillAddsDirectlyNoPrompt(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	registerPOSModifiersAPI(mux, dp)

	rec := posPostForm(mux, "/api/pos/scan", "code=C-S")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Retarget"); got != "" {
		t.Fatalf("a variant barcode scan must not redirect to the picker, got HX-Retarget=%q", got)
	}
	b := dp.Engine.Basket()
	if len(b.Lines) != 1 || b.Lines[0].VariantID != "v-small" {
		t.Fatalf("want 1 line carrying v-small, got %+v", b.Lines)
	}
}

// ut-docs#2227 review, BLOCKER 1: a weight/price-embedded scale label
// (QtyFromCode) resolving to a PARENT item with sellable variants must be
// added directly, exactly as before this fix -- redirecting it into the
// picker would silently replace its decoded weight/price with the
// submitted form qty on submit (reproduced against an unguarded build: a
// 1.234kg label re-priced as a plain qty=1 once routed through the
// picker's variant resolution, which resolves the chosen variant's own
// plain code and carries no decoded qty at all).
func TestScanAPI_ScaleLabelWithVariants_AddsDirectlyUnaffected(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	registerPOSModifiersAPI(mux, dp)

	rec := posPostForm(mux, "/api/pos/scan", "code=COFFEE-SCALE")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Retarget"); got != "" {
		t.Fatalf("a scale-label scan must not redirect to the picker, got HX-Retarget=%q", got)
	}
	b := dp.Engine.Basket()
	if len(b.Lines) != 1 || b.Lines[0].ItemID != "itm-coffee" || b.Lines[0].Qty != 1.234 {
		t.Fatalf("want 1 line for itm-coffee carrying the decoded qty 1.234, got %+v", b.Lines)
	}
}

// Acceptance criterion: an item with no sellable variant is unaffected.
func TestScanAPI_ItemWithNoVariants_AddsDirectly(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	registerPOSModifiersAPI(mux, dp)

	rec := posPostForm(mux, "/api/pos/scan", "code=WATER")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Retarget"); got != "" {
		t.Fatalf("an item with no sellable variant must not redirect, got HX-Retarget=%q", got)
	}
	b := dp.Engine.Basket()
	if len(b.Lines) != 1 || b.Lines[0].ItemID != "itm-water" {
		t.Fatalf("want 1 line for itm-water, got %+v", b.Lines)
	}
}

// F1 (ut-docs#2227 review): the guard must fire even when the code is
// already in the scan cache / already a basket line -- exactly the "held
// sale resumed from before the fix" scenario. A guard placed only at the
// later "fast path: resolve item" branch would be bypassed here.
func TestScanAPI_ParentCodeAlreadyInBasket_StillRedirectsOnRescan(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	registerPOSModifiersAPI(mux, dp)

	// Seed a pre-existing parent-priced line directly on the engine --
	// simulating a basket resumed from before this fix shipped, which
	// HasLine(code)/HasScanCache(code) would otherwise treat as a fast-path
	// hit and re-add without ever consulting the new guard.
	if _, err := dp.Engine.Scan("COFFEE"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	rec := posPostForm(mux, "/api/pos/scan", "code=COFFEE")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Retarget"); got != "#modifier-modal" {
		t.Fatalf("a re-scan of an already-basketed parent code must still redirect to the picker, got HX-Retarget=%q", got)
	}
	if len(dp.Engine.Basket().Lines) != 1 {
		t.Fatalf("the re-scan must not add a second parent-priced line, got %d lines", len(dp.Engine.Basket().Lines))
	}
}

// The kiosk's /api/self-order/scan is anonymous and auth-exempt -- the
// shipped grid never reaches it with a variant-bearing parent code
// (self_order_grid.html's own HasVariants check), but a crafted POST still
// can. Unlike the cashier path there is no picker to redirect to here, so
// this refuses the add rather than prompting.
func TestSelfOrderScanAPI_ParentCodeWithVariants_RefusesRatherThanAdds(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=COFFEE"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (cart re-render with a toast), got %d: %s", rec.Code, rec.Body.String())
	}
	if len(dp.KioskEngine.Basket().Lines) != 0 {
		t.Fatal("a crafted parent-code POST must not add a line at the parent's base price")
	}
}

// ut-docs#2244: once the guard has checked itm-water this session and found
// zero sellable variants, a repeat scan of the same code must not repeat the
// ItemVariantsFor DB round trip. Proven without a query spy: drop the table
// between the two scans, so the second scan would fail closed (toast, no
// line added) if it still queried it -- it must instead still add the item,
// which only happens if the guard was skipped via the memoized result.
func TestScanAPI_RepeatScanOfNoVariantItem_SkipsRedundantVariantsQuery(t *testing.T) {
	dp, d := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	registerPOSModifiersAPI(mux, dp)

	rec := posPostForm(mux, "/api/pos/scan", "code=WATER")
	if rec.Code != http.StatusOK {
		t.Fatalf("first scan: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(dp.Engine.Basket().Lines) != 1 {
		t.Fatalf("first scan: want 1 line, got %d", len(dp.Engine.Basket().Lines))
	}

	if _, err := d.DB.Exec(`DROP TABLE item_variants`); err != nil {
		t.Fatalf("drop item_variants: %v", err)
	}

	rec = posPostForm(mux, "/api/pos/scan", "code=WATER")
	if rec.Code != http.StatusOK {
		t.Fatalf("second scan: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	b := dp.Engine.Basket()
	if len(b.Lines) != 1 || b.Lines[0].Qty != 2 {
		t.Fatalf("second scan must have merged into the existing line via the scanCache fast path (qty 2), got %+v -- a fail-closed toast here means the guard still queried the (now-dropped) item_variants table instead of trusting the memoized result", b.Lines)
	}
}

// Same fix, same proof, on the kiosk's /api/self-order/scan.
func TestSelfOrderScanAPI_RepeatScanOfNoVariantItem_SkipsRedundantVariantsQuery(t *testing.T) {
	dp, d := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=WATER"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("first scan: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(dp.KioskEngine.Basket().Lines) != 1 {
		t.Fatalf("first scan: want 1 line, got %d", len(dp.KioskEngine.Basket().Lines))
	}

	if _, err := d.DB.Exec(`DROP TABLE item_variants`); err != nil {
		t.Fatalf("drop item_variants: %v", err)
	}

	rec := post()
	if rec.Code != http.StatusOK {
		t.Fatalf("second scan: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	b := dp.KioskEngine.Basket()
	if len(b.Lines) != 1 || b.Lines[0].Qty != 2 {
		t.Fatalf("second scan must have merged via the scanCache fast path (qty 2), got %+v -- a stuck-at-1 / error result means the guard still queried the dropped table", b.Lines)
	}
}

// Same non-regression as the cashier path: a scanned variant barcode is
// unaffected on the kiosk.
func TestSelfOrderScanAPI_VariantBarcodeStillAddsDirectly(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=C-S"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	b := dp.KioskEngine.Basket()
	if len(b.Lines) != 1 || b.Lines[0].VariantID != "v-small" {
		t.Fatalf("want 1 line carrying v-small, got %+v", b.Lines)
	}
}
