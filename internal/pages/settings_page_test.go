package pages

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pages/settingsnav"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/uislot"
	layoutsalon "github.com/universaltill/universal-till/plugins/layout-salon"
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

// ut-docs#1400: POST /api/settings/payments-fee hardcoded `* 100` for the
// fixed-fee minor-unit conversion regardless of the active currency's
// decimals -- an operator entering "500" for a ¥500 fixed fee on a
// 0-decimal shop (IRR/IRT/IQD/AFN/JPY) got 50000 minor units stored instead
// of 500. bp (basis points) is unaffected -- it's a percentage, not money,
// and stays *100 regardless of currency.
func TestPaymentsSettingsFee_IsCurrencyDecimalsAware(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	httpx.InitCurrency("IRT")
	t.Cleanup(func() { httpx.InitCurrency("GBP") })

	rec := postForm(mux, "/api/settings/payments-fee", url.Values{
		"method": {"cash"}, "percent": {"1.75"}, "fixed": {"500"},
	}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("valid fee under IRT: code=%d body=%s", rec.Code, rec.Body.String())
	}
	raw, ok, _ := d.Settings.Get(t.Context(), "payments.fee.cash")
	if !ok {
		t.Fatalf("stored fee not found")
	}
	// Decoded, not a substring match: "fixed":500 is itself a substring of
	// the unfixed bug's "fixed":50000, so a Contains check here would
	// silently pass against the very regression this test exists to catch.
	var fee struct {
		BP    int64 `json:"bp"`
		Fixed int64 `json:"fixed"`
	}
	if err := json.Unmarshal([]byte(raw), &fee); err != nil {
		t.Fatalf("decode stored fee %q: %v", raw, err)
	}
	if fee.BP != 175 || fee.Fixed != 500 {
		t.Fatalf("stored fee under a 0-decimal currency = %+v, want {BP:175 Fixed:500} (not Fixed:50000)", fee)
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

// The self-order kiosk payment mode (ut-docs#582) is manager-gated,
// validated against the closed {kiosk, counter} enum, and stored in
// runtime state.
func TestKioskPaymentModeEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	// ut-docs#865: elevation prompt, not a flat 403 (same as every other
	// kiosk setting on this page).
	if rec := postForm(mux, "/api/settings/kiosk-payment-mode", url.Values{"mode": {"counter"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier kiosk-payment-mode = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	if rec := postForm(mux, "/api/settings/kiosk-payment-mode", url.Values{"mode": {"bogus"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid kiosk-payment-mode = %d, want 400", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/kiosk-payment-mode", url.Values{"mode": {"counter"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("valid kiosk-payment-mode = %d, want 204", rec.Code)
	}
	if d.CurrentState().KioskPaymentMode != "counter" {
		t.Fatalf("kiosk payment mode = %q, want %q", d.CurrentState().KioskPaymentMode, "counter")
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

// CatalogImportBarcodeFromSKUDefault (ut-docs#1356) is the per-shop toggle
// for whether the import page's "derive a barcode from SKU" checkbox
// starts pre-ticked. Same manager-gated, elevation-wired, boolean shape as
// launch-on-startup above — this endpoint only ever writes the settings-
// table key, never anything import behaviour actually reads at commit time.
func TestCatalogImportBarcodeFromSKUDefaultEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if rec := postForm(mux, "/api/settings/catalog-import-barcode-default", url.Values{"enabled": {"true"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier catalog-import-barcode-default = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	if rec := postForm(mux, "/api/settings/catalog-import-barcode-default", url.Values{"enabled": {"not-a-bool"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed catalog-import-barcode-default = %d, want 400", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/catalog-import-barcode-default", url.Values{"enabled": {"true"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("enable catalog-import-barcode-default = %d, want 204", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), data.CatalogImportBarcodeFromSKUDefaultKey); v != "1" {
		t.Fatalf("stored %s = %q, want 1", data.CatalogImportBarcodeFromSKUDefaultKey, v)
	}
	if rec := postForm(mux, "/api/settings/catalog-import-barcode-default", url.Values{"enabled": {"false"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("disable catalog-import-barcode-default = %d, want 204", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), data.CatalogImportBarcodeFromSKUDefaultKey); v != "0" {
		t.Fatalf("stored %s = %q, want 0", data.CatalogImportBarcodeFromSKUDefaultKey, v)
	}
}

// CatalogPrePackUnitPrice (ut-docs#3391) is the shop's opt-in for printing
// a pre-packed item's unit price on its shelf label. Same manager-gated,
// elevation-wired, boolean shape as catalog-import-barcode-default above,
// plus the audit row every settings write leaves.
func TestCatalogPrePackUnitPriceEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if v, ok, _ := d.Settings.Get(t.Context(), data.CatalogPrePackUnitPriceEnabledKey); ok && v == "1" {
		t.Fatalf("%s must default off, got %q", data.CatalogPrePackUnitPriceEnabledKey, v)
	}
	if rec := postForm(mux, "/api/settings/catalog-pre-pack-unit-price", url.Values{"enabled": {"true"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier catalog-pre-pack-unit-price = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	if v, ok, _ := d.Settings.Get(t.Context(), data.CatalogPrePackUnitPriceEnabledKey); ok && v == "1" {
		t.Fatal("an un-elevated cashier POST turned the setting on")
	}
	if rec := postForm(mux, "/api/settings/catalog-pre-pack-unit-price", url.Values{"enabled": {"not-a-bool"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed catalog-pre-pack-unit-price = %d, want 400", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/catalog-pre-pack-unit-price", url.Values{"enabled": {"true"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("enable catalog-pre-pack-unit-price = %d, want 204", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), data.CatalogPrePackUnitPriceEnabledKey); v != "1" {
		t.Fatalf("stored %s = %q, want 1", data.CatalogPrePackUnitPriceEnabledKey, v)
	}
	if rec := postForm(mux, "/api/settings/catalog-pre-pack-unit-price", url.Values{"enabled": {"false"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("disable catalog-pre-pack-unit-price = %d, want 204", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), data.CatalogPrePackUnitPriceEnabledKey); v != "0" {
		t.Fatalf("stored %s = %q, want 0", data.CatalogPrePackUnitPriceEnabledKey, v)
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'catalog_pre_pack_unit_price_changed' AND entity_id = ?`, data.CatalogPrePackUnitPriceEnabledKey).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("audit rows = %d, want 2 (one per successful write, none for the refused cashier)", n)
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

// RecordInputHeartbeat exists only to satisfy the WindowController
// interface for this settings-page test double (ut-docs#1329) — none of
// this file's tests exercise the heartbeat endpoint (that's
// window_state_api_test.go), so there's nothing meaningful to record here.
func (r *recordingWindowController) RecordInputHeartbeat() error { return nil }

// ReleaseKiosk only satisfies the interface (ut-docs#3466): no settings-page
// handler calls it — the kiosk_unlock directive has its own spy in
// cloudsync_kiosk_unlock_test.go.
func (r *recordingWindowController) ReleaseKiosk() error { return nil }

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

// TestExitToOSEndpoint_LockedOutReturns429NotGeneric403 is ut-docs#1104: the
// handler already special-cases auth.ErrLockedOut into 429 (vs. a plain PIN
// failure's 403), but nothing proved it — every existing exit-to-os test
// that burns failed attempts
// (TestExitToOSBlankPINRejectedWithoutBurningLockoutBudget) deliberately
// sends BLANK pins, which the handler rejects before ever calling
// AuthorizeManager, so the lockout path itself had no coverage here. Five
// wrong, non-blank attempts burn the device-wide lockout budget
// (auth.ErrLockedOut, 5 failures — internal/auth/auth_test.go's
// TestLoginLockout); the request right after — even with the CORRECT PIN —
// must come back 429, not 403, since that's the status the client JS
// branches on to show "locked out" instead of "wrong PIN"
// (web/ui/pages/settings.html, web/ui/pages/login.html).
func TestExitToOSEndpoint_LockedOutReturns429NotGeneric403(t *testing.T) {
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

	makeReq := func(pin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/settings/exit-to-os",
			strings.NewReader(url.Values{"manager_pin": {pin}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, cashUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// 5 wrong, non-blank attempts — enough to burn the device-wide lockout
	// budget.
	for i := 0; i < 5; i++ {
		if rec := makeReq("000000"); rec.Code != http.StatusForbidden {
			t.Fatalf("wrong PIN attempt %d: got %d, want 403", i+1, rec.Code)
		}
	}
	// Locked out now: even the CORRECT PIN must come back 429, not 403.
	if rec := makeReq("482913"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("correct PIN while locked out: got %d, want 429: %s", rec.Code, rec.Body.String())
	}
	if wc.called {
		t.Fatal("a locked-out attempt must never reach the window-control hook")
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

// POST /api/settings/basket-panel-width (ut-docs#2308): the sell-screen
// basket/products divider's persisted position. Mirrors
// TestDisplayAndStoreSettings' own ui-scale bounds coverage just below, plus
// the reset shape (width_rem omitted/"0") that ui-scale itself has no
// equivalent of.
func TestBasketPanelWidthSettings(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	// Bounds: [common.MinBasketPanelWidthRem, common.MaxBasketPanelWidthRem].
	if rec := postForm(mux, "/api/settings/basket-panel-width", url.Values{"width_rem": {"5"}}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("below-floor width = %d, want 400", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/basket-panel-width", url.Values{"width_rem": {"999"}}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("above-ceiling width = %d, want 400", rec.Code)
	}

	// A valid save is reflected into runtime state immediately (no reload
	// needed — d.CurrentState() is what index_page.go's next render reads).
	if rec := postForm(mux, "/api/settings/basket-panel-width", url.Values{"width_rem": {"30"}}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("valid width = %d, want 204", rec.Code)
	}
	if d.CurrentState().BasketPanelWidthRem != 30 {
		t.Fatalf("basket panel width = %v, want 30", d.CurrentState().BasketPanelWidthRem)
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyBasketPanelWidth); v != "30" {
		t.Fatalf("stored %s = %q, want %q", common.KeyBasketPanelWidth, v, "30")
	}

	// Reset: width_rem="0" (the divider's own double-tap/double-click and
	// the Settings -> Display Reset button both send exactly this) clears
	// the override back to 0 (unset, app.css's own built-in default) —
	// unlike ui-scale, which has no reset affordance and would silently
	// keep the prior value on a bare "0" (see
	// common.TestSaveState_ZeroUIScaleDoesNotClobberPriorValue).
	if rec := postForm(mux, "/api/settings/basket-panel-width", url.Values{"width_rem": {"0"}}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("reset = %d, want 204", rec.Code)
	}
	if d.CurrentState().BasketPanelWidthRem != 0 {
		t.Fatalf("basket panel width after reset = %v, want 0", d.CurrentState().BasketPanelWidthRem)
	}
	if v, ok, _ := d.Settings.Get(t.Context(), common.KeyBasketPanelWidth); ok && v != "" {
		t.Fatalf("stored %s = %q after reset, want cleared", common.KeyBasketPanelWidth, v)
	}

	// An omitted width_rem is the same reset shape as an explicit "0".
	if rec := postForm(mux, "/api/settings/basket-panel-width", url.Values{"width_rem": {"32"}}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("valid width = %d, want 204", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/basket-panel-width", url.Values{}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("omitted width_rem = %d, want 204", rec.Code)
	}
	if d.CurrentState().BasketPanelWidthRem != 0 {
		t.Fatalf("basket panel width after omitted-reset = %v, want 0", d.CurrentState().BasketPanelWidthRem)
	}
}

// UI scale / theme are ungated per-till display preferences; save/upsert are
// manager-gated (ut-docs#179) store-wide settings. All validate and reflect
// into runtime state for a manager.
func TestDisplayAndStoreSettings(t *testing.T) {
	t.Setenv("UT_AUTH", "off") // behaviour, not the ut-docs#3079 permission gate
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
	if st.Currency != "EUR" || st.Country != "DE" || st.TaxRateBP != 1900 {
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

// ut-docs#3292: on a joined till (sync.primary_url set) the Settings rename
// is that till's own name — sync.till_name, per-till, saved locally — never
// till.name, which there is the main till's shop-wide name and would be
// written through to the main till (ut-docs#2791), renaming it instead.
func TestTillNameEndpoint_JoinedTillRenamesItselfNotTheMainTill(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	var mainHits atomic.Int32
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mainHits.Add(1)
		http.Error(w, "the main till must not be asked", http.StatusTeapot)
	}))
	t.Cleanup(main.Close)
	// A bearer, so a write-through would really reach the stub above
	// (applySettingsOnMain refuses before any request without one).
	for k, v := range map[string]string{
		"sync.primary_url": main.URL,
		"sync.bearer":      "test-bearer",
		"till.name":        "Main Counter",
		"sync.till_name":   "Back Office",
	} {
		if err := d.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}

	if rec := postForm(mux, "/api/settings/till-name", url.Values{"name": {"  Terrace  "}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("joined till-name = %d body=%s, want 204", rec.Code, rec.Body.String())
	}
	if got := settingValue(t, d, "sync.till_name"); got != "Terrace" {
		t.Fatalf("sync.till_name = %q, want %q", got, "Terrace")
	}
	if got := settingValue(t, d, "till.name"); got != "Main Counter" {
		t.Fatalf("till.name (the main till's) = %q, want it unchanged", got)
	}
	if n := mainHits.Load(); n != 0 {
		t.Fatalf("the rename reached the main till %d time(s); a joined till's own name is per-till", n)
	}
	if got := enroll.DeviceName(ctx, d.Settings); got != "Terrace" {
		t.Fatalf("DeviceName (reported to the cloud) = %q, want %q", got, "Terrace")
	}
	audits := renameTillAudits(t, d)
	if len(audits) != 1 || audits[0]["_entity_id"] != "sync.till_name" || audits[0]["key"] != "sync.till_name" || audits[0]["name"] != "Terrace" {
		t.Fatalf("audit rows = %+v, want one till_name_changed on sync.till_name", audits)
	}
}

// The main till's Settings rename is unchanged by ut-docs#3292: till.name,
// audited under that key.
func TestTillNameEndpoint_MainTillAuditsTillNameKey(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if rec := postForm(mux, "/api/settings/till-name", url.Values{"name": {"Front"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("main till-name = %d, want 204", rec.Code)
	}
	if got := settingValue(t, d, "till.name"); got != "Front" {
		t.Fatalf("till.name = %q", got)
	}
	if got := settingValue(t, d, "sync.till_name"); got != "" {
		t.Fatalf("sync.till_name = %q, want it untouched on a main till", got)
	}
	audits := renameTillAudits(t, d)
	if len(audits) != 1 || audits[0]["_entity_id"] != "till.name" || audits[0]["key"] != "till.name" {
		t.Fatalf("audit rows = %+v, want one till_name_changed on till.name", audits)
	}
}

// ut-docs#3292: the till-name field shows on every till, holding the name
// that till reports for itself (enroll.DeviceName): till.name on the main
// till, sync.till_name on a joined till — never the main till's name there.
func TestSettingsPage_TillNameFieldShowsThisTillsOwnName(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	ctx := t.Context()

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
	field := func(v string) string { return `<input type="text" name="name" maxlength="60" value="` + v + `">` }

	if err := d.Settings.Set(ctx, "till.name", "Main Counter"); err != nil {
		t.Fatal(err)
	}
	if body := get(); !strings.Contains(body, `/api/settings/till-name`) || !strings.Contains(body, field("Main Counter")) {
		t.Fatalf("main till: expected the till-name field holding till.name, got:\n%s", body)
	}

	for k, v := range map[string]string{"sync.primary_url": "https://primary.local", "sync.till_name": "Back Office"} {
		if err := d.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	body := get()
	if !strings.Contains(body, `/api/settings/till-name`) || !strings.Contains(body, field("Back Office")) {
		t.Fatalf("joined till: expected the till-name field holding sync.till_name, got:\n%s", body)
	}
	if strings.Contains(body, field("Main Counter")) {
		t.Fatal("joined till: the till-name field must not show the main till's name")
	}

	// No own name yet → the translated default, like the main till.
	if err := d.Settings.Set(ctx, "sync.till_name", ""); err != nil {
		t.Fatal(err)
	}
	if body := get(); !strings.Contains(body, field(httpx.T("en", "setup.till_name.default"))) {
		t.Fatalf("joined till with no own name: expected the default name in the field")
	}
}

// ut-docs#2726: the auto-update switch shows what the scheduler actually
// does — On at 03:00 on a till that never touched it, Off once the shop
// switched it off.
func TestSettingsPage_AutoUpdateShowsEffectiveDefault(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	get := func() string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}
	checked := regexp.MustCompile(`<input type="checkbox" name="enabled"\s+checked>`)

	body := get()
	if !checked.MatchString(body) {
		t.Fatal("untouched till: the auto-update switch should render On")
	}
	if !strings.Contains(body, `<input type="time" name="time" value="03:00">`) {
		t.Fatal("untouched till: the auto-update time should render 03:00")
	}

	if err := d.Settings.Set(t.Context(), keyAutoUpdateEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	if checked.MatchString(get()) {
		t.Fatal("shop switched auto-update off: the switch must render Off")
	}
}

// ut-docs#1133 (ADR-0065 follow-up, independent review 2026-08-26): the
// Tills card's quarantine help text + "View quarantined entries" button
// must not appear on a single-till shop that has never enrolled a
// replica — InsertJournalQuarantine only ever fires while applying a
// REPLICA's pushed batch, so there is structurally nothing to see yet.
// Once a replica has ever been enrolled, or a quarantine row already
// exists (even after every replica that ever produced one is later
// revoked — ListTills only returns CURRENTLY enrolled tills), the
// section must show. newFullAuthDeps's minimal schema has no tills/
// sync_journal_quarantine tables, so this needs the fully migrated DB
// newMigratedSyncDeps (sync_admin_test.go) already sets up.
func TestSettingsPage_QuarantineSectionOnlyWhenRelevant(t *testing.T) {
	dp := newMigratedSyncDeps(t, "settings-quarantine.db")
	dp.AuthSvc = auth.NewService(dp.Db) // newMigratedSyncDeps leaves this nil -- registerSettings's canPerform needs it
	initPagesI18n(t)
	mux := http.NewServeMux()
	registerSettings(mux, dp)

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

	if body := get(); strings.Contains(body, `href="/sync-quarantine"`) {
		t.Fatalf("single-till shop, never enrolled a replica: quarantine section should be hidden, got:\n%s", body)
	}

	tillID, err := data.NewTillsRepo(dp.Db).InsertTill(context.Background(), "Replica 1", hashBearer("token-settings"), data.TillRoleAdditional)
	if err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	if body := get(); !strings.Contains(body, `href="/sync-quarantine"`) {
		t.Fatalf("a replica is enrolled: quarantine section should show, got:\n%s", body)
	}

	if err := data.NewPOSRepo(dp.Db).InsertJournalQuarantine(context.Background(), data.JournalQuarantineEntry{
		TillID: tillID, SaleID: "sale-q1", ReceiptNo: "T2-Q001",
		Reason: "unknown voucher on redemption replay", PayloadJSON: "{}",
		QuarantinedAt: "2026-08-26T10:00:00Z",
	}); err != nil {
		t.Fatalf("seed quarantine entry: %v", err)
	}
	if err := data.NewTillsRepo(dp.Db).DeleteTill(context.Background(), tillID); err != nil {
		t.Fatalf("revoke till: %v", err)
	}
	body := get()
	if !strings.Contains(body, `href="/sync-quarantine"`) {
		t.Fatalf("quarantine row survives after its till is revoked: section should still show, got:\n%s", body)
	}
	if !strings.Contains(body, "1") {
		t.Fatalf("expected the quarantine count in the card, got:\n%s", body)
	}
}

// ut-docs#1613 review finding (M1): a restore staged in an EARLIER visit
// must still show its restart trigger on a plain page render — otherwise an
// operator who reloads (or the till itself relaunches) between staging and
// clicking Restart now lands right back on the "restart the till to finish"
// dead end this card exists to remove, with the restore still silently
// staged on disk (db.PendingRestore is a persistent, on-disk fact; only
// the POST /api/backup/restore response used to show this). Stubs the
// backupRestorePending/backupRestartSupported seams directly (same
// convention as backup_api_test.go) rather than staging a real restore —
// this page-render test only needs to observe what the page does once
// PendingRestore reports true, not re-prove StageRestore itself.
func TestSettingsPage_ShowsRestartButtonWhenRestorePending(t *testing.T) {
	dp := newMigratedSyncDeps(t, "settings-restore-pending.db")
	dp.AuthSvc = auth.NewService(dp.Db)
	initPagesI18n(t)
	mux := http.NewServeMux()
	registerSettings(mux, dp)

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

	if body := get(); strings.Contains(body, `hx-post="/api/backup/restart-now"`) {
		t.Fatalf("no restore staged: restart trigger should not appear, got:\n%s", body)
	}

	stubBackupRestorePending(t, true)
	stubBackupRestartSupported(t, true)
	body := get()
	if !strings.Contains(body, `hx-post="/api/backup/restart-now"`) {
		t.Fatalf("restore staged from an earlier visit: want the restart trigger on plain page load, got:\n%s", body)
	}
	if !strings.Contains(body, httpx.T("en", "settings.backup.restart_now")) {
		t.Fatalf("want the visible Restart now button, got:\n%s", body)
	}

	stubBackupRestartSupported(t, false)
	if body := get(); !strings.Contains(body, httpx.T("en", "tills.pairing.close_and_reopen")) {
		t.Fatalf("restore staged but unsupported platform: want the close-and-reopen instruction, got:\n%s", body)
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

	// A replica (sync.primary_url set) keeps the picker, like till-name.
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "https://primary.local"); err != nil {
		t.Fatal(err)
	}
	if body := get(); !strings.Contains(body, `/api/settings/till-register`) {
		t.Fatalf("replica: expected the till-register picker to still render, got:\n%s", body)
	}
}

// ut-docs#1894: the reset-archives list's CreatedAt and RetainedUntilDisplay
// now render through locale-aware formatting (httpx.FormatDateTime/
// FormatDate) instead of a hardcoded Go layout, same pattern as journal's
// #1632 fix. RetainedUntil = created_at's date + data.GlobalArchiveMinDays
// (no country configured in this fixture, so the global floor applies).
func TestSettingsPage_ResetArchivesRendersLocaleFormattedTimestamps(t *testing.T) {
	orig := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = orig })

	mux, d := newRealDBDeps(t)
	createdAt := "2026-08-15T09:30:00Z"
	if _, err := d.Db.Exec(`INSERT INTO reset_batches (id, created_at, sales_count) VALUES ('b-gated', ?, 3)`, createdAt); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings?lang=de-DE", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, createdAt) {
		t.Fatalf("reset-archives row must not show the raw RFC3339 CreatedAt: %s", body)
	}
	if !strings.Contains(body, "15.08.2026 09:30") {
		t.Fatalf("reset-archives row must show the de-DE-formatted CreatedAt: %s", body)
	}
	retainedUntil := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(data.GlobalArchiveMinDays))
	wantRetained := retainedUntil.Format("02.01.2006")
	if !strings.Contains(body, wantRetained) {
		t.Fatalf("gated row must show the de-DE-formatted RetainedUntilDisplay (%s): %s", wantRetained, body)
	}
}

// ut-docs#698: a batch still inside its retention window must show the
// retained-until date and NOT offer the Delete-permanently button, so an
// operator never steps through the manager-PIN prompt (ut-docs#1841,
// ADR-0087) only to be refused; a batch outside the window (or with no
// sales at all) must offer the control exactly as before.
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
		`name="address" id="printer-address-input" value="" placeholder="192.168.1.50:9100" dir="ltr"`,
		`name="device" value="" placeholder="/dev/usb/lp0" dir="ltr"`,
		`name="kitchenAddr" id="printer-kitchen-addr-input" value="" placeholder="192.168.1.60:9100" dir="ltr"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected printer field with dir=\"ltr\": %s\ngot:\n%s", want, body)
		}
	}
}

// TestSettingsPage_DrawerPinFieldRendersAndSelects (ut-docs#1136) covers the
// template's actual `selected`-attribute output for the drawer-pin <select>,
// the same GET /settings round-trip precedent as
// TestWindowModeEndpoint/TestSettingsPage_TillRegisterPickerRendersAndSelects
// — the earlier unit tests cover printerConfig/the handler/Render()'s byte
// output but never the rendered HTML itself.
func TestSettingsPage_DrawerPinFieldRendersAndSelects(t *testing.T) {
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

	// Default (no setting persisted yet): pin 2 selected.
	if !regexp.MustCompile(`value="2"\s+selected`).MatchString(get()) {
		t.Fatalf("expected pin 2 selected by default:\n%s", get())
	}
	if regexp.MustCompile(`value="5"\s+selected`).MatchString(get()) {
		t.Fatalf("pin 5 must not be selected by default:\n%s", get())
	}

	// Persist pin 5 directly (registerPrintAPI's own POST endpoint isn't
	// registered on this harness's mux) and confirm the template flips.
	if err := d.Settings.Set(context.Background(), keyPrinterDrawerPin, "5"); err != nil {
		t.Fatalf("Settings.Set: %v", err)
	}
	body := get()
	if !regexp.MustCompile(`value="5"\s+selected`).MatchString(body) {
		t.Fatalf("expected pin 5 selected after persisting the setting:\n%s", body)
	}
	if regexp.MustCompile(`value="2"\s+selected`).MatchString(body) {
		t.Fatalf("pin 2 must not still show selected once pin 5 is persisted:\n%s", body)
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

	// ut-docs#3079: a cashier no longer gets the page at all (sale-only),
	// so neither the raw table nor the #867 elevation-wired currency card
	// reaches them.
	cashierHTML := settingsRefusedForCashier(t, mux)
	if strings.Contains(cashierHTML, `id="new-setting"`) {
		t.Fatal("cashier sees the raw settings.all key/value table")
	}
	if strings.Contains(cashierHTML, `name="currency"`) {
		t.Fatal("cashier's 403 still carries the currency card")
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

// TestSaveSettings_LocaleMarksConfirmed (ut-docs#1074): this handler is the
// one genuine operator-explicit locale choice, so it must mark
// common.KeyLocaleConfirmed — the signal that stops a later derivation
// (ut-docs#1027's country-change re-derive, or a base-plugin-install
// catch-up) from silently overriding it.
func TestSaveSettings_LocaleMarksConfirmed(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if v, ok, _ := d.Settings.Get(t.Context(), common.KeyLocaleConfirmed); ok && v == "true" {
		t.Fatal("locale-confirmed already true before any Settings save — test fixture is not proving anything")
	}

	rec := postForm(mux, "/api/settings/save", url.Values{"locale": {"ar"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("save = %d", rec.Code)
	}
	if v, ok, err := d.Settings.Get(t.Context(), common.KeyLocaleConfirmed); err != nil || !ok || v != "true" {
		t.Fatalf("stored %s = (%q, %v, %v), want (true, true, nil)", common.KeyLocaleConfirmed, v, ok, err)
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

// TestSaveSettings_CountryChangeRederivesLocale is the Settings-side half of
// ut-docs#1027's acceptance criteria: "changing country afterwards
// re-derives the locale... never silently leaves a mismatched pair" — gated
// by localeSafeToPreset (see its doc comment), so an RTL locale whose
// language pack isn't installed yet doesn't switch the live UI's direction/
// digit rendering out from under it.
//
// (Note: neither shipped Settings form actually posts "country" to this
// handler today — see TestUpsertCountry_RederivesLocale below for the path
// an operator can actually reach. This handler's own country handling is
// exercised directly for defensiveness/API-shape correctness.)
func TestSaveSettings_CountryChangeRederivesLocale(t *testing.T) {
	t.Run("country changes to GB, no locale posted, en is available -> re-derives to en-GB", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		rec := postForm(mux, "/api/settings/save", url.Values{"country": {"GB"}}, &mgrUser)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("save = %d", rec.Code)
		}
		if got := d.CurrentState().Locale; got != "en-GB" {
			t.Fatalf("Locale after country->GB = %q, want en-GB", got)
		}
	})
	t.Run("country changes to DE, no locale posted, de is NOT available but non-RTL -> re-derives to de-DE anyway", func(t *testing.T) {
		// de-DE is non-RTL (Latin digits, LTR either way), so localeSafeToPreset
		// allows it even with no German pack installed — this is the card's own
		// headline case (a German shop must not stay on en-US) and must not
		// regress to "left untouched" the way an RTL locale correctly does below.
		mux, _, d := newFullAuthDeps(t)
		rec := postForm(mux, "/api/settings/save", url.Values{"country": {"DE"}}, &mgrUser)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("save = %d", rec.Code)
		}
		if got := d.CurrentState().Locale; got != "de-DE" {
			t.Fatalf("Locale after country->DE = %q, want de-DE", got)
		}
	})
	t.Run("country changes to PK, ur-PK is RTL and NOT available -> locale left untouched", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		d.UpdateState(func(s *common.RuntimeState) { s.Locale = "tr" })
		rec := postForm(mux, "/api/settings/save", url.Values{"country": {"PK"}}, &mgrUser)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("save = %d", rec.Code)
		}
		if got := d.CurrentState().Locale; got != "tr" {
			t.Fatalf("Locale after country->PK = %q, want unchanged tr (ur-PK is RTL and not installed)", got)
		}
	})
	t.Run("country changes to GB AND an explicit locale is posted -> explicit locale wins, no re-derive", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		rec := postForm(mux, "/api/settings/save", url.Values{"country": {"GB"}, "locale": {"ar"}}, &mgrUser)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("save = %d", rec.Code)
		}
		if got := d.CurrentState().Locale; got != "ar" {
			t.Fatalf("Locale after country->GB with explicit locale=ar = %q, want ar (explicit wins)", got)
		}
	})
}

// TestUpsertCountry_RederivesLocale is the regression test for ut-docs#1027
// review finding 2: neither shipped Settings form posts "country" to
// /api/settings/save, so the raw "All settings" key/value table
// (POST /api/settings/upsert) is, today, the ONLY shipped UI path an
// operator can use to change store.country after setup — this is where the
// card's re-derive acceptance criterion has to actually live.
func TestUpsertCountry_RederivesLocale(t *testing.T) {
	t.Run("country -> GB, en available -> re-derives to en-GB and live-applies", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.country"}, "value": {"GB"}}, &mgrUser)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("upsert = %d body=%s", rec.Code, rec.Body.String())
		}
		if got := d.CurrentState().Locale; got != "en-GB" {
			t.Fatalf("Locale after upsert country->GB = %q, want en-GB", got)
		}
		if got := httpx.DefaultLocale(); got != "en-GB" {
			t.Fatalf("DefaultLocale after upsert country->GB = %q, want en-GB (live apply, no restart)", got)
		}
	})
	t.Run("country -> PK, ur-PK is RTL and not installed -> locale left untouched", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		d.UpdateState(func(s *common.RuntimeState) { s.Locale = "tr" })
		rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.country"}, "value": {"PK"}}, &mgrUser)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("upsert = %d body=%s", rec.Code, rec.Body.String())
		}
		if got := d.CurrentState().Locale; got != "tr" {
			t.Fatalf("Locale after upsert country->PK = %q, want unchanged tr (ur-PK is RTL and not installed)", got)
		}
	})
}

// The claim-code / register-now / fleet enrol endpoints all refuse a non-manager
// operator before ever touching the marketplace (offline-first, no network in
// A high-density small touchscreen (e.g. a 10.1" 1920x1200 panel, ~224 PPI)
