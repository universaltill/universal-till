package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

func setupModifiersTestDeps(t *testing.T) (*common.Deps, *db.DB) {
	t.Helper()
	chdirRoot(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "mods.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-coffee', 'COFFEE', 'Flat White', 320, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`
		INSERT INTO item_modifier_groups (id, item_id, name, required, min_select, max_select, sort_order)
		VALUES ('g-extras', 'itm-coffee', 'Extras', 0, 0, 2, 1)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`
		INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order)
		VALUES ('o-shot', 'g-extras', 'Extra shot', 50, 1), ('o-oat', 'g-extras', 'Oat milk', 40, 2)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`
		INSERT INTO item_modifier_groups (id, item_id, name, required, min_select, max_select, sort_order)
		VALUES ('g-size', 'itm-coffee', 'Size', 1, 1, 1, 2)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`
		INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order)
		VALUES ('o-reg', 'g-size', 'Regular', 0, 1), ('o-lrg', 'g-size', 'Large', 100, 2)
	`); err != nil {
		t.Fatal(err)
	}

	resolver := stubResolver{
		"COFFEE": {SKU: "COFFEE", ItemID: "itm-coffee", Name: "Flat White", Qty: 1, PriceCents: 320},
	}
	dp := &common.Deps{
		State:  common.RuntimeState{Currency: "GBP", TaxRatePct: 20},
		Engine: pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver),
		Db:     d.DB,
	}
	return dp, d
}

func TestGetModifiers_RendersPickerWithGroups(t *testing.T) {
	dp, _ := setupModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSModifiersAPI(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/pos/modifiers?item=itm-coffee&code=COFFEE", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Flat White", "Extras", "Extra shot", "Oat milk", "Size", "Regular", "Large"} {
		if !strings.Contains(body, want) {
			t.Errorf("picker missing %q", want)
		}
	}
	// Size group has max_select=1 -> radios; Extras has max_select=2 -> checkboxes.
	if !strings.Contains(body, `type="radio" name="mod_g-size"`) {
		t.Error("expected radio inputs for the single-select Size group")
	}
	if !strings.Contains(body, `type="checkbox" name="mod_g-extras"`) {
		t.Error("expected checkbox inputs for the multi-select Extras group")
	}
}

func TestGetModifiers_UnknownItemIs404(t *testing.T) {
	dp, _ := setupModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSModifiersAPI(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/pos/modifiers?item=itm-coffee&code=NOPE", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for an unresolvable code, got %d", rec.Code)
	}
}

func TestScanWithModifiers_AddsLineWithFoldedPrice(t *testing.T) {
	dp, _ := setupModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSModifiersAPI(mux, dp)

	form := url.Values{
		"code":         {"COFFEE"},
		"itemId":       {"itm-coffee"},
		"mod_g-size":   {"o-lrg"},  // required single-select: Large (+100)
		"mod_g-extras": {"o-shot"}, // optional multi-select: Extra shot (+50)
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
	if line.PriceCents != 470 { // 320 base + 100 large + 50 extra shot
		t.Fatalf("want folded price 470, got %d", line.PriceCents)
	}
	if len(line.Modifiers) != 2 {
		t.Fatalf("want 2 modifier snapshot entries, got %d: %+v", len(line.Modifiers), line.Modifiers)
	}
}

// Server must ignore whatever price/name the client claims and use ONLY the
// server-loaded option data — this is the security-relevant assertion.
func TestScanWithModifiers_IgnoresClientSuppliedNamesAndPrices(t *testing.T) {
	dp, _ := setupModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSModifiersAPI(mux, dp)

	// The client can only submit option IDs (the form has no name/price
	// fields at all) — this test documents that even if it tried to smuggle
	// extra fields, only the id is read.
	form := url.Values{
		"code":             {"COFFEE"},
		"itemId":           {"itm-coffee"},
		"mod_g-size":       {"o-reg"},
		"mod_g-size_price": {"-99999"}, // not a real field the handler reads
		"mod_g-size_name":  {"Free Coffee"},
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
	if b.Lines[0].PriceCents != 320 { // base only, Regular = +0
		t.Fatalf("want price 320 (server-authoritative, ignoring client-smuggled fields), got %d", b.Lines[0].PriceCents)
	}
	if b.Lines[0].Modifiers[0].OptionName != "Regular" {
		t.Fatalf("want server-loaded option name 'Regular', got %q", b.Lines[0].Modifiers[0].OptionName)
	}
}

func TestScanWithModifiers_RejectsOptionFromWrongGroup(t *testing.T) {
	dp, _ := setupModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSModifiersAPI(mux, dp)

	// o-lrg belongs to g-size, not g-extras — a manipulated submission.
	form := url.Values{
		"code":         {"COFFEE"},
		"itemId":       {"itm-coffee"},
		"mod_g-size":   {"o-reg"},
		"mod_g-extras": {"o-lrg"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan-with-modifiers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for an option id from the wrong group, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(dp.Engine.Basket().Lines) != 0 {
		t.Fatal("a rejected submission must not add a line")
	}
}

func TestScanWithModifiers_RejectsMissingRequiredGroup(t *testing.T) {
	dp, _ := setupModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSModifiersAPI(mux, dp)

	// g-size is required (min_select=1) but omitted entirely.
	form := url.Values{
		"code":   {"COFFEE"},
		"itemId": {"itm-coffee"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan-with-modifiers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 when a required group has no selection, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(dp.Engine.Basket().Lines) != 0 {
		t.Fatal("a rejected submission must not add a line")
	}
}

func TestScanWithModifiers_RejectsTooManySelections(t *testing.T) {
	dp, _ := setupModifiersTestDeps(t)
	mux := http.NewServeMux()
	registerPOSModifiersAPI(mux, dp)

	// g-extras allows max 2; submit 2 valid ones plus try a third distinct
	// value is not possible (only 2 options exist), so instead violate
	// g-size's max_select=1 by submitting it twice.
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan-with-modifiers",
		strings.NewReader("code=COFFEE&itemId=itm-coffee&mod_g-size=o-reg&mod_g-size=o-lrg"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 when a single-select group gets 2 selections, got %d: %s", rec.Code, rec.Body.String())
	}
}
