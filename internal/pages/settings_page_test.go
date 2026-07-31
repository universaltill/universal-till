package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func TestShortDeviceID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"till-123", "till-123"},
		{"1234567890123456", "1234567890123456"}, // exactly 16, untouched
		{"till-0123456789abcdef-tail", "till-0123456789a…"}, // >16 → first 16 + ellipsis
	}
	for _, c := range cases {
		if got := shortDeviceID(c.in); got != c.want {
			t.Errorf("shortDeviceID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

var mgrUser = auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}
var cashUser = auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}

// The preferred-payment-method and per-provider fee endpoints are manager-gated
// and persist into the settings store.
func TestPaymentsSettingsEndpoints(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	// A cashier is refused (HTMX swap target → 200 with an error span).
	rec := postForm(mux, "/api/settings/payments-default", url.Values{"method": {"cash"}}, &cashUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `class="error"`) {
		t.Fatalf("cashier payments-default: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// A manager sets the default and it persists.
	rec = postForm(mux, "/api/settings/payments-default", url.Values{"method": {"cash"}}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("manager payments-default: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), "payments.default_method"); v != "cash" {
		t.Fatalf("payments.default_method = %q", v)
	}

	// Fee: missing method and out-of-range percent are rejected.
	if rec := postForm(mux, "/api/settings/payments-fee", url.Values{"percent": {"1"}}, &mgrUser); !strings.Contains(rec.Body.String(), "method") {
		t.Fatalf("empty method: %s", rec.Body.String())
	}
	if rec := postForm(mux, "/api/settings/payments-fee", url.Values{"method": {"card"}, "percent": {"150"}}, &mgrUser); !strings.Contains(rec.Body.String(), "range") {
		t.Fatalf("out-of-range percent: %s", rec.Body.String())
	}

	// Valid fee: 1.75% + 0.20 fixed → bp=175, fixed=20 (minor units).
	rec = postForm(mux, "/api/settings/payments-fee", url.Values{"method": {"card"}, "percent": {"1.75"}, "fixed": {"0.20"}}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("valid fee: code=%d body=%s", rec.Code, rec.Body.String())
	}
	raw, ok, _ := d.Settings.Get(t.Context(), "payments.fee.card")
	if !ok || !strings.Contains(raw, `"bp":175`) || !strings.Contains(raw, `"fixed":20`) {
		t.Fatalf("stored fee = %q ok=%v", raw, ok)
	}
}

// Idle auto-lock is manager-gated, range-validated, and threads through to the
// auth service + runtime state.
func TestIdleLockEndpoint(t *testing.T) {
	mux, svc, d := newFullAuthDeps(t)

	if rec := postForm(mux, "/api/settings/idle-lock", url.Values{"minutes": {"10"}}, &cashUser); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier idle-lock = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/idle-lock", url.Values{"minutes": {"999"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range idle-lock = %d, want 400", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/idle-lock", url.Values{"minutes": {"15"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("valid idle-lock = %d, want 204", rec.Code)
	}
	if d.CurrentState().IdleLockMinutes != 15 || svc.IdleLockMinutes() != 15 {
		t.Fatalf("idle lock not applied: state=%d svc=%d", d.CurrentState().IdleLockMinutes, svc.IdleLockMinutes())
	}
}

// The kiosk idle-reset window is manager-gated, range-validated (0..600s) and
// stored in runtime state.
func TestKioskIdleResetEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if rec := postForm(mux, "/api/settings/kiosk-idle-reset", url.Values{"seconds": {"30"}}, &cashUser); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier kiosk-idle-reset = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/kiosk-idle-reset", url.Values{"seconds": {"999"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range kiosk = %d, want 400", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/kiosk-idle-reset", url.Values{"seconds": {"45"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("valid kiosk = %d, want 204", rec.Code)
	}
	if d.CurrentState().KioskIdleResetSeconds != 45 {
		t.Fatalf("kiosk idle reset = %d, want 45", d.CurrentState().KioskIdleResetSeconds)
	}
}

// Telemetry opt-in is manager-gated and stored as a string flag.
func TestTelemetryEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if rec := postForm(mux, "/api/settings/telemetry", url.Values{"optIn": {"on"}}, &cashUser); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier telemetry = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/telemetry", url.Values{"optIn": {"on"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("manager telemetry = %d, want 204", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), "marketplace.telemetry_opt_in"); v != "true" {
		t.Fatalf("telemetry_opt_in = %q, want true", v)
	}
	// Unchecked box stores false.
	if rec := postForm(mux, "/api/settings/telemetry", url.Values{}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("clear telemetry = %d", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), "marketplace.telemetry_opt_in"); v != "false" {
		t.Fatalf("telemetry_opt_in = %q, want false", v)
	}
}

