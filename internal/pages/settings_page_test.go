package pages

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
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

	// A cashier without an approver PIN gets the in-place elevation prompt
	// (ut-docs#796) — still 200 (HTMX swap target), no longer the flat
	// forbidden error span.
	rec := postForm(mux, "/api/settings/payments-default", url.Values{"method": {"cash"}}, &cashUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") ||
		!strings.Contains(rec.Body.String(), `name="override_pin"`) {
		t.Fatalf("cashier payments-default: code=%d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
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

	// ut-docs#865: a denied cashier gets the in-place elevation prompt
	// (ut-docs#557/#796 mechanism), not a flat 403.
	if rec := postForm(mux, "/api/settings/idle-lock", url.Values{"minutes": {"10"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier idle-lock = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
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

	// ut-docs#865: elevation prompt, not a flat 403 (same as idle-lock).
	if rec := postForm(mux, "/api/settings/kiosk-idle-reset", url.Values{"seconds": {"30"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier kiosk-idle-reset = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
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

// Window mode (ut-docs#608 scaffold) is manager-gated, validated against the
// closed enum, and round-trips through runtime state / GET /settings.
func TestWindowModeEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	wc := &recordingWindowController{}
	d.WindowCtl = wc

	// ut-docs#865: elevation prompt, not a flat 403 (same as idle-lock).
	if rec := postForm(mux, "/api/settings/window-mode", url.Values{"mode": {"kiosk"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier window-mode = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	if len(wc.applyModeCalls) != 0 {
		t.Fatalf("cashier (unelevated) request must not reach ApplyMode: calls=%v", wc.applyModeCalls)
	}
	if rec := postForm(mux, "/api/settings/window-mode", url.Values{"mode": {"bogus"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid window-mode = %d, want 400", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/window-mode", url.Values{"mode": {"kiosk"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("valid window-mode = %d, want 204", rec.Code)
	}
	if d.CurrentState().WindowMode != "kiosk" {
		t.Fatalf("WindowMode = %q, want kiosk", d.CurrentState().WindowMode)
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyWindowMode); v != "kiosk" {
		t.Fatalf("stored %s = %q, want kiosk", common.KeyWindowMode, v)
	}
	// ut-docs#883: the hook is what actually flips the Pi kiosk service (or
	// any future real WindowController) — persisting the preference is not
	// enough on its own.
	if len(wc.applyModeCalls) != 1 || wc.applyModeCalls[0] != "kiosk" {
		t.Fatalf("ApplyMode calls = %v, want exactly one call with %q", wc.applyModeCalls, "kiosk")
	}

	// Round-trips via GET /settings: the freshly saved mode renders as the
	// selected <option>.
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	if !regexp.MustCompile(`value="kiosk"\s+selected`).MatchString(rec.Body.String()) {
		t.Fatalf("GET /settings body does not show kiosk as selected window mode:\n%s", rec.Body.String())
	}
}

// TestWindowModeEndpoint_ApplyModeFailureSurfacesAndDoesNotPersist covers
// ut-docs#883's "graceful, clearly-surfaced failure" requirement: a Pi that
// hasn't got the sudoers grant yet (pre-#883 upgrade) makes ApplyMode fail —
// the handler must report a server error rather than silently swallowing it,
// and must not persist a WindowMode the OS never actually applied.
func TestWindowModeEndpoint_ApplyModeFailureSurfacesAndDoesNotPersist(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	wc := &recordingWindowController{applyModeErr: errors.New("systemctl enable unitill-kiosk.service: sudo: a password is required")}
	d.WindowCtl = wc

	rec := postForm(mux, "/api/settings/window-mode", url.Values{"mode": {"kiosk"}}, &mgrUser)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("window-mode with failing ApplyMode = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if len(wc.applyModeCalls) != 1 || wc.applyModeCalls[0] != "kiosk" {
		t.Fatalf("ApplyMode calls = %v, want exactly one call with %q", wc.applyModeCalls, "kiosk")
	}
	if d.CurrentState().WindowMode == "kiosk" {
		t.Fatal("WindowMode must not be persisted as kiosk when ApplyMode failed to actually apply it")
	}
}

// TestWindowModeEndpoint_NilWindowCtlDoesNotPanic mirrors
// TestExitToOSEndpoint_NilWindowCtlDoesNotPanic: most test-Deps helpers
// don't set WindowCtl, so the handler must fall back to
// common.NoopWindowController rather than dereferencing a nil interface.
func TestWindowModeEndpoint_NilWindowCtlDoesNotPanic(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if d.WindowCtl != nil {
		t.Fatal("test assumes newFullAuthDeps leaves WindowCtl unset")
	}
	if rec := postForm(mux, "/api/settings/window-mode", url.Values{"mode": {"fullscreen"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("nil WindowCtl window-mode = %d, want 204 (fallback to NoopWindowController): %s", rec.Code, rec.Body.String())
	}
}

// Launch-on-startup (ut-docs#608 scaffold) is manager-gated, boolean, and
// round-trips through runtime state. It does NOT touch the filesystem here
// (ut-docs#611 review, M2/M3): OS-level application is the desktop shell's
// own job at its own next launch, via GET /api/window-mode — see
// TestWindowStateAPI_ExposesLaunchOnStartup and cmd/unitill-desktop's own
// autostart tests. This handler only persists the preference.
func TestLaunchOnStartupEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	// ut-docs#865: elevation prompt, not a flat 403 (same as idle-lock).
	if rec := postForm(mux, "/api/settings/launch-on-startup", url.Values{"enabled": {"true"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier launch-on-startup = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	if rec := postForm(mux, "/api/settings/launch-on-startup", url.Values{"enabled": {"not-a-bool"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed launch-on-startup = %d, want 400", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/launch-on-startup", url.Values{"enabled": {"true"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("enable launch-on-startup = %d, want 204", rec.Code)
	}
	if !d.CurrentState().LaunchOnStartup {
		t.Fatal("LaunchOnStartup = false, want true")
	}
	if rec := postForm(mux, "/api/settings/launch-on-startup", url.Values{"enabled": {"false"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("disable launch-on-startup = %d, want 204", rec.Code)
	}
	if d.CurrentState().LaunchOnStartup {
		t.Fatal("LaunchOnStartup = true, want false")
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyLaunchOnStartup); v != "false" {
		t.Fatalf("stored %s = %q, want false", common.KeyLaunchOnStartup, v)
	}
}

// recordingWindowController is a WindowController test double that records
// whether ExitToOS was invoked (so exit-to-os tests can assert the hook was,
// or for rejected auth was NOT, reached) and every mode ApplyMode was called
// with (ut-docs#883), optionally failing with applyModeErr to exercise the
// window-mode handler's failure path.
type recordingWindowController struct {
	called         bool
	applyModeCalls []string
	applyModeErr   error
}

func (r *recordingWindowController) ExitToOS() error {
	r.called = true
	return nil
}

func (r *recordingWindowController) ApplyMode(mode string) error {
	r.applyModeCalls = append(r.applyModeCalls, mode)
	return r.applyModeErr
}

// Exit-to-os (ut-docs#608 scaffold) requires a LIVE manager PIN — an existing
// manager session (isManagerOrAuthOff) is not enough — mirroring the
// blank-PIN-lockout fix in shifts_api.go's cash-adjustment/payout handlers.
func TestExitToOSEndpoint(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, _, d := newFullAuthDeps(t)
	wc := &recordingWindowController{}
	d.WindowCtl = wc

	hash, err := auth.HashPIN("482913")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.ExecContext(t.Context(),
		`INSERT INTO users(id,username,display_name,pin_hash,role,is_active) VALUES('mgr1','mgr1','Manager One',?,'manager',1)`, hash); err != nil {
		t.Fatal(err)
	}

	makeReq := func(form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/settings/exit-to-os", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, cashUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Blank PIN: 403, hook never called.
	if rec := makeReq(url.Values{}); rec.Code != http.StatusForbidden {
		t.Fatalf("blank PIN = %d, want 403", rec.Code)
	}
	if wc.called {
		t.Fatal("blank PIN must not call the hook")
	}

	// Wrong PIN: 403, hook never called.
	if rec := makeReq(url.Values{"manager_pin": {"000000"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong PIN = %d, want 403", rec.Code)
	}
	if wc.called {
		t.Fatal("wrong PIN must not call the hook")
	}

	// ut-docs#616: neither rejected attempt above should have left an
	// audit-log entry — a failed/blank PIN authorizes nothing to record.
	if n := auditCount(t, d.Db, "exit_to_os"); n != 0 {
		t.Fatalf("audit_log has %d exit_to_os entries before any successful attempt, want 0", n)
	}

	// Correct manager PIN: 204, hook called.
	if rec := makeReq(url.Values{"manager_pin": {"482913"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("correct manager PIN = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if !wc.called {
		t.Fatal("correct manager PIN must call the hook")
	}

	// ut-docs#616: a successful exit-to-os records who authorized it.
	var actorID string
	if err := d.Db.QueryRow(`SELECT actor_id FROM audit_log WHERE action='exit_to_os'`).Scan(&actorID); err != nil {
		t.Fatalf("exit_to_os audit entry not found: %v", err)
	}
	if actorID != "mgr1" {
		t.Fatalf("exit_to_os audit actor_id = %q, want %q (the authorizing manager)", actorID, "mgr1")
	}
}

// TestExitToOSEndpoint_NilWindowCtlDoesNotPanic: newFullAuthDeps (like most
// test-Deps helpers predating ut-docs#608) doesn't set WindowCtl, and
// nothing forces every future caller to remember it — same convention as
// Deps.OrderStatus (deps.go), whose handlers nil-check rather than trust
// every construction site. Exercises the handler's fallback to
// common.NoopWindowController with a real manager PIN, not just a build-time
// nil check.
func TestExitToOSEndpoint_NilWindowCtlDoesNotPanic(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, _, d := newFullAuthDeps(t)
	if d.WindowCtl != nil {
		t.Fatal("test assumes newFullAuthDeps leaves WindowCtl unset")
	}

	hash, err := auth.HashPIN("482913")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.ExecContext(t.Context(),
		`INSERT INTO users(id,username,display_name,pin_hash,role,is_active) VALUES('mgr1','mgr1','Manager One',?,'manager',1)`, hash); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/settings/exit-to-os",
		strings.NewReader(url.Values{"manager_pin": {"482913"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, cashUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req) // must not panic
	if rec.Code != http.StatusNoContent {
		t.Fatalf("nil WindowCtl, correct PIN = %d, want 204 (fallback to NoopWindowController): %s", rec.Code, rec.Body.String())
	}
}

// TestExitToOSBlankPINRejectedWithoutBurningLockoutBudget mirrors
// shifts_api_test.go's TestRecordCashAdjustment_BlankManagerPINRejectedWithoutBurningLockoutBudget:
// the blank-PIN pre-check exists specifically so a blank submission never
// reaches AuthorizeManager, which would otherwise burn a failed-attempt
// count shared device-wide with keypad login (5 failures = 30s lockout).
// A single blank attempt can't distinguish "pre-checked" from "checked and
// happened to 403 anyway" — this test sends one MORE than the lockout
// budget, then proves a correct PIN still works immediately after.
func TestExitToOSBlankPINRejectedWithoutBurningLockoutBudget(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, _, d := newFullAuthDeps(t)
	wc := &recordingWindowController{}
	d.WindowCtl = wc

	hash, err := auth.HashPIN("482913")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.ExecContext(t.Context(),
		`INSERT INTO users(id,username,display_name,pin_hash,role,is_active) VALUES('mgr1','mgr1','Manager One',?,'manager',1)`, hash); err != nil {
		t.Fatal(err)
	}

	makeReq := func(form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/settings/exit-to-os", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, cashUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// 6 blank-PIN submissions — one more than the device-wide 5-failure
	// lockout budget. Every one must be a plain 403, never 429.
	for i := 0; i < 6; i++ {
		if rec := makeReq(url.Values{}); rec.Code != http.StatusForbidden {
			t.Fatalf("blank PIN attempt %d: expected 403, got %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}

	// The correct PIN must still work immediately — proof the blank
	// attempts never touched the lockout counter.
	if rec := makeReq(url.Values{"manager_pin": {"482913"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 with the correct PIN after blank attempts, got %d: %s", rec.Code, rec.Body.String())
	}
	if !wc.called {
		t.Fatal("correct manager PIN must call the hook")
	}
}

// Telemetry opt-in is manager-gated and stored as a string flag.
func TestTelemetryEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	// ut-docs#865: elevation prompt, not a flat 403 (same as idle-lock).
	if rec := postForm(mux, "/api/settings/telemetry", url.Values{"optIn": {"on"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier telemetry = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
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

// UI scale / theme are ungated per-till display preferences; save/upsert are
// manager-gated (ut-docs#179) store-wide settings. All validate and reflect
// into runtime state for a manager.
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
	// settings page's generic key/value editor. Manager-gated (ut-docs#179).
	rec := postForm(mux, "/api/settings/save", url.Values{
		"currency":   {"EUR"},
		"country":    {"DE"},
		"taxRatePct": {"19"},
	}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("save = %d", rec.Code)
	}
	st := d.CurrentState()
	if st.Currency != "EUR" || st.Country != "DE" || st.TaxRatePct != 19 {
		t.Fatalf("save not applied: %+v", st)
	}
	// ut-docs#970 review (F2): this is the handler the shipped currency card
	// actually posts to — an earlier attempt marked confirmation in
	// /api/settings/upsert's generic key/value switch instead, which the
	// shipped UI never calls, so an operator using Settings normally stayed
	// gated on their next import. Regression coverage for the fix.
	if confirmed, ok, err := d.Settings.Get(t.Context(), common.KeyCurrencyConfirmed); err != nil || !ok || confirmed != "true" {
		t.Fatalf("currency_confirmed = (%q, %v, %v), want (true, true, nil) after /api/settings/save sets a currency", confirmed, ok, err)
	}

	// Upsert: empty key is a 400; a known key reflects into state.
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"value": {"x"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty key = %d, want 400", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.tax_inclusive"}, "value": {"true"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("upsert = %d", rec.Code)
	}
	if !d.CurrentState().TaxInclusive {
		t.Fatal("upsert did not reflect store.tax_inclusive=true into state")
	}
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"pos.allow_negative_inventory"}, "value": {"true"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("upsert = %d", rec.Code)
	}
	if !d.CurrentState().AllowNegativeInventory {
		t.Fatal("upsert did not reflect pos.allow_negative_inventory=true into state")
	}
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"pos.allow_negative_inventory"}, "value": {"false"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("upsert = %d", rec.Code)
	}
	if d.CurrentState().AllowNegativeInventory {
		t.Fatal("upsert did not reflect pos.allow_negative_inventory=false into state")
	}
	if v, _, _ := d.Settings.Get(t.Context(), "pos.allow_negative_inventory"); v != "false" {
		t.Fatalf("stored allow_negative = %q", v)
	}
}

// ut-docs#244: the service charge rate upsert accepts fractional percents
// (12.5% is the standard UK rate) and rejects an invalid value with a 400
// instead of silently no-op'ing — and, critically, never persists the
// invalid value to the settings store in the first place.
func TestServiceChargeRateUpsertEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.service_charge_rate_pct"}, "value": {"12.5"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("valid fractional rate = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := d.CurrentState().ServiceChargeRateBasisPoints; got != 1250 {
		t.Fatalf("ServiceChargeRateBasisPoints = %d, want 1250", got)
	}
	if v, _, _ := d.Settings.Get(t.Context(), "store.service_charge_rate_pct"); v != "12.5" {
		t.Fatalf("stored service charge rate = %q, want %q", v, "12.5")
	}

	for _, bad := range []string{"abc", "-5", "NaN", "Inf", "Infinity"} {
		rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.service_charge_rate_pct"}, "value": {bad}}, &mgrUser)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid rate %q = %d, want 400", bad, rec.Code)
		}
		// Must not have overwritten the prior valid value in either the
		// live state or the settings store.
		if got := d.CurrentState().ServiceChargeRateBasisPoints; got != 1250 {
			t.Fatalf("after rejected upsert %q: ServiceChargeRateBasisPoints = %d, want unchanged 1250", bad, got)
		}
		if v, _, _ := d.Settings.Get(t.Context(), "store.service_charge_rate_pct"); v != "12.5" {
			t.Fatalf("after rejected upsert %q: stored service charge rate = %q, want unchanged %q", bad, v, "12.5")
		}
	}
}

// ut-docs#962: Turkey's 2026-01-30 Fiyat Etiketi Yönetmeliği amendment
// makes a service-charge/cover line illegal on any bill, so a TR-configured
// shop must not be able to save a nonzero rate at all — refused with a 400
// and a localized explanation, not silently accepted. A zero rate (turning
// the setting back off) must still be allowed for a TR shop.
func TestServiceChargeRateUpsertEndpoint_TurkeyForbidsNonzeroRate(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	d.UpdateState(func(s *common.RuntimeState) { s.Country = "TR" })

	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.service_charge_rate_pct"}, "value": {"12.5"}}, &mgrUser)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("nonzero rate for TR = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := d.CurrentState().ServiceChargeRateBasisPoints; got != 0 {
		t.Fatalf("ServiceChargeRateBasisPoints after rejected TR upsert = %d, want unchanged 0", got)
	}
	if v, _, _ := d.Settings.Get(t.Context(), "store.service_charge_rate_pct"); v != "" {
		t.Fatalf("stored service charge rate after rejected TR upsert = %q, want unset", v)
	}

	// Explicitly zeroing it back out stays allowed.
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.service_charge_rate_pct"}, "value": {"0"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("zero rate for TR = %d, want 204: %s", rec.Code, rec.Body.String())
	}
}

// ut-docs#962: switching the shop's country TO Turkey must drop the live
// engines' service-charge rate immediately, not only after a restart.
// /api/settings/upsert reflects store.country into the runtime state but
// used to re-push pos.Config only for the currency/tax-inclusive/rate keys,
// so a GB shop with 12.5% configured that became a TR shop kept quoting an
// illegal service-charge line on the basket (and the customer-facing
// display) until the process restarted — while the tender path already
// recomputed it as 0, so the screen and the recorded sale disagreed too.
// Both engines, because the kiosk basket is a separate instance (ADR-0020).
func TestCountryUpsertToTurkeyClearsLiveServiceChargeRate(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.service_charge_rate_pct"}, "value": {"12.5"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("seed rate = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := d.Engine.Config().ServiceChargeRateBasisPoints; got != 1250 {
		t.Fatalf("engine rate before country change = %d, want 1250", got)
	}

	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.country"}, "value": {"TR"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("country upsert = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := d.Engine.Config().ServiceChargeRateBasisPoints; got != 0 {
		t.Fatalf("cashier engine rate after switching to TR = %d, want 0", got)
	}
	if d.KioskEngine != nil {
		if got := d.KioskEngine.Config().ServiceChargeRateBasisPoints; got != 0 {
			t.Fatalf("kiosk engine rate after switching to TR = %d, want 0", got)
		}
	}

	// And back out again: leaving TR restores the still-stored rate, so the
	// zeroing is a country-scoped suppression, not a destructive erase.
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.country"}, "value": {"GB"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("country upsert back to GB = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := d.Engine.Config().ServiceChargeRateBasisPoints; got != 1250 {
		t.Fatalf("engine rate after leaving TR = %d, want the still-configured 1250", got)
	}
}

// ut-docs#962: the TR suppression is a fail-closed compliance backstop, so
// it must not hinge on the exact casing of store.country. The wizard
// persists uppercase, but /api/settings/upsert, a restored backup or a
// hand-edited DB row can all carry "tr" — and unlike internal/fiscal's
// deliberately strict "DE" match (where a loose match would BLOCK a sale),
// a loose match here can only remove a line that is illegal to print, so
// leniency is the safe direction.
func TestServiceChargeRateUpsertEndpoint_TurkeyMatchIsCaseInsensitive(t *testing.T) {
	for _, code := range []string{"tr", "Tr", " TR "} {
		t.Run(code, func(t *testing.T) {
			mux, _, d := newFullAuthDeps(t)
			d.UpdateState(func(s *common.RuntimeState) { s.Country = code })
			rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.service_charge_rate_pct"}, "value": {"12.5"}}, &mgrUser)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("nonzero rate for country %q = %d, want 400: %s", code, rec.Code, rec.Body.String())
			}
		})
	}
}

// The Settings page's dedicated till-name field (ut-docs#396) persists under
// till.name — distinct from a replica's own sync.till_name — and is
// manager-gated the same way as /api/settings/display-mode.
func TestTillNameEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	// ut-docs#796: a denied cashier gets the elevation prompt (200), not a
	// flat 403 — and nothing is written.
	if rec := postForm(mux, "/api/settings/till-name", url.Values{"name": {"Front Register"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier till-name = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	if _, ok, _ := d.Settings.Get(t.Context(), "till.name"); ok {
		t.Fatal("cashier's denied till-name attempt must not have written till.name")
	}
	if rec := postForm(mux, "/api/settings/till-name", url.Values{"name": {"Front Register"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("manager till-name = %d, want 204", rec.Code)
	}
	if v, ok, _ := d.Settings.Get(t.Context(), "till.name"); !ok || v != "Front Register" {
		t.Fatalf("till.name = %q ok=%v, want %q", v, ok, "Front Register")
	}
}

// The Settings page's till-name field only makes sense on the primary —
// till.name isn't read anywhere on a replica (its own identity is
// sync.till_name, set at join time), so showing an editable field there
// would be a dead control: a manager could save a name that changes
// nothing anywhere in the product (independent review, ut-docs#396).
func TestSettingsPage_TillNameFieldOnlyOnPrimary(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

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

	if body := get(); !strings.Contains(body, `/api/settings/till-name`) {
		t.Fatalf("primary till: expected the till-name field, got:\n%s", body)
	}

	if err := d.Settings.Set(t.Context(), "sync.primary_url", "https://primary.local"); err != nil {
		t.Fatalf("set sync.primary_url: %v", err)
	}
	if body := get(); strings.Contains(body, `/api/settings/till-name`) {
		t.Fatalf("replica: till-name field should be hidden, got:\n%s", body)
	}
}

// The Settings page's till-register picker (ut-docs#268) persists this
// till's own register identity under till.register_id — the register a
// shift-scoped write (e.g. a Pfandrückgabe payout) resolves against.
// Manager-gated like till-name, and an id that isn't an active register is
// rejected rather than persisted.
func TestTillRegisterEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	for _, ins := range []string{
		`INSERT INTO registers(id,name,is_active) VALUES('regA','Front Till',1)`,
		`INSERT INTO registers(id,name,is_active) VALUES('regB','Back Till',1)`,
		`INSERT INTO registers(id,name,is_active) VALUES('reg-old','Retired Till',0)`,
	} {
		if _, err := d.Db.Exec(ins); err != nil {
			t.Fatal(err)
		}
	}

	// ut-docs#796: a denied cashier gets the elevation prompt (200), not a
	// flat 403 — and nothing is written.
	if rec := postForm(mux, "/api/settings/till-register", url.Values{"register_id": {"regA"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier till-register = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	if _, ok, _ := d.Settings.Get(t.Context(), pos.SettingsKeyTillRegisterID); ok {
		t.Fatal("cashier's denied till-register attempt must not have written till.register_id")
	}
	if rec := postForm(mux, "/api/settings/till-register", url.Values{}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing register_id = %d, want 400", rec.Code)
	}
	// Not a register at all, and an inactive one: both rejected, nothing
	// persisted — garbage here would silently misroute payouts later.
	for _, bad := range []string{"no-such-register", "reg-old"} {
		if rec := postForm(mux, "/api/settings/till-register", url.Values{"register_id": {bad}}, &mgrUser); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid register %q = %d, want 400", bad, rec.Code)
		}
		if v, ok, _ := d.Settings.Get(t.Context(), pos.SettingsKeyTillRegisterID); ok {
			t.Fatalf("invalid register %q persisted as %q", bad, v)
		}
	}

	if rec := postForm(mux, "/api/settings/till-register", url.Values{"register_id": {"regB"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("manager till-register = %d, want 204", rec.Code)
	}
	if v, ok, _ := d.Settings.Get(t.Context(), pos.SettingsKeyTillRegisterID); !ok || v != "regB" {
		t.Fatalf("till.register_id = %q ok=%v, want regB", v, ok)
	}
}

// The register picker renders in the Tills card whether or not this till is
// the primary — unlike the till-name field, every till (primary or replica)
// processes its own local payouts, so register identity matters on all of
// them (ut-docs#268). With nothing persisted on a multi-register shop the
// picker renders unselected; once set, the chosen register is selected.
func TestSettingsPage_TillRegisterPickerRendersAndSelects(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	for _, ins := range []string{
		`INSERT INTO registers(id,name,is_active) VALUES('regA','Front Till',1)`,
		`INSERT INTO registers(id,name,is_active) VALUES('regB','Back Till',1)`,
	} {
		if _, err := d.Db.Exec(ins); err != nil {
			t.Fatal(err)
		}
	}

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

	// Ambiguous (two registers, nothing persisted): the picker still
	// renders — that's exactly when a manager needs it — with no option
	// selected.
	body := get()
	if !strings.Contains(body, `/api/settings/till-register`) {
		t.Fatalf("expected the till-register picker, got:\n%s", body)
	}
	if !strings.Contains(body, `value="regA"`) || !strings.Contains(body, `value="regB"`) {
		t.Fatalf("expected both registers as options, got:\n%s", body)
	}
	if strings.Contains(body, `value="regA" selected`) || strings.Contains(body, `value="regB" selected`) {
		t.Fatalf("expected no register pre-selected while unset, got:\n%s", body)
	}

	if err := d.Settings.Set(t.Context(), pos.SettingsKeyTillRegisterID, "regB"); err != nil {
		t.Fatal(err)
	}
	if body := get(); !strings.Contains(body, `value="regB" selected`) {
		t.Fatalf("expected regB selected after persisting it, got:\n%s", body)
	}

	// A replica (sync.primary_url set) keeps the picker — no
	// .IsPrimaryTill gate here, unlike till-name.
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "https://primary.local"); err != nil {
		t.Fatal(err)
	}
	if body := get(); !strings.Contains(body, `/api/settings/till-register`) {
		t.Fatalf("replica: expected the till-register picker to still render, got:\n%s", body)
	}
}

// ut-docs#698: a batch still inside its retention window must show the
// retained-until date and NOT offer the Delete-permanently confirm flow, so
// an operator never types PURGE and confirms only to be refused; a batch
// outside the window (or with no sales at all) must offer the control
// exactly as before.
func TestSettingsPage_ResetArchivesShowsPurgeEligibility(t *testing.T) {
	// newFullAuthDeps' hand-built fixture schema has no reset_batches table
	// (it's not a real migrated DB) -- newRealDBDeps (demo_seed_opt_in_test.go)
	// is, and already registers registerSettings.
	mux, d := newRealDBDeps(t)
	now := time.Now().UTC().Format(time.RFC3339)
	// Purgeable: no trading history at all -- DeleteResetBatch's own
	// no-sales carve-out, mirrored here.
	if _, err := d.Db.Exec(`INSERT INTO reset_batches (id, created_at, sales_count) VALUES ('b-purgeable', ?, 0)`, now); err != nil {
		t.Fatal(err)
	}
	// Not purgeable: real sales, archived "now" -- well inside any real
	// country's retention window (or the global floor with none configured).
	if _, err := d.Db.Exec(`INSERT INTO reset_batches (id, created_at, sales_count) VALUES ('b-gated', ?, 3)`, now); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, `data-purge-batch="b-purgeable"`) {
		t.Fatalf("a batch with no trading history must still offer Delete-permanently, got:\n%s", body)
	}
	if strings.Contains(body, `data-purge-batch="b-gated"`) {
		t.Fatalf("a batch within its retention window must not offer the Delete-permanently control, got:\n%s", body)
	}
	// Both rows keep their (unrelated) restore controls -- restore is
	// out of scope for this card.
	if !strings.Contains(body, `data-restore-batch="b-purgeable"`) || !strings.Contains(body, `data-restore-batch="b-gated"`) {
		t.Fatalf("restore controls must stay untouched on every row, got:\n%s", body)
	}
	if !strings.Contains(body, "Retained until") {
		t.Fatalf("gated row must show a retained-until message, got:\n%s", body)
	}
}

// ut-docs#553: the printer/kitchen-printer address fields hold technical
// LTR strings (host:port, a device path) that render corrupted/right-
// truncated under an RTL locale unless force-directioned, the same bug
// class independently caught and fixed on /kitchen-stations (ut-docs#516).
// This regression test would have failed against the pre-fix markup, which
// had no dir="ltr" on any of the three inputs.
func TestSettingsPage_PrinterAddressFieldsAreLTR(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		`name="address" value="" placeholder="192.168.1.50:9100" dir="ltr"`,
		`name="device" value="" placeholder="/dev/usb/lp0" dir="ltr"`,
		`name="kitchenAddr" value="" placeholder="192.168.1.60:9100" dir="ltr"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected printer field with dir=\"ltr\": %s\ngot:\n%s", want, body)
		}
	}
}

// A cashier (and an unauthenticated/no-session request) is refused on both
// mutating settings endpoints (ut-docs#179 — /save and /upsert were the two
// exceptions that had none). Since ut-docs#796 the refusal is the in-place
// elevation prompt (200 with the dialog, nothing written) rather than a
// flat 403.
func TestSaveAndUpsertSettings_RequireManager(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	for _, tc := range []struct {
		name string
		user *auth.User
	}{
		{"no session", nil},
		{"cashier", &cashUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postForm(mux, "/api/settings/save", url.Values{"currency": {"GBP"}}, tc.user)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") ||
				!strings.Contains(rec.Body.String(), `name="override_pin"`) {
				t.Fatalf("save = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
			}
			rec = postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.tax_inclusive"}, "value": {"true"}}, tc.user)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") ||
				!strings.Contains(rec.Body.String(), `name="override_pin"`) {
				t.Fatalf("upsert = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
			}
		})
	}

	// Neither refused call actually wrote anything.
	if d.CurrentState().Currency == "GBP" {
		t.Fatal("cashier/no-session save must not have changed currency")
	}
	if v, _, _ := d.Settings.Get(t.Context(), "store.tax_inclusive"); v == "true" {
		t.Fatal("cashier/no-session upsert must not have written store.tax_inclusive")
	}

	// A manager still succeeds (sanity check the gate isn't fail-closed for everyone).
	if rec := postForm(mux, "/api/settings/save", url.Values{"currency": {"GBP"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("manager save = %d, want 204", rec.Code)
	}
}

// The template half of the fix (ut-docs#179 review finding, narrowed by
// ut-docs#867): a cashier's rendered /settings page must not contain the raw
// key/value table — it's an unbounded browser over the whole settings store
// with no cashier use case, deliberately kept manager-only even though its
// endpoint is elevation-wired. The currency card, by contrast, IS visible to
// a cashier since ut-docs#867: its POST goes through checkOrElevate, so the
// in-place PIN dialog — not template hiding — is the authorization layer
// (see TestSettingsPage_ElevationWiredFormsVisibleToCashier).
func TestSettingsPage_HidesManagerOnlyCardsFromCashier(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	get := func(user *auth.User) string {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		if user != nil {
			req = auth.WithUser(req, *user)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}

	cashierHTML := get(&cashUser)
	if strings.Contains(cashierHTML, `id="new-setting"`) {
		t.Fatal("cashier sees the raw settings.all key/value table")
	}
	if !strings.Contains(cashierHTML, `name="currency"`) {
		t.Fatal("cashier should see the elevation-wired currency card (ut-docs#867)")
	}

	managerHTML := get(&mgrUser)
	if !strings.Contains(managerHTML, `id="new-setting"`) {
		t.Fatal("manager should see the raw settings.all key/value table")
	}
	if !strings.Contains(managerHTML, `name="currency"`) {
		t.Fatal("manager should see the currency card")
	}

	// super_admin (ut-docs#710): the "isManager" template flag now comes from
	// canPerform(d, r, "settings") instead of isManagerOrAuthOff, which never
	// recognized super_admin (User.IsManager() only checks manager/admin) —
	// this is the real broadening that swap brings. A super_admin session
	// must see exactly what a manager sees.
	superAdminHTML := get(&auth.User{ID: "sa1", Role: "super_admin", DisplayName: "Super"})
	if !strings.Contains(superAdminHTML, `id="new-setting"`) {
		t.Fatal("super_admin should see the raw settings.all key/value table")
	}
	if !strings.Contains(superAdminHTML, `name="currency"`) {
		t.Fatal("super_admin should see the currency card")
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
	rec := postForm(mux, "/api/settings/save", url.Values{"currency": {"GBP"}}, &mgrUser)
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

// TestSaveSettings_Locale (ut-docs#861): a shop's default locale is settable
// via the shipped Settings Language card (same handler currency/country
// already use), applies live (no restart, no second InitI18n call), and
// persists across a boot-time reload from the settings store — exactly the
// gap the card was filed for (previously only UT_DEFAULT_LOCALE at install
// time could change this).
func TestSaveSettings_Locale(t *testing.T) {
	mux, _, d := newFullAuthDeps(t) // already calls initAuthTestI18n -> httpx.InitI18n(realBundle, "en")

	if got := httpx.DefaultLocale(); got != "en" {
		t.Fatalf("DefaultLocale before save = %q, want en", got)
	}

	rec := postForm(mux, "/api/settings/save", url.Values{"locale": {"ar"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("save = %d", rec.Code)
	}
	if got := httpx.DefaultLocale(); got != "ar" {
		t.Fatalf("DefaultLocale after save = %q, want ar (live apply, no restart)", got)
	}
	if v, ok, err := d.Settings.Get(t.Context(), "store.locale"); err != nil || !ok || v != "ar" {
		t.Fatalf("stored store.locale = (%q, %v, %v), want (ar, true, nil)", v, ok, err)
	}

	// Reloading from the store (what a real boot does) must see the same
	// value — this is the "no redeploy needed" acceptance criterion.
	reloaded := common.LoadState(t.Context(), d.Settings, &config.Config{})
	if reloaded.Locale != "ar" {
		t.Fatalf("LoadState after save: Locale = %q, want ar", reloaded.Locale)
	}
}

// TestSaveSettings_LocaleRejectsUnknownValue: an unrecognized locale is
// silently skipped (same lenient contract this handler already applies to
// every other field — see the handler's own "no rejecting validation"
// comment), never stored or applied — an unknown locale would make T() fall
// back to raw keys sitewide for anything with no request to resolve a
// per-browser preference from (background jobs, notification email).
func TestSaveSettings_LocaleRejectsUnknownValue(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	rec := postForm(mux, "/api/settings/save", url.Values{"locale": {"xx-not-real"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("save = %d", rec.Code)
	}
	if got := httpx.DefaultLocale(); got != "en" {
		t.Fatalf("DefaultLocale after unknown-locale save = %q, want unchanged en", got)
	}
	// SaveState always re-persists the full RuntimeState snapshot (by
	// design — see its own doc comment), so store.locale existing isn't
	// itself a failure; what must never happen is the REJECTED value
	// landing there.
	if v, _, _ := d.Settings.Get(t.Context(), "store.locale"); v == "xx-not-real" {
		t.Fatalf("unknown locale value %q was persisted to store.locale, want rejected", v)
	}
}

// TestUpsertLocale_ReflectsIntoStateAndSurvivesLaterSave (ut-docs#861 review
// finding F2): SaveState now unconditionally re-persists KeyLocale on every
// call (this card's own change), but until this fix the raw upsert editor's
// reflect-into-state switch had no case for it — an operator editing
// store.locale via Settings' All-settings table saw their edit silently
// reverted by the very next /api/settings/save from ANY other card (that
// handler always writes back CurrentState().Locale, which stayed stale).
// Same class of bug as ut-docs#178.
func TestUpsertLocale_ReflectsIntoStateAndSurvivesLaterSave(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.locale"}, "value": {"ar"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("upsert = %d", rec.Code)
	}
	if got := d.CurrentState().Locale; got != "ar" {
		t.Fatalf("CurrentState().Locale after upsert = %q, want ar (reflect-into-state case missing)", got)
	}
	if got := httpx.DefaultLocale(); got != "ar" {
		t.Fatalf("DefaultLocale after upsert = %q, want ar (live-apply missing)", got)
	}

	// An unrelated save from another card (currency) must NOT revert it —
	// this is the actual regression: SaveState always writes back whatever
	// CurrentState().Locale currently is.
	if rec := postForm(mux, "/api/settings/save", url.Values{"currency": {"EUR"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("save = %d", rec.Code)
	}
	if got := d.CurrentState().Locale; got != "ar" {
		t.Fatalf("Locale after an unrelated currency save = %q, want unchanged ar (silently reverted)", got)
	}
	if got, _, _ := d.Settings.Get(t.Context(), "store.locale"); got != "ar" {
		t.Fatalf("stored store.locale after unrelated save = %q, want unchanged ar", got)
	}

	// Invalid value via the raw editor: persisted raw (this table's
	// established freedom, same as currency/country get) but NOT reflected
	// into live state / DefaultLocale() — matches the shipped Language
	// card's validation, which the raw editor must not be a way around.
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.locale"}, "value": {"xx-not-real"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("upsert invalid = %d", rec.Code)
	}
	if got := httpx.DefaultLocale(); got != "ar" {
		t.Fatalf("DefaultLocale after invalid upsert = %q, want unchanged ar", got)
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

// SaveState's write must be all-or-nothing at the HTTP boundary too
// (ut-docs#157): a failed settings-page save must answer 5xx and must not
// apply the change to the live sale engine, or a shop would silently
// mis-price sales on the old currency/tax combination.
func TestSettingsSave_FailsClosedOnSaveError(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	// Seed a known-good baseline the same way a real shop would have one.
	if rec := postForm(mux, "/api/settings/save", url.Values{
		"currency":   {"GBP"},
		"taxRatePct": {"20"},
	}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("seed save = %d", rec.Code)
	}

	if _, err := d.Db.Exec(`
CREATE TRIGGER boom BEFORE INSERT ON settings
WHEN NEW.key = 'store.tax_rate'
BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rec := postForm(mux, "/api/settings/save", url.Values{
		"currency":   {"EUR"},
		"taxRatePct": {"7"},
	}, &mgrUser)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("save with an aborting trigger = %d, want 500", rec.Code)
	}

	// The DB must still hold the seeded values — no partial currency-only write.
	if v, _, _ := d.Settings.Get(t.Context(), "store.currency"); v != "GBP" {
		t.Fatalf("store.currency = %q after failed save, want seeded %q (partial write not rolled back)", v, "GBP")
	}
	if v, _, _ := d.Settings.Get(t.Context(), "store.tax_rate"); v != "20" {
		t.Fatalf("store.tax_rate = %q after failed save, want seeded %q", v, "20")
	}
	// The live sale engine must not have picked up the failed-to-persist
	// currency/tax combination.
	if d.Engine.Config().TaxRateBasisPoints != 2000 {
		t.Fatalf("engine TaxRateBasisPoints = %d after failed save, want unchanged 2000 (700 would mean the failed 7%% rate was silently applied)", d.Engine.Config().TaxRateBasisPoints)
	}
}

// A rejected save must not leak into the in-memory state either — otherwise
// a later, unrelated successful save silently re-persists the change the
// operator was just told failed (ut-docs#157 review finding).
func TestSettingsSave_FailedSaveDoesNotLeakIntoLaterSave(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if rec := postForm(mux, "/api/settings/save", url.Values{"currency": {"GBP"}, "taxRatePct": {"20"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("seed save = %d", rec.Code)
	}

	if _, err := d.Db.Exec(`
CREATE TRIGGER boom2 BEFORE INSERT ON settings
WHEN NEW.key = 'store.tax_rate'
BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rec := postForm(mux, "/api/settings/save", url.Values{"currency": {"EUR"}, "taxRatePct": {"7"}}, &mgrUser)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed save = %d, want 500", rec.Code)
	}
	if d.CurrentState().Currency != "GBP" {
		t.Fatalf("in-memory currency = %q after failed save, want unchanged %q (leaked into in-memory state)", d.CurrentState().Currency, "GBP")
	}

	if _, err := d.Db.Exec(`DROP TRIGGER boom2`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	// An unrelated, otherwise-successful save must not silently persist the
	// previously-rejected currency/tax-rate change.
	if rec := postForm(mux, "/api/settings/ui-scale", url.Values{"scale": {"1.5"}}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("unrelated ui-scale save = %d", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), "store.currency"); v != "GBP" {
		t.Fatalf("store.currency = %q after an unrelated save, want still %q (rejected change leaked)", v, "GBP")
	}
	if v, _, _ := d.Settings.Get(t.Context(), "store.tax_rate"); v != "20" {
		t.Fatalf("store.tax_rate = %q after an unrelated save, want still %q", v, "20")
	}
	if d.Engine.Config().TaxRateBasisPoints != 2000 {
		t.Fatalf("engine TaxRateBasisPoints = %d after an unrelated save, want unchanged 2000", d.Engine.Config().TaxRateBasisPoints)
	}
}

// GET /api/enrol/devices is read-only and stays on the flat canPerform gate
// (out of ut-docs#865's scope, same as ut-docs#796's own non-goals) — a
// denied cashier still gets the 200 HTMX swap target with a forbidden/muted
// notice, never a hard status.
func TestEnrolDevicesEndpointRefusesNonManager(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/enrol/devices", nil), cashUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/enrol/devices: code=%d, want 200 (HTMX swap target)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="error"`) && !strings.Contains(body, `class="muted"`) {
		t.Fatalf("GET /api/enrol/devices did not render a forbidden notice: %s", body)
	}
}

// ut-docs#865: claim-code and enrol/now moved off the flat canPerform gate
// onto checkOrElevate (#557/#796 mechanism) — a denied cashier now gets the
// in-place elevation prompt, not the flat forbidden span.
func TestEnrolClaimCodeAndNow_RefuseNonManagerViaElevation(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	for _, path := range []string{"/api/enrol/claim-code", "/api/enrol/now"} {
		req := auth.WithUser(httptest.NewRequest(http.MethodPost, path, nil), cashUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") ||
			!strings.Contains(rec.Body.String(), `name="override_pin"`) {
			t.Fatalf("POST %s: code=%d body=%s, want 200 with the elevation prompt", path, rec.Code, rec.Body.String())
		}
	}
}

// settingsForbiddenText is the exact localized string every HTMX-style
// (200-with-error-span) settings endpoint renders when canPerform() denies
// the request — httpx.T(locale, "settings.enrol.forbidden") in
// web/locales/en.json. Used below to tell "denied" apart from "past the
// gate but failed downstream for an unrelated reason" on those endpoints,
// which never answer a hard 403.
const settingsForbiddenText = "Only a manager or admin can register this till."

// TestSettingsEndpoints_RoleMatrix is ut-docs#710's role-matrix proof: every
// isManagerOrAuthOff site this card moved onto canPerform(d, r, "settings")
// (19 handler gates + the GET /settings "isManager" template flag covered
// separately by TestSettingsPage_HidesManagerOnlyCardsFromCashier) denies a
// cashier and lets manager/admin/super_admin past the auth gate — same
// table-driven role-matrix convention as #706's
// TestPluginManagementEndpoints_RealSessionGatesByRole and #707's
// TestDataManagementEndpoints_RealSessionGatesByRole
// (data_backup_manager_gate_test.go). super_admin is the row that actually
// documents canPerform()'s real broadening over the old isManagerOrAuthOff
// gate (User.IsManager() never recognized super_admin) — written to fail
// against the pre-#710 gate (a super_admin session got 403/the forbidden
// span everywhere, same as a cashier) and confirmed to pass once every site
// in settings_page.go was switched.
//
// Denied behavior comes in three flavors since ut-docs#796/#865:
//   - gate403: a hard 403 (the not-yet-elevation-wired plain handlers —
//     none remain in this file as of ut-docs#865; kept as a gateKind since
//     it's still the right shape for a future not-yet-wired site).
//   - gateForbiddenSpan: an HTMX swap target that always answers 200 with
//     the localized forbidden text in an error/muted span (HTMX drops
//     non-2xx bodies) — GET /api/enrol/devices (read-only), still on the
//     flat gate.
//   - gateElevation: the handlers wired to checkOrElevate (ut-docs#796's
//     original 8, plus #865's 10) — a denied cashier gets 200 with the
//     in-place elevation prompt dialog (neither the forbidden text nor a
//     403).
//
// "Past the gate" for manager/admin/super_admin means downstream processing
// was reached ON THEIR OWN SESSION — no PIN involved (an already-authorized
// user hits checkOrElevate's allowed branch, never needsElevation) — not
// that it necessarily succeeded (e.g. enrol/now still fails offline-first
// with no reachable marketplace, which is fine — this test only proves the
// auth gate itself).
func TestSettingsEndpoints_RoleMatrix(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if _, err := d.Db.Exec(`INSERT INTO registers(id,name,is_active) VALUES('regA','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	// ut-docs#868: dismiss-pending-base-plugin now validates canonical_type/
	// locale against the pending list before checkOrElevate, so this row's
	// {"canonical_type": {"x"}} (locale unset -> "") must actually be
	// pending, or every case below sees the new 400 instead of the gate
	// this test is about.
	if err := savePendingBasePlugins(t.Context(), d, []basePluginSpec{{CanonicalType: "x", Locale: ""}}); err != nil {
		t.Fatal(err)
	}

	type gateKind int
	const (
		gate403 gateKind = iota
		gateForbiddenSpan
		gateElevation
	)

	type matrixCase struct {
		name, method, path string
		form               url.Values
		gate               gateKind
	}

	cases := []matrixCase{
		{"payments-default", http.MethodPost, "/api/settings/payments-default", url.Values{"method": {"cash"}}, gateElevation},
		{"payments-fee", http.MethodPost, "/api/settings/payments-fee", url.Values{"method": {"cash"}, "percent": {"1"}}, gateElevation},
		{"enrol-claim-code", http.MethodPost, "/api/enrol/claim-code", nil, gateElevation},
		{"enrol-now", http.MethodPost, "/api/enrol/now", nil, gateElevation},
		{"enrol-devices", http.MethodGet, "/api/enrol/devices", nil, gateForbiddenSpan},
		{"idle-lock", http.MethodPost, "/api/settings/idle-lock", url.Values{"minutes": {"10"}}, gateElevation},
		{"kiosk-idle-reset", http.MethodPost, "/api/settings/kiosk-idle-reset", url.Values{"seconds": {"30"}}, gateElevation},
		{"window-mode", http.MethodPost, "/api/settings/window-mode", url.Values{"mode": {"kiosk"}}, gateElevation},
		{"launch-on-startup", http.MethodPost, "/api/settings/launch-on-startup", url.Values{"enabled": {"true"}}, gateElevation},
		{"telemetry", http.MethodPost, "/api/settings/telemetry", url.Values{"optIn": {"on"}}, gateElevation},
		{"display-mode", http.MethodPost, "/api/settings/display-mode", url.Values{"mode": {"backoffice"}}, gateElevation},
		{"shop-type", http.MethodPost, "/api/settings/shop-type", url.Values{"shop_type": {""}}, gateElevation},
		{"remove-demo-catalogue", http.MethodPost, "/api/settings/remove-demo-catalogue", nil, gateElevation},
		{"dismiss-restore-prompt", http.MethodPost, "/api/settings/dismiss-restore-prompt", nil, gateElevation},
		{"dismiss-pending-base-plugin", http.MethodPost, "/api/settings/dismiss-pending-base-plugin", url.Values{"canonical_type": {"x"}}, gateElevation},
		{"till-name", http.MethodPost, "/api/settings/till-name", url.Values{"name": {"X"}}, gateElevation},
		{"till-register", http.MethodPost, "/api/settings/till-register", url.Values{"register_id": {"regA"}}, gateElevation},
		{"save", http.MethodPost, "/api/settings/save", url.Values{"currency": {"GBP"}}, gateElevation},
		{"upsert", http.MethodPost, "/api/settings/upsert", url.Values{"key": {"x"}, "value": {"y"}}, gateElevation},
	}

	doReq := func(tc matrixCase, u auth.User) *httptest.ResponseRecorder {
		var req *http.Request
		if tc.form != nil {
			req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req = httptest.NewRequest(tc.method, tc.path, nil)
		}
		req = auth.WithUser(req, u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	for _, tc := range cases {
		t.Run(tc.name+"/cashier_denied", func(t *testing.T) {
			rec := doReq(tc, cashUser)
			switch tc.gate {
			case gateForbiddenSpan:
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), settingsForbiddenText) {
					t.Fatalf("%s %s cashier = %d %q, want 200 with the forbidden text", tc.method, tc.path, rec.Code, rec.Body.String())
				}
			case gateElevation:
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") ||
					!strings.Contains(rec.Body.String(), `name="override_pin"`) {
					t.Fatalf("%s %s cashier = %d %q, want 200 with the elevation prompt", tc.method, tc.path, rec.Code, rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), settingsForbiddenText) {
					t.Fatalf("%s %s cashier got the OLD forbidden text alongside the prompt: %s", tc.method, tc.path, rec.Body.String())
				}
			default:
				if rec.Code != http.StatusForbidden {
					t.Fatalf("%s %s cashier = %d, want 403", tc.method, tc.path, rec.Code)
				}
			}
		})
		for _, role := range []string{"manager", "admin", "super_admin"} {
			t.Run(tc.name+"/"+role+"_past_gate", func(t *testing.T) {
				u := auth.User{ID: "u-" + role, Role: role}
				rec := doReq(tc, u)
				switch tc.gate {
				case gateForbiddenSpan:
					if strings.Contains(rec.Body.String(), settingsForbiddenText) {
						t.Fatalf("%s %s %s got the forbidden text, want past the auth gate: %s", tc.method, tc.path, role, rec.Body.String())
					}
				case gateElevation:
					// An already-authorized session hits the allowed branch
					// on its own — no 403, and crucially NO elevation prompt
					// (no PIN should ever be demanded of them).
					if rec.Code == http.StatusForbidden {
						t.Fatalf("%s %s %s = 403, want past the auth gate", tc.method, tc.path, role)
					}
					if strings.Contains(rec.Body.String(), "elevation-dialog") {
						t.Fatalf("%s %s %s was shown the elevation prompt on an already-authorized session: %s", tc.method, tc.path, role, rec.Body.String())
					}
				default:
					if rec.Code == http.StatusForbidden {
						t.Fatalf("%s %s %s = 403, want past the auth gate", tc.method, tc.path, role)
					}
				}
			})
		}
	}
}

// TestSettingsPage_ElevationWiredFormsVisibleToCashier is ut-docs#867's
// template-visibility proof. Every settings form whose POST handler goes
// through checkOrElevate (ADR-0052's in-place manager-PIN dialog,
// elevation.go) must render for a cashier too — hiding it behind
// {{ if .isManager }} (the same canPerform check elevation exists to soften)
// meant the shipped UI could never trigger the dialog at all; a denied
// cashier just never saw the form. Authorization is unchanged: the server
// still answers a denied POST with the elevation prompt.
//
// Content that is NOT elevation-wired (flat canPerform/deny handlers, the
// exit-to-os AuthorizeManager flow, real business data like backup files or
// GDPR search) stays manager-gated exactly as before, as does the raw
// key/value upsert browser (elevation-wired but deliberately excepted — an
// unbounded settings-store browser has no cashier-triggerable action to
// name in a PIN prompt).
//
// The enrollment card's enrolled branch (claim-code) can't be exercised
// here — "enrolled" reads package-global enroll.CurrentStatus() — so the
// card is covered via its unenrolled branch (/api/enrol/now), the identical
// un-gating in the same card.
func TestSettingsPage_ElevationWiredFormsVisibleToCashier(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	// Seed the DATA-availability guards (not permission guards — those must
	// survive ut-docs#867 untouched) so every guarded un-gated site actually
	// renders: a payment method ({{ if .payMethods }}), a sample catalogue
	// item ({{ if gt .sampleCount 0 }}), a deferred restore prompt
	// ({{ if .restorePromptDeferred }}), a pending base plugin
	// ({{ range .pendingBasePlugins }}), and a register for the picker.
	for _, s := range []string{
		`CREATE TABLE payment_methods (id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT,
		 is_active INTEGER NOT NULL DEFAULT 1, sort_order INTEGER NOT NULL DEFAULT 0, plugin_id TEXT)`,
		`INSERT INTO payment_methods (id, name, type, is_active, sort_order) VALUES ('cash', 'Cash', 'cash', 1, 1)`,
		`CREATE TABLE items (id TEXT PRIMARY KEY, name TEXT, is_sample_data INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO items (id, name, is_sample_data) VALUES ('demo-1', 'Demo Widget', 1)`,
		`INSERT INTO registers(id,name,is_active) VALUES('regA','Front Till',1)`,
	} {
		if _, err := d.Db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Settings.Set(t.Context(), common.KeyRestorePromptStatus, common.RestorePromptStatusDeferred); err != nil {
		t.Fatal(err)
	}
	if err := savePendingBasePlugins(t.Context(), d, []basePluginSpec{{CanonicalType: "language", Locale: "de"}}); err != nil {
		t.Fatal(err)
	}

	get := func(u auth.User) string {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req = auth.WithUser(req, u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}

	// One marker per elevation-wired site, unique to that form/button.
	elevationWired := []string{
		`hx-post="/api/enrol/now"`,                  // enrollment card, unenrolled branch
		`hx-post="/api/settings/display-mode"`,      // display-advanced: mode form
		`id="window-mode-form"`,                     // display-advanced: window mode
		`id="launch-on-startup-cb"`,                 // display-advanced: autostart checkbox
		`hx-post="/api/settings/payments-default"`,  // payments card (kept {{ if .payMethods }})
		`hx-post="/api/settings/payments-fee"`,      // payments fee rows
		`hx-post="/api/backup/now"`,                 // backup card: only the Backup-now button
		`data-testid="demo-remove"`,                 // data card (kept sampleCount guard)
		`data-testid="restore-dismiss"`,             // data card (kept restorePromptDeferred guard)
		`data-testid="pending-base-plugin-dismiss"`, // data card (kept pendingBasePlugins guard)
		`hx-post="/api/settings/report-retention"`,  // retention card: mode form only
		`hx-post="/api/settings/till-name"`,         // tills card (kept IsPrimaryTill guard)
		`hx-post="/api/settings/till-register"`,     // tills card: register picker
		`hx-post="/api/settings/idle-lock"`,         // idle-lock card
		`hx-post="/api/settings/kiosk-idle-reset"`,  // kiosk-idle-reset card
		`hx-post="/api/settings/telemetry"`,         // telemetry card
		`hx-post="/api/settings/save"`,              // currency card
		`hx-post="/api/settings/shop-type"`,         // shop-type card
		`hx-post="/api/settings/printer"`,           // ut-docs#866: printer card
		`hx-post="/api/settings/invoice"`,           // ut-docs#866: invoice card
	}

	// Manager-only content — one marker per site that must stay gated. The
	// prose markers are the empty-state strings those gated blocks render in
	// this dataless fixture (their tables/exports have no structural marker
	// until data exists).
	managerOnly := []string{
		`href="/report-issue"`,                     // issue-report card: not elevation-wired
		`hx-post="/api/update/check"`,              // update card: flat canPerform(plugin_management)
		`hx-post="/api/settings/update-schedule"`,  // update card
		`id="exit-to-os-form"`,                     // its own AuthorizeManager PIN flow, out of scope
		"No backups yet",                           // backup file table / empty-state: flat deny, real content
		`data-testid="data-reset"`,                 // reset-transactions: flat-denied fetch()
		`id="reset-archives"`,                      // archives restore/purge: flat-denied
		`id="cust-search-btn"`,                     // GDPR search: flat-denied, surfaces PII
		`id="cat-preview-btn"`,                     // catalog cleanup: flat-denied
		"No export or report plugin is installed.", // data-export section: flat-denied
		"No archived reports yet.",                 // retention coverage summary: business content
		`data-testid="retention-export"`,           // retention export: elevation-wired endpoint, but stays gated — real business content (coverage stats + a sales-report download), not just an action
		`hx-post="/api/settings/upsert"`,           // raw upsert browser: deliberate exception
		`id="new-setting"`,                         // raw upsert browser's add form
	}

	cashierHTML := get(cashUser)
	for _, marker := range elevationWired {
		if !strings.Contains(cashierHTML, marker) {
			t.Errorf("cashier render is missing elevation-wired site %s — the in-place PIN dialog can never be reached from the UI", marker)
		}
	}
	for _, marker := range managerOnly {
		if strings.Contains(cashierHTML, marker) {
			t.Errorf("cashier render leaks manager-only content %s", marker)
		}
	}

	// No regression for the already-working case: a manager sees everything.
	managerHTML := get(mgrUser)
	for _, marker := range elevationWired {
		if !strings.Contains(managerHTML, marker) {
			t.Errorf("manager render is missing %s", marker)
		}
	}
	for _, marker := range managerOnly {
		if !strings.Contains(managerHTML, marker) {
			t.Errorf("manager render is missing manager-only content %s", marker)
		}
	}
}

// ut-docs#867 review nit: with none of the Data card's data-availability
// guards true (no demo sample, no pending base plugin, no deferred restore
// prompt), a cashier must not get an empty bordered card containing only the
// "🧹 Data management" heading — the card itself should be absent, same as
// any other manager-only card with nothing to show.
func TestSettingsPage_DataCardHiddenFromCashierWhenNothingPending(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_ = d

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, cashUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "Data management") {
		t.Errorf("cashier with no pending demo/restore/plugin data still sees the Data management card heading")
	}
	if strings.Contains(body, `data-testid="demo-remove"`) || strings.Contains(body, `data-testid="restore-dismiss"`) || strings.Contains(body, `data-testid="pending-base-plugin-dismiss"`) {
		t.Errorf("cashier sees a Data-card sub-action with nothing backing it")
	}
}

// ut-docs#866 review (N3): un-gating the whole printer card would newly
// expose two flat-denied controls to a cashier — the Test print button
// (no audit trail, stays out of the elevation mechanism) and the
// receipt-designer link (a flat-denied full-page redirect, ut-docs#870) —
// producing the exact "visible but silently blocked" bug this line of work
// exists to remove (a click would fall through to app.js's generic
// server-error banner). Both stay manager-only; the mode/address form
// itself (the actual elevation-wired site) must still render.
func TestSettingsPage_PrinterCardHidesTestPrintAndDesignerLinkFromCashier(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_ = d

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, cashUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-post="/api/settings/printer"`) {
		t.Fatal("cashier render is missing the elevation-wired printer form itself")
	}
	if strings.Contains(body, `hx-post="/api/print/test"`) {
		t.Error("cashier render leaks the flat-denied Test print button")
	}
	if strings.Contains(body, `href="/receipt-designer"`) {
		t.Error("cashier render leaks the flat-denied receipt-designer link")
	}

	mgrReq := httptest.NewRequest(http.MethodGet, "/settings", nil)
	mgrReq = auth.WithUser(mgrReq, mgrUser)
	mgrRec := httptest.NewRecorder()
	mux.ServeHTTP(mgrRec, mgrReq)
	mgrBody := mgrRec.Body.String()
	if !strings.Contains(mgrBody, `hx-post="/api/print/test"`) || !strings.Contains(mgrBody, `href="/receipt-designer"`) {
		t.Error("manager render is missing the Test print button or receipt-designer link")
	}
}

// ut-docs#924: a Settings.Set failure on the telemetry/generic-upsert
// endpoints used to leak the raw Go/SQL error text via http.Error(w,
// err.Error(), ...) instead of a translated message. Force a real repo
// error (drop the underlying table) and assert the response carries the
// localized fallback, never the raw error string.
func TestSettingsEndpoints_RepoErrorIsLocalized(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if _, err := d.Db.Exec(`DROP TABLE settings`); err != nil {
		t.Fatalf("drop settings table: %v", err)
	}

	rec := postForm(mux, "/api/settings/telemetry", url.Values{"optIn": {"on"}}, &mgrUser)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("telemetry with broken settings table = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Could not save") {
		t.Fatalf("telemetry error body = %q, want the localized save-failed message", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "no such table") {
		t.Fatalf("telemetry error body leaked raw SQL error text: %q", rec.Body.String())
	}

	rec = postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.tax_inclusive"}, "value": {"true"}}, &mgrUser)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("upsert with broken settings table = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Could not save") {
		t.Fatalf("upsert error body = %q, want the localized save-failed message", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "no such table") {
		t.Fatalf("upsert error body leaked raw SQL error text: %q", rec.Body.String())
	}
}

// TestExitToOSEndpoint_NoShellAttached503NoAudit (ut-docs#1039): the .deb
// attach-mode topology — ShellPollWindowController wired, no shell polling,
// no spawn-mode fallback. A correct manager PIN must get an honest 503
// ("this till's window can't be reached"), never the fabricated 204 the
// old NoopWindowController default produced — and NO audit row: only a
// real exit gets audited, same only-on-success reasoning as ut-docs#616.
func TestExitToOSEndpoint_NoShellAttached503NoAudit(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, _, d := newFullAuthDeps(t)
	d.WindowCtl = common.NewShellPollWindowController(d.Shell, nil)

	hash, err := auth.HashPIN("482913")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.ExecContext(t.Context(),
		`INSERT INTO users(id,username,display_name,pin_hash,role,is_active) VALUES('mgr1','mgr1','Manager One',?,'manager',1)`, hash); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/settings/exit-to-os",
		strings.NewReader(url.Values{"manager_pin": {"482913"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, cashUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-shell exit-to-os = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if n := auditCount(t, d.Db, "exit_to_os"); n != 0 {
		t.Fatalf("audit_log has %d exit_to_os entries after a failed exit, want 0 (only a real exit is audited)", n)
	}
}

// TestExitToOSEndpoint_AttachedAckingShell204AndAudit: with a shell holding
// a live control poll that applies and acknowledges "normal", the same
// request is a genuine 204 with the ut-docs#616 audit row.
func TestExitToOSEndpoint_AttachedAckingShell204AndAudit(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, _, d := newFullAuthDeps(t)
	d.WindowCtl = common.NewShellPollWindowController(d.Shell, nil)

	hash, err := auth.HashPIN("482913")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.ExecContext(t.Context(),
		`INSERT INTO users(id,username,display_name,pin_hash,role,is_active) VALUES('mgr1','mgr1','Manager One',?,'manager',1)`, hash); err != nil {
		t.Fatal(err)
	}

	// Simulate the attached shell: heartbeat now, then behave like the real
	// long poll — wake on the mode change, apply, acknowledge.
	d.Shell.SetMode("kiosk")
	d.Shell.NoteSeen("kiosk")
	_, rev := d.Shell.Snapshot()
	go func() {
		mode, _ := d.Shell.Wait(context.Background(), rev, 5*time.Second)
		d.Shell.NoteSeen(mode)
	}()

	req := httptest.NewRequest(http.MethodPost, "/api/settings/exit-to-os",
		strings.NewReader(url.Values{"manager_pin": {"482913"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, cashUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("attached+acked exit-to-os = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if mode, _ := d.Shell.Snapshot(); mode != "normal" {
		t.Fatalf("live mode after exit-to-os = %q, want normal", mode)
	}
	if n := auditCount(t, d.Db, "exit_to_os"); n != 1 {
		t.Fatalf("audit_log has %d exit_to_os entries after a real exit, want 1", n)
	}
}
