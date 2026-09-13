package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#2209: tapping a variant-bearing item tile (small/regular/large)
// on the sale screen must open the picker and ask which variant BEFORE
// adding a line — never straight to the basket at the parent item's own
// base price. setupVariantModifiersTestDeps seeds one item with three real
// active variants (each with its own barcode and price, resolvable through
// a stubResolver exactly like setupModifiersTestDeps does for modifiers),
// a second, unrelated item with its own variant (the cross-item guard's
// "item B" for the money-critical test below), and one codeless variant
// (no barcode, no SKU) that must never reach the picker at all.
func setupVariantModifiersTestDeps(t *testing.T) (*common.Deps, *db.DB) {
	t.Helper()
	chdirRoot(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "variant-mods.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// itm-coffee's OWN base price (999) is deliberately far from any of its
	// variants' prices — TestScanWithModifiers_ValidVariant... below relies
	// on this to prove the added line carries the VARIANT's price, not a
	// silently-unchanged parent price.
	execAll(t, d, []string{
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-coffee', 'COFFEE', 'Flat White', 999, 1)`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-tea', 'TEA', 'Tea', 250, 1)`,

		`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-small', 'itm-coffee', 'COFFEE-S', 'Small', 250, 1)`,
		`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-reg',   'itm-coffee', 'COFFEE-R', 'Regular', 310, 1)`,
		`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-large', 'itm-coffee', 'COFFEE-L', 'Large', 350, 1)`,
		// Codeless: no barcode row, and blank SKU — must never be offered.
		`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-nocode', 'itm-coffee', NULL, 'Ghost Size', 999, 1)`,
		`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-tea-1',  'itm-tea', 'TEA-1', 'Mug', 250, 1)`,

		`INSERT INTO variant_barcodes (barcode, variant_id, is_primary) VALUES ('C-S', 'v-small', 1)`,
		`INSERT INTO variant_barcodes (barcode, variant_id, is_primary) VALUES ('C-R', 'v-reg', 1)`,
		`INSERT INTO variant_barcodes (barcode, variant_id, is_primary) VALUES ('C-L', 'v-large', 1)`,
		`INSERT INTO variant_barcodes (barcode, variant_id, is_primary) VALUES ('T-1', 'v-tea-1', 1)`,

		// itm-coffee also has an (optional) modifier group, to prove
		// variant + modifier selections fold together correctly.
		`INSERT INTO item_modifier_groups (id, item_id, name, required, min_select, max_select, sort_order) VALUES ('g-extras', 'itm-coffee', 'Extras', 0, 0, 2, 1)`,
		`INSERT INTO item_modifier_group_links (item_id, group_id, sort_order) VALUES ('itm-coffee', 'g-extras', 1)`,
		`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order) VALUES ('o-shot', 'g-extras', 'Extra shot', 50, 1)`,
	})

	resolver := stubResolver{
		"COFFEE": {SKU: "COFFEE", ItemID: "itm-coffee", Name: "Flat White", Qty: 1, PriceCents: 999},
		"TEA":    {SKU: "TEA", ItemID: "itm-tea", Name: "Tea", Qty: 1, PriceCents: 250},
		"C-S":    {SKU: "COFFEE-S", ItemID: "itm-coffee", VariantID: "v-small", Name: "Flat White Small", Qty: 1, PriceCents: 250},
		"C-R":    {SKU: "COFFEE-R", ItemID: "itm-coffee", VariantID: "v-reg", Name: "Flat White Regular", Qty: 1, PriceCents: 310},
		"C-L":    {SKU: "COFFEE-L", ItemID: "itm-coffee", VariantID: "v-large", Name: "Flat White Large", Qty: 1, PriceCents: 350},
		"T-1":    {SKU: "TEA-1", ItemID: "itm-tea", VariantID: "v-tea-1", Name: "Tea Mug", Qty: 1, PriceCents: 250},
	}
	dp := &common.Deps{
		State:       common.RuntimeState{Currency: "GBP", TaxRatePct: 20},
		Engine:      pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver),
		KioskEngine: pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver),
		Db:          d.DB,
	}
	return dp, d
}

func execAll(t *testing.T, d *db.DB, stmts []string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
}