// UI scale / theme / save / upsert are the ungated per-till display + store
// preferences; they validate and reflect into runtime state.
func TestDisplayAndStoreSettings(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	// UI scale bounds (0.5..2.0).
	if rec := postForm(mux, "/api/settings/ui-scale", url.Values{"scale": {"0.1"}}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range scale = %d, want 400", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/ui-scale", url.Values{"scale": {"1.5"}}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("valid scale = %d, want 204", rec.Code)
	}
	if d.CurrentState().UIScale != 1.5 {
		t.Fatalf("ui scale = %v, want 1.5", d.CurrentState().UIScale)
	}

	// Theme.
	if rec := postForm(mux, "/api/settings/theme", url.Values{"theme": {"dark"}}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("theme = %d", rec.Code)
	}
	if d.CurrentState().Theme != "dark" {
		t.Fatalf("theme = %q, want dark", d.CurrentState().Theme)
	}

	// Store save: currency/country/tax rate — the fields the shipped currency
	// card actually posts. TaxInclusive/AllowNegativeInventory are NOT settable
	// via /save (see TestSaveSettingsCurrencyOnlyDoesNotClearTaxOrInventoryFlags);
	// they go through /upsert below, same as a real manager would use the
	// settings page's generic key/value editor.
	rec := postForm(mux, "/api/settings/save", url.Values{
		"currency":   {"EUR"},
		"country":    {"DE"},
		"taxRatePct": {"19"},
	}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("save = %d", rec.Code)
	}
	st := d.CurrentState()
	if st.Currency != "EUR" || st.Country != "DE" || st.TaxRatePct != 19 {
		t.Fatalf("save not applied: %+v", st)
	}

	// Upsert: empty key is a 400; a known key reflects into state.
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"value": {"x"}}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty key = %d, want 400", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.tax_inclusive"}, "value": {"true"}}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("upsert = %d", rec.Code)
	}
	if !d.CurrentState().TaxInclusive {
		t.Fatal("upsert did not reflect store.tax_inclusive=true into state")
	}
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"pos.allow_negative_inventory"}, "value": {"true"}}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("upsert = %d", rec.Code)
	}
	if !d.CurrentState().AllowNegativeInventory {
		t.Fatal("upsert did not reflect pos.allow_negative_inventory=true into state")
	}
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"pos.allow_negative_inventory"}, "value": {"false"}}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("upsert = %d", rec.Code)
	}
	if d.CurrentState().AllowNegativeInventory {
		t.Fatal("upsert did not reflect pos.allow_negative_inventory=false into state")
	}
	if v, _, _ := d.Settings.Get(t.Context(), "pos.allow_negative_inventory"); v != "false" {
		t.Fatalf("stored allow_negative = %q", v)
	}
}

// Regression: the shipped currency card (web/ui/pages/settings.html) posts
// ONLY "currency" to /api/settings/save — it has no taxInclusive/
// allowNegativeInventory fields at all. The handler must not silently zero
// those flags just because a currency-only POST didn't include them (ut-docs#178).
func TestSaveSettingsCurrencyOnlyDoesNotClearTaxOrInventoryFlags(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	// Seed both flags on, as a shop that explicitly enabled them would have —
	// persisted to the store as well as in-memory, matching a real boot.
	st := d.UpdateState(func(s *common.RuntimeState) {
		s.TaxInclusive = true
		s.AllowNegativeInventory = true
	})
	common.SaveState(t.Context(), d.Settings, st)

	// Reproduce the real shipped form exactly: only "currency" is posted.
	rec := postForm(mux, "/api/settings/save", url.Values{"currency": {"GBP"}}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("save = %d", rec.Code)
	}

	got := d.CurrentState()
	if got.Currency != "GBP" {
		t.Fatalf("currency = %q, want GBP", got.Currency)
	}
	if !got.TaxInclusive {
		t.Fatal("currency-only save silently cleared TaxInclusive (state)")
	}
	if !got.AllowNegativeInventory {
		t.Fatal("currency-only save silently cleared AllowNegativeInventory (state)")
	}

	// Persistence must reflect the same, not just the in-memory copy.
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyTaxInclusive); v != "true" {
		t.Fatalf("stored %s = %q, want true", common.KeyTaxInclusive, v)
	}
	if v, _, _ := d.Settings.Get(t.Context(), "pos.allow_negative_inventory"); v != "true" {
		t.Fatalf("stored pos.allow_negative_inventory = %q, want true", v)
	}
}

// The claim-code / register-now / fleet enrol endpoints all refuse a non-manager
// operator before ever touching the marketplace (offline-first, no network in
// A high-density small touchscreen (e.g. a 10.1" 1920x1200 panel, ~224 PPI)
// needs more than the old 150% ceiling to render legibly -- the backend
// already validates up to 2.0 (see the bounds check above), but the
// settings page itself never offered an option past 150%, so a shop on
// that hardware had no way to reach a usable scale through the UI at all.
func TestSettingsPageOffersHighDensityScaleOption(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="2"`) {
		t.Fatalf("settings page offers no 200%% scale option for high-density small panels:\n%s", body)
	}
	// The value the backend actually applies for a "200%" pick must itself
	// be within the already-validated 0.5..2.0 range -- posting it should
	// succeed, not 400.
	if rec := postForm(mux, "/api/settings/ui-scale", url.Values{"scale": {"2"}}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("scale=2 (the new option's value) = %d, want 204", rec.Code)
	}
}

// the forbidden path). Each answers 200 (HTMX swap target) with an error/muted
// notice rather than a hard status.
func TestEnrolEndpointsRefuseNonManager(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	for _, ep := range []struct {
		method, path string
	}{
		{http.MethodPost, "/api/enrol/claim-code"},
		{http.MethodPost, "/api/enrol/now"},
		{http.MethodGet, "/api/enrol/devices"},
	} {
		req := auth.WithUser(httptest.NewRequest(ep.method, ep.path, nil), cashUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: code=%d, want 200 (HTMX swap target)", ep.method, ep.path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `class="error"`) && !strings.Contains(body, `class="muted"`) {
			t.Fatalf("%s %s did not render a forbidden notice: %s", ep.method, ep.path, body)
		}
	}
}
