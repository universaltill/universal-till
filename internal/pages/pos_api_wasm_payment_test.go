package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#3633 (P1, money integrity): "an unconfigured payment plugin
// completed the sale and printed a receipt as if paid." The existing
// TestTenderHandler_DeclinedPaymentShowsLocalizedMessageNotRawError
// (pos_api_test.go) proves completeTender fails closed GIVEN a declining
// subscriber — but that subscriber is an in-process Go closure. These tests
// cover the link it skips: a REAL compiled wasip1 payment guest
// (internal/plugins/testdata/unsetsecret_guest), loaded through
// WasmRuntime.Sync and run by wazero, reading its own "secret_key" setting
// via the settings_get host function and exiting non-zero when it is unset.
// That exit code must reach completeTender as a decline: 402, localized
// copy, basket kept, no sale. The positive control (same guest, setting
// present) proves the guest/wiring genuinely approves, so the decline test
// cannot pass merely because the guest always fails.

const (
	unsetSecretPluginID = "com.universaltill.payment-unsetsecret"
	unsetSecretMethod   = "unsetsecretpay"
	unsetSecretEvent    = "payment.unsetsecretpay.authorize"
)

// buildUnsetSecretGuest compiles internal/plugins/testdata/unsetsecret_guest
// for wasip1 — same mechanics as buildFiscalGuest, different source dir.
func buildUnsetSecretGuest(t *testing.T) []byte {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	out := filepath.Join(t.TempDir(), "unsetsecret_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/unsetsecret_guest")
	cmd.Dir = filepath.Join(filepath.Dir(file), "..", "plugins")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 guest unsetsecret_guest: %v\n%s", err, raw)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read built guest: %v", err)
	}
	return raw
}

// newUnsetSecretPaymentDeps is newPOSTestDeps with a real plugin dir
// (initTestPaths BEFORE plugins.Init, which captures paths.Plugins() as the
// wasm runtime's base dir — newFiscalSignDeps' ordering), plus the
// payment-plugin rows for a runtime='wasm' payment entry and its compiled
// guest on disk, loaded for real via Sync. secretKey == "" seeds NO
// plugin_settings row at all: absence is exactly the "unconfigured" state
// hostSettingsGet reports as not-found.
func newUnsetSecretPaymentDeps(t *testing.T, secretKey string) (*http.ServeMux, *common.Deps) {
	t.Helper()
	initTestPaths(t)
	mux, dp := newPOSTestDeps(t)
	ctx := context.Background()

	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers() // process-global singleton; isolate from other tests
	t.Cleanup(bus.ResetSubscribers)

	// Row shape: TestTenderHandler_DeclinedPaymentShowsLocalizedMessageNotRawError,
	// with a real wasm entrypoint this time.
	seeds := []struct {
		what  string
		query string
		args  []any
	}{
		{"plugin_catalog", `INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
		  VALUES (?, '1.0.0', 'Unset Secret Pay', 'test', 'wasm', './plugin.wasm', 'https://example.test/plugin.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`, []any{unsetSecretPluginID}},
		{"plugins", `INSERT INTO plugins (id, name, version, install_state, entrypoint, runtime, is_active, trust_level)
		  VALUES (?, 'Unset Secret Pay', '1.0.0', 'installed', './plugin.wasm', 'wasm', 1, 'trusted')`, []any{unsetSecretPluginID}},
		{"plugin_entries", `INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
		  VALUES ('e-unsetsecret', ?, ?, 'Unset Secret Pay', 'payment', 'payment.unsetsecretpay.requested', 1)`, []any{unsetSecretPluginID, unsetSecretMethod}},
		{"plugin_hooks", `INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
		  VALUES ('h-unsetsecret', ?, ?, 'handle_authorize', 1)`, []any{unsetSecretPluginID, unsetSecretEvent}},
		{"plugin_permissions", `INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
		  VALUES ('p-unsetsecret', ?, 'events:receive', 1)`, []any{unsetSecretPluginID}},
	}
	if secretKey != "" {
		seeds = append(seeds, struct {
			what  string
			query string
			args  []any
		}{"plugin_settings", `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope)
		  VALUES ('s-unsetsecret', ?, 'secret_key', ?, 'global')`, []any{unsetSecretPluginID, `"` + secretKey + `"`}})
	}
	for _, s := range seeds {
		if _, err := dp.Db.ExecContext(ctx, s.query, s.args...); err != nil {
			t.Fatalf("seed %s: %v", s.what, err)
		}
	}

	guest := buildUnsetSecretGuest(t)
	dir := filepath.Join(paths.Plugins(), unsetSecretPluginID, "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), guest, 0o644); err != nil {
		t.Fatalf("write plugin.wasm: %v", err)
	}
	dp.Pm.Wasm.Sync(ctx, dp.Db)

	// Guard against a silent no-op: if the module failed to load, the bus
	// has no subscriber for the authorize event, blockingPaymentEventDispatch
	// treats the leg as hookless, and the tender would go through for the
	// WRONG reason (no plugin asked at all) — that must fail here, loudly.
	if !bus.HasSubscribers(unsetSecretEvent) {
		t.Fatalf("real wasm guest not subscribed to %s after Sync — module did not load", unsetSecretEvent)
	}
	return mux, dp
}

func postUnsetSecretTender(t *testing.T, mux *http.ServeMux) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"`+unsetSecretMethod+`","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestTenderHandler_RealWasmGuestDeclinesUnconfiguredPayment: no secret_key
// row -> settings_get not-found -> guest exits 2 -> wasm handler error ->
// PublishAuthorize error -> completeTender paymentDeclinedError -> 402.
func TestTenderHandler_RealWasmGuestDeclinesUnconfiguredPayment(t *testing.T) {
	mux, dp := newUnsetSecretPaymentDeps(t, "")
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	rec := postUnsetSecretTender(t, mux)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("want 402 when an unconfigured real wasm payment plugin declines, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, raw := range []string{"payment declined:", "wasm handler", "exit_code", "secret_key"} {
		if strings.Contains(body, raw) {
			t.Fatalf("raw engine/plugin error text %q leaked into the operator-facing response: %s", raw, body)
		}
	}
	if !strings.Contains(body, "Payment declined") {
		t.Fatalf("expected the localized pos.toast.payment_declined copy, got: %s", body)
	}
	if len(dp.Engine.Basket().Lines) == 0 {
		t.Fatalf("expected basket to survive a declined payment")
	}
	if n := countSales(t, dp); n != 0 {
		t.Fatalf("expected no sale to be recorded when an unconfigured payment plugin declines, got %d", n)
	}
}

// TestTenderHandler_RealWasmGuestApprovesConfiguredPayment is the positive
// control for the test above: the SAME compiled guest, with secret_key
// configured, approves and the sale completes — so the decline above is
// caused by the missing setting, not by a guest/wiring that always fails.
func TestTenderHandler_RealWasmGuestApprovesConfiguredPayment(t *testing.T) {
	mux, dp := newUnsetSecretPaymentDeps(t, "configured-placeholder")
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	rec := postUnsetSecretTender(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 when the configured real wasm payment plugin approves, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := countSales(t, dp); n != 1 {
		t.Fatalf("expected exactly one sale after an approved payment, got %d", n)
	}
}