func TestGetModifiers_RendersVariantFieldsetFilteringCodeless(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSModifiersAPI(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/pos/modifiers?item=itm-coffee&code=COFFEE", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Small", "Regular", "Large", `name="variantId"`} {
		if !strings.Contains(body, want) {
			t.Errorf("picker missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Ghost Size") {
		t.Errorf("a codeless variant (no barcode, no SKU) must never be offered: %s", body)
	}
	if !strings.Contains(body, `type="radio" name="variantId"`) {
		t.Errorf("expected radio inputs for the variant picker: %s", body)
	}
}

// The single most important test in this change: item A's itemId paired
// with item B's variantId must be rejected, and MUST NOT add a line —
// selling item B's variant priced/labeled as item A would be a silent
// money-correctness bug identical in shape to the one this whole card
// exists to fix.
func TestScanWithModifiers_RejectsCrossItemVariant(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSModifiersAPI(mux, dp)

	// code/itemId correctly pair (COFFEE -> itm-coffee), but variantId
	// (v-tea-1) belongs to itm-tea, not itm-coffee.
	form := url.Values{"code": {"COFFEE"}, "itemId": {"itm-coffee"}, "variantId": {"v-tea-1"}}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan-with-modifiers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a cross-item variant, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(dp.Engine.Basket().Lines) != 0 {
		t.Fatal("a rejected cross-item variant submission must not add a line")
	}
}

func TestScanWithModifiers_RejectsMissingVariantIdWhenItemHasVariants(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSModifiersAPI(mux, dp)

	form := url.Values{"code": {"COFFEE"}, "itemId": {"itm-coffee"}}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan-with-modifiers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 when a variant-bearing item is submitted with no variantId, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(dp.Engine.Basket().Lines) != 0 {
		t.Fatal("a rejected submission must not add a line")
	}
}

func TestScanWithModifiers_ValidVariantAddsLineWithVariantIDAndPrice(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSModifiersAPI(mux, dp)

	form := url.Values{"code": {"COFFEE"}, "itemId": {"itm-coffee"}, "variantId": {"v-reg"}}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan-with-modifiers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	b := dp.Engine.Basket()
	if len(b.Lines) != 1 {
		t.Fatalf("want 1 basket line, got %d", len(b.Lines))
	}
	line := b.Lines[0]
	if line.VariantID != "v-reg" {
		t.Fatalf("want VariantID v-reg, got %q (ItemID=%q)", line.VariantID, line.ItemID)
	}
	if line.PriceCents != 310 {
		t.Fatalf("want the VARIANT's own price 310 (not the parent item's base 999), got %d", line.PriceCents)
	}
}

func TestScanWithModifiers_VariantAndModifiersCombine(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSModifiersAPI(mux, dp)

	form := url.Values{
		"code":         {"COFFEE"},
		"itemId":       {"itm-coffee"},
		"variantId":    {"v-large"},
		"mod_g-extras": {"o-shot"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan-with-modifiers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	b := dp.Engine.Basket()
	if len(b.Lines) != 1 {
		t.Fatalf("want 1 basket line, got %d", len(b.Lines))
	}
	line := b.Lines[0]
	if line.VariantID != "v-large" {
		t.Fatalf("want VariantID v-large, got %q", line.VariantID)
	}
	if line.PriceCents != 400 { // 350 (Large) + 50 (Extra shot)
		t.Fatalf("want folded price 400 (350 variant + 50 modifier), got %d", line.PriceCents)
	}
}

// Proves the kiosk path shares resolveAndValidateModifiers for real, not by
// assumption — the exact same cross-item guard must fire through
// /api/self-order/scan-with-modifiers using d.KioskEngine.
func TestSelfOrderScanWithModifiers_RejectsCrossItemVariant(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	form := url.Values{"code": {"COFFEE"}, "itemId": {"itm-coffee"}, "variantId": {"v-tea-1"}}
	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan-with-modifiers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a cross-item variant on the kiosk path, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(dp.KioskEngine.Basket().Lines) != 0 {
		t.Fatal("a rejected cross-item variant submission must not add a line to the kiosk basket")
	}
	// And the cashier's OWN basket must be untouched too (ADR-0020 kiosk
	// isolation) — a bug that accidentally touched d.Engine instead of
	// d.KioskEngine would still pass the assertion above.
	if len(dp.Engine.Basket().Lines) != 0 {
		t.Fatal("the kiosk submission must never touch the cashier's basket")
	}
}

// The kiosk browse grid must route a variant-only item (no modifiers) to
// the picker, exactly like the cashier grid (internal/ui's
// TestButtonsHTTPList_VariantOnlyItemOpensPickerNotStraightToBasket).
func TestSelfOrderShop_GridRoutesVariantOnlyItemToPicker(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	// itm-tea (sku "TEA") has a variant (v-tea-1) and no modifier groups.
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/self-order/grid", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/api/self-order/modifiers?item=itm-tea") {
		t.Fatalf("a variant-only kiosk tile must open the picker: %s", body)
	}
}

func TestSelfOrderScanWithModifiers_ValidVariantAddsLineToKioskBasket(t *testing.T) {
	dp, _ := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	form := url.Values{"code": {"COFFEE"}, "itemId": {"itm-coffee"}, "variantId": {"v-small"}}
	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan-with-modifiers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	b := dp.KioskEngine.Basket()
	if len(b.Lines) != 1 || b.Lines[0].VariantID != "v-small" || b.Lines[0].PriceCents != 250 {
		t.Fatalf("want 1 line, VariantID=v-small, price=250, got %+v", b.Lines)
	}
}

// ut-docs#2209 review, blocker 1 — the deactivation race. len(variants) is
// read at SUBMIT time, but the picker rendered earlier, so an item's last
// sellable variant can be retired while the modal sits open. Before the fix
// the submitted variantId was silently discarded and the PARENT base line
// was added: a 999 line for a 310 coffee, no error, no log — the exact money
// defect this card exists to remove, laundered through a dialog that looks
// like it asked the question.
//
// This test drives the real handler, deactivating every sellable variant
// between the picker render and the POST rather than simulating the state,
// so it fails if the guard is removed.
func TestScanWithModifiers_VariantDeactivatedMidPickIsRejectedNotParentPriced(t *testing.T) {
	dp, d := setupVariantModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSModifiersAPI(mux, dp)

	// The operator opened the picker while the variants were live.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/pos/modifiers?item=itm-coffee&code=COFFEE", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("picker render = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "v-reg") {
		t.Fatalf("picker should have offered v-reg while it was active: %.300s", rec.Body.String())
	}

	// ...then a manager retires every sellable variant of that item while
	// the modal is still open (v-nocode stays active but is codeless, so it
	// is not sellable either — the item now has NO sellable variant).
	execAll(t, d, []string{
		`UPDATE item_variants SET is_active = 0 WHERE item_id = 'itm-coffee' AND id <> 'v-nocode'`,
	})

	form := url.Values{"code": {"COFFEE"}, "itemId": {"itm-coffee"}, "variantId": {"v-reg"}}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan-with-modifiers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)

	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("want 400 when the chosen variant is gone, got %d: %.300s", rec2.Code, rec2.Body.String())
	}
	// The assertion that actually matters: no line at ANY price, and
	// emphatically not one at the parent's 999.
	if lines := dp.Engine.Basket().Lines; len(lines) != 0 {
		t.Fatalf("a variant retired mid-pick must add no line at all; got %d line(s), first priced %v", len(lines), lines[0].PriceCents)
	}
}

