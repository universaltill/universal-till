package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/barcode"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// TestSettingsPage_BarcodeSymbologiesChecklist covers the GET /settings
// render side of ut-docs#935: every registry entry appears once, the
// compatibility-preserving defaults (ADR-0059 §2 — everything except the
// two embedded-data entries) are checked on a fresh shop, and toggling one
// on via the API flips its checkbox on the next render.
func TestSettingsPage_BarcodeSymbologiesChecklist(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req = auth.WithUser(req, mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}

	body := get()
	for _, id := range []string{
		"EAN13", "EAN8", "UPCA", "UPCE", "GTIN14", "CODE128", "CODE39",
		"INTERNAL_PLU", "EAN13_WEIGHT_PREFIX2X", "EAN13_PRICE_PREFIX02",
	} {
		if !strings.Contains(body, `id="bcsym-`+id+`"`) {
			t.Fatalf("expected a checkbox for %s, got:\n%s", id, body)
		}
	}
	// Default-on entries are checked; the two embedded-data entries are not.
	if !checkboxChecked(body, "EAN13") {
		t.Fatalf("expected EAN13 checked by default, got:\n%s", body)
	}
	if checkboxChecked(body, "EAN13_WEIGHT_PREFIX2X") {
		t.Fatalf("expected EAN13_WEIGHT_PREFIX2X unchecked by default, got:\n%s", body)
	}
	if checkboxChecked(body, "EAN13_PRICE_PREFIX02") {
		t.Fatalf("expected EAN13_PRICE_PREFIX02 unchecked by default, got:\n%s", body)
	}

	// Enable the weight-embedded entry via the API, then confirm the next
	// render shows it checked.
	if rec := postForm(mux, "/api/settings/barcode-symbology",
		url.Values{"id": {"EAN13_WEIGHT_PREFIX2X"}, "enabled": {"true"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("enable EAN13_WEIGHT_PREFIX2X = %d, want 204", rec.Code)
	}
	if !checkboxChecked(get(), "EAN13_WEIGHT_PREFIX2X") {
		t.Fatal("expected EAN13_WEIGHT_PREFIX2X checked after enabling it")
	}
}

// checkboxChecked reports whether the checkbox with the given registry id
// carries the `checked` attribute. Scans forward from the id to the
// closing `>` of that same input tag and looks for a literal "checked>" —
// a plain `strings.Contains(tag, "checked")` false-positives on this
// template, whose hx-on::after-request handler contains the JS expression
// `this.checked=!this.checked` (found the hard way: this test failed on
// EAN13_WEIGHT_PREFIX2X, which is unchecked by default, until fixed).
func checkboxChecked(body, id string) bool {
	needle := `id="bcsym-` + id + `"`
	i := strings.Index(body, needle)
	if i < 0 {
		return false
	}
	end := strings.Index(body[i:], ">")
	if end < 0 {
		return false
	}
	tag := body[i : i+end+1]
	return strings.Contains(tag, "checked>")
}

// ut-docs#935 (ADR-0059 Decision §2): the barcode-symbology checklist toggle
// is manager-gated (same checkOrElevate shape as launch-on-startup), rejects
// an unknown registry id, and persists through
// SettingsRepo.EnabledBarcodeSymbologies — the same accessor the scan path
// and AddBarcode read (#933/#934).
func TestBarcodeSymbologyEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	settingsRepo := data.NewSettingsRepo(d.Db)

	// A cashier without an approver PIN gets the in-place elevation prompt,
	// not a flat 403 (same as launch-on-startup/idle-lock).
	if rec := postForm(mux, "/api/settings/barcode-symbology",
		url.Values{"id": {"EAN13_WEIGHT_PREFIX2X"}, "enabled": {"true"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier barcode-symbology = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}

	// An unknown registry id is rejected before ever touching settings.
	if rec := postForm(mux, "/api/settings/barcode-symbology",
		url.Values{"id": {"NOT_A_REAL_SYMBOLOGY"}, "enabled": {"true"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown id = %d, want 400", rec.Code)
	}

	// A malformed enabled value is rejected the same way as launch-on-startup.
	if rec := postForm(mux, "/api/settings/barcode-symbology",
		url.Values{"id": {"EAN13_WEIGHT_PREFIX2X"}, "enabled": {"not-a-bool"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed enabled = %d, want 400", rec.Code)
	}

	// The two embedded-data symbologies default OFF (ADR-0059 §2) — enabling
	// one is the interesting direction to test first.
	if rec := postForm(mux, "/api/settings/barcode-symbology",
		url.Values{"id": {"EAN13_WEIGHT_PREFIX2X"}, "enabled": {"true"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("enable EAN13_WEIGHT_PREFIX2X = %d, want 204", rec.Code)
	}
	ids, err := settingsRepo.EnabledBarcodeSymbologies(t.Context())
	if err != nil {
		t.Fatalf("read enabled symbologies: %v", err)
	}
	if !slicesContain(ids, "EAN13_WEIGHT_PREFIX2X") {
		t.Fatalf("enabled symbologies = %v, want EAN13_WEIGHT_PREFIX2X present", ids)
	}
	// Compatibility-preserving defaults must still be there — toggling one
	// entry on must not have dropped the rest of the default set.
	if !slicesContain(ids, "EAN13") || !slicesContain(ids, "CODE128") {
		t.Fatalf("enabled symbologies = %v, want the default set preserved alongside the new entry", ids)
	}

	// Disabling a default-on entry (EAN13) removes it and nothing else.
	if rec := postForm(mux, "/api/settings/barcode-symbology",
		url.Values{"id": {"EAN13"}, "enabled": {"false"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("disable EAN13 = %d, want 204", rec.Code)
	}
	ids, err = settingsRepo.EnabledBarcodeSymbologies(t.Context())
	if err != nil {
		t.Fatalf("read enabled symbologies: %v", err)
	}
	if slicesContain(ids, "EAN13") {
		t.Fatalf("enabled symbologies = %v, want EAN13 removed", ids)
	}
	if !slicesContain(ids, "EAN13_WEIGHT_PREFIX2X") {
		t.Fatalf("enabled symbologies = %v, want EAN13_WEIGHT_PREFIX2X still present", ids)
	}

	// Toggling the same id off twice is a no-op, not an error or a
	// duplicate-removal panic.
	if rec := postForm(mux, "/api/settings/barcode-symbology",
		url.Values{"id": {"EAN13"}, "enabled": {"false"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("re-disable EAN13 = %d, want 204", rec.Code)
	}
}

// TestBarcodeSymbologyEndpoint_CannotDisableLastEnabled is ut-docs#935
// review finding MAJOR 3: unticking every symbology would leave every scan
// and every untyped AddBarcode call matching nothing, silently. Disabling
// the last remaining enabled entry must be refused (400), and the set must
// stay non-empty afterward.
func TestBarcodeSymbologyEndpoint_CannotDisableLastEnabled(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	settingsRepo := data.NewSettingsRepo(d.Db)

	// Narrow the shop down to exactly one enabled symbology.
	if err := settingsRepo.SetEnabledBarcodeSymbologies(t.Context(), []string{"EAN13"}); err != nil {
		t.Fatalf("seed enabled symbologies: %v", err)
	}

	if rec := postForm(mux, "/api/settings/barcode-symbology",
		url.Values{"id": {"EAN13"}, "enabled": {"false"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("disable the last enabled symbology = %d, want 400", rec.Code)
	}
	ids, err := settingsRepo.EnabledBarcodeSymbologies(t.Context())
	if err != nil {
		t.Fatalf("read enabled symbologies: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("enabled symbologies = [], want the refused toggle to have written nothing")
	}
	if !slicesContain(ids, "EAN13") {
		t.Fatalf("enabled symbologies = %v, want EAN13 still present (refused toggle must not partially apply)", ids)
	}
}

// TestSetBarcodeSymbologyEnabled_ConcurrentTogglesDoNotLoseEachOther is
// ut-docs#935 review finding MAJOR 4: the old settings_page.go handler did
// a separate read then a separate full-list write, so two toggles issued
// close together (plausible with ten independent checkboxes and no
// hx-sync between them) could race — both read the same starting list,
// both write their own version, and whichever write lands second silently
// discards the first. SetBarcodeSymbologyEnabled now does the whole
// read-modify-write inside one DB transaction; toggling two DIFFERENT ids
// concurrently must land both, not just one.
func TestSetBarcodeSymbologyEnabled_ConcurrentTogglesDoNotLoseEachOther(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	settingsRepo := data.NewSettingsRepo(d.Db)
	ctx := t.Context()

	// Start from a known, narrow set so the two concurrent enables are each
	// independently observable in the result.
	if err := settingsRepo.SetEnabledBarcodeSymbologies(ctx, []string{"EAN13"}); err != nil {
		t.Fatalf("seed enabled symbologies: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"EAN8", "UPCA"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := settingsRepo.SetBarcodeSymbologyEnabled(ctx, id, true); err != nil {
				errs <- err
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent enable failed: %v", err)
	}

	ids, err := settingsRepo.EnabledBarcodeSymbologies(ctx)
	if err != nil {
		t.Fatalf("read enabled symbologies: %v", err)
	}
	if !slicesContain(ids, "EAN8") || !slicesContain(ids, "UPCA") {
		t.Fatalf("enabled symbologies = %v, want BOTH EAN8 and UPCA present — one concurrent toggle was lost", ids)
	}
	if !slicesContain(ids, "EAN13") {
		t.Fatalf("enabled symbologies = %v, want the original EAN13 still present", ids)
	}
}

// TestBarcodeSymbologyNameKeysResolveInEveryLocale is ut-docs#935 review
// finding MINOR 6: guard-i18n.sh only scans LITERAL `{{ T "key" }}`
// template calls; settings.html renders each row's name via the dynamic
// `{{ T .NameKey }}`, which the guard cannot see. A registry entry whose
// NameKey has no translation would render the raw key string as UI text
// with CI green. This test is the guard the static scanner can't be.
func TestBarcodeSymbologyNameKeysResolveInEveryLocale(t *testing.T) {
	newFullAuthDeps(t) // wires httpx's translator (InitI18n) as a side effect
	locales := httpx.AvailableLocales()
	if len(locales) == 0 {
		t.Fatal("no locales available — translator not wired by test setup")
	}
	for _, id := range barcode.Default().IDs() {
		sym, ok := barcode.Default().Lookup(id)
		if !ok {
			t.Fatalf("registry.IDs() returned %s but Lookup found nothing", id)
		}
		for _, locale := range locales {
			got := httpx.T(locale, sym.NameKey)
			if got == sym.NameKey {
				t.Errorf("barcode symbology %s: NameKey %q has no translation in locale %q (T returned the key itself)", id, sym.NameKey, locale)
			}
		}
	}
}

func slicesContain(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
