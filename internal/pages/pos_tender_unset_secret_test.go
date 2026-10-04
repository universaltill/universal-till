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
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#3648 (found while building #3633's regression coverage, out of
// that card's scope — #3633 was an unconfigured-but-loaded plugin; this is
// a plugin that never loaded at all). blockingPaymentEventDispatch
// (refund_page.go) treated "no bus subscriber" as "nothing to gate" for
// every payment method, including one whose owning plugin DECLARES a
// payment.<key>.authorize hook but isn't actually running (module failed
// to load at Sync, so it's never subscribed) — as opposed to a genuinely
// hook-less method (cash, qrpay), which must still pass through untouched.
// This drives a REAL compiled wasip1 payment guest through the real
// WasmRuntime.Sync and the real POST /api/pos/tender handler, with the
// module file deliberately absent so Sync can't subscribe it, and proves
// the tender is refused rather than completed unconsulted.

const brokenHookPluginID = "com.test.payment-brokenhook"

func buildBrokenHookGuest(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	out := filepath.Join(t.TempDir(), "brokenhook.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/unsetsecret_guest")
	cmd.Dir = filepath.Dir(file)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 unsetsecret guest: %v\n%s", err, raw)
	}
	return out
}

// installBrokenHookPlugin seeds an active wasm payment plugin with a
// "stripe" payment entry and its payment.stripe.authorize hook — the hook
// row exists either way, since it's written at install time, independent of
// whether the plugin ever loads. withModule=false leaves the module file
// off disk, so the real Sync marks the plugin broken and never subscribes
// it: the hook stays declared with zero live subscribers.
func installBrokenHookPlugin(t *testing.T, dp *common.Deps, withModule bool) {
	t.Helper()
	stmts := []string{
		`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
		 VALUES ('` + brokenHookPluginID + `', '1.0.0', 'Broken Hook Pay', 'test', 'wasm', './plugin.wasm', 'https://example.test/p.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`,
		`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('` + brokenHookPluginID + `', 'Broken Hook Pay', '1.0.0', './plugin.wasm', 'wasm', 1)`,
		`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
		 VALUES ('e-bh', '` + brokenHookPluginID + `', 'stripe', 'Card (Stripe)', 'payment', 'payment.stripe.requested', 1)`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
		 VALUES ('h-bh', '` + brokenHookPluginID + `', 'payment.stripe.authorize', 'handle_authorize', 1)`,
		`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
		 VALUES ('p-bh', '` + brokenHookPluginID + `', 'events:receive', 1)`,
	}
	for _, s := range stmts {
		if _, err := dp.Db.Exec(s); err != nil {
			t.Fatalf("seed plugin: %v\n%s", err, s)
		}
	}

	baseDir := t.TempDir()
	if withModule {
		guest := buildBrokenHookGuest(t)
		raw, err := os.ReadFile(guest)
		if err != nil {
			t.Fatal(err)
		}
		modDir := filepath.Join(baseDir, brokenHookPluginID, "1.0.0")
		if err := os.MkdirAll(modDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(modDir, "plugin.wasm"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers() // process-global singleton; isolate from other tests
	w := plugins.NewWasmRuntime(baseDir)
	t.Cleanup(func() {
		w.Close(context.Background())
		bus.ResetSubscribers()
	})
	w.Sync(context.Background(), dp.Db)
}

// tenderBrokenHookPayment tenders the seeded ABC basket (total 120) on the
// plugin's "stripe" tender through the real POST /api/pos/tender handler.
func tenderBrokenHookPayment(t *testing.T, mux *http.ServeMux) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"stripe","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// Ask #1 of #3633 also named "a crashed plugin / missing hook"; #3648 is
// that case tracked on its own, since #3633 only covered an unconfigured
// BUT LOADED plugin. An active payment plugin whose module is missing (so
// Sync marks it broken and it has NO subscription for
// payment.stripe.authorize) must not let its tender complete a sale as if
// paid.
func TestTender_RealWasmPaymentPlugin_ModuleMissingDeclinesAndRecordsNoSale(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	installBrokenHookPlugin(t, dp, false)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	rec := tenderBrokenHookPayment(t, mux)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("want 402 (declined) for a tender on a plugin that declares the hook but never loaded, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, leak := range []string{"payment declined:", "wasm", "exit_code", "not loaded", "not subscribed"} {
		if strings.Contains(body, leak) {
			t.Fatalf("raw plugin/engine error text %q leaked into the operator-facing response: %s", leak, body)
		}
	}
	if !strings.Contains(body, "Payment declined") {
		t.Fatalf("expected the localized pos.toast.payment_declined copy, got: %s", body)
	}
	if n := countSales(t, dp); n != 0 {
		t.Fatalf("a tender on an unloaded payment plugin recorded %d sale(s) — the sale completed (and a receipt would print) with no plugin consulted", n)
	}
	if len(dp.Engine.Basket().Lines) == 0 {
		t.Fatalf("expected the basket to survive a declined plugin tender")
	}
}