// ut-docs#2209 review, blocker 2 — the two predicates must agree.
// CatalogRepo.ItemIDsWithVariants decides whether the TILE opens the picker;
// sellableVariants decides what the picker OFFERS. While the first counted
// any active variant and the second required a resolvable code, an item whose
// variants were all codeless opened a picker with no variant fieldset and a
// live "Add to cart" button, and submitting it added the parent base price.
// This test pins the resolution: a codeless-only item is not "has variants".
func TestItemIDsWithVariants_ExcludesCodelessOnlyItem(t *testing.T) {
	dp, d := setupVariantModifiersTestDeps(t)
	_ = dp

	// itm-ghost's only active variant has neither a barcode nor a SKU, so
	// it can never be resolved at sale time and must not make the tile open
	// a picker.
	execAll(t, d, []string{
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-ghost', 'GHOST', 'Ghost', 500, 1)`,
		`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-ghost', 'itm-ghost', NULL, 'Only Size', 500, 1)`,
	})

	got, err := data.NewCatalogRepo(d.DB).ItemIDsWithVariants(t.Context(), []string{"itm-coffee", "itm-ghost", "itm-tea"})
	if err != nil {
		t.Fatalf("ItemIDsWithVariants: %v", err)
	}
	if !got["itm-coffee"] {
		t.Error("itm-coffee has three coded variants and must be present")
	}
	if !got["itm-tea"] {
		t.Error("itm-tea has one coded variant and must be present")
	}
	if got["itm-ghost"] {
		t.Error("itm-ghost's only variant is codeless — its tile must NOT open a picker it cannot fill (ut-docs#2209 review, blocker 2)")
	}
}
