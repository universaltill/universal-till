//go:build bundledplugins

package bundled

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/netaccess"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
)

// These run only in the bundled-plugins CI job (scripts/ci/fetch-bundled-plugins.sh
// then go test -tags bundledplugins): the REAL Stripe and SumUp wasm, embedded
// from the pinned releases, are installed through Sync, verified against the
// lock, loaded by the wasm runtime on the App Store channel and run on their
// no-credentials demo paths. Nothing reaches the network.

func realEmbedPresent(t *testing.T) {
	t.Helper()
	if !Available() {
		t.Fatal("bundledplugins build without the embedded set")
	}
	l, err := compiledLock()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range l.Plugins {
		if _, err := os.Stat(filepath.Join("embedded", e.ID, e.WasmPath)); err != nil {
			t.Fatalf("run scripts/ci/fetch-bundled-plugins.sh first: %v", err)
		}
	}
}

func authorize(t *testing.T, rt *plugins.WasmRuntime, id, typ string, amount int64) (string, error) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"amount": amount, "currency": "GBP"})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := rt.HandleEvent(ctx, id, plugins.Event{ID: "ev-" + typ, Type: typ, Timestamp: time.Now(), Payload: payload})
	return string(out), err
}

func TestRealBundles_InstallVerifyLoadAndRunDemoPaths_AppStore(t *testing.T) {
	realEmbedPresent(t)
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	netaccess.SetDemo(true) // no plugin egress at all, whatever it asks for
	t.Cleanup(func() { netaccess.SetDemo(false) })
	d := openTestDB(t)
	ctx := context.Background()

	changed, err := Sync(ctx, d.DB)
	if err != nil || !changed {
		t.Fatalf("Sync = %v, %v", changed, err)
	}
	repo := data.NewPluginRepo(d.DB)
	const stripe, sumup = "com.universaltill.payment-stripe", "com.universaltill.payment-sumup"
	for _, id := range []string{stripe, sumup} {
		if on, _ := repo.PluginActive(ctx, id); on {
			t.Fatalf("%s must start disabled", id)
		}
	}

	// Disabled: nothing is loaded, so nothing can run.
	Register()
	t.Cleanup(func() { plugins.SetBundledCodeVerifier(nil) })
	rt := plugins.NewWasmRuntime(paths.Plugins())
	t.Cleanup(func() { rt.Close(ctx) })
	rt.Sync(ctx, d.DB)
	if _, err := authorize(t, rt, sumup, "payment.sumup.authorize", 1000); err == nil || !strings.Contains(err.Error(), "module not loaded") {
		t.Fatalf("a disabled bundled plugin ran: %v", err)
	}

	// The shop switches both on: the lock-verified wasm loads on appstore.
	for _, id := range []string{stripe, sumup} {
		if err := repo.SetPluginActive(ctx, nil, id, true); err != nil {
			t.Fatal(err)
		}
	}
	rt.Sync(ctx, d.DB)

	// SumUp 1.9.x fails closed: with no API key it declines at once (no
	// demo approval any more, ut-docs#6565), and uses no network.
	for _, amt := range []int64{1000, 1013} {
		_, err = authorize(t, rt, sumup, "payment.sumup.authorize", amt)
		if err == nil || !strings.Contains(err.Error(), "exit_code(2)") { // the plugins' declined exit code
			t.Fatalf("sumup with no key (%d) = %v, want a decline (exit code 2)", amt, err)
		}
	}
	// Stripe without a key declines at once, with no network use.
	_, err = authorize(t, rt, stripe, "payment.stripe.authorize", 1000)
	if err == nil || !strings.Contains(err.Error(), "exit_code(2)") {
		t.Fatalf("stripe with no key = %v, want a decline (exit code 2)", err)
	}

	// Tamper with the installed wasm: the loader backstop refuses to load it.
	wasm := filepath.Join(paths.Plugins(), sumup, "1.9.2", "bin", "plugin.wasm")
	raw, _ := os.ReadFile(wasm)
	if err := os.WriteFile(wasm, append(raw, 0), 0o644); err != nil {
		t.Fatal(err)
	}
	rt2 := plugins.NewWasmRuntime(paths.Plugins())
	t.Cleanup(func() { rt2.Close(ctx) })
	rt2.Sync(ctx, d.DB)
	if _, err := authorize(t, rt2, sumup, "payment.sumup.authorize", 1000); err == nil || !strings.Contains(err.Error(), "module not loaded") {
		t.Fatalf("a modified bundled wasm was loaded: %v", err)
	}
}

// ut-docs#6626, #6627: the real third entry (UK VAT) installs with the others,
// and every real plugin gets its declared permissions granted at install,
// including the provider hosts of Stripe, SumUp and (sandbox) HMRC, with no
// shop action: the net: call needs a granted net:<host>. A plugin stays OFF
// until enabled, and the UK VAT one is the lock's enable_for_countries GB.
func TestRealBundles_InstallGrantsDeclaredPermissions_AppStore(t *testing.T) {
	realEmbedPresent(t)
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	d := openTestDB(t)
	ctx := context.Background()
	if changed, err := Sync(ctx, d.DB); err != nil || !changed {
		t.Fatalf("Sync = %v, %v", changed, err)
	}
	repo := data.NewPluginRepo(d.DB)
	want := map[string][]string{
		"com.universaltill.payment-stripe": {"net:api.stripe.com"},
		"com.universaltill.payment-sumup":  {"net:api.sumup.com"},
		"com.universaltill.tax-uk":         {"net:test-api.service.hmrc.gov.uk", "events:receive"},
	}
	for id, perms := range want {
		got := grantedSet(t, repo, id)
		for _, p := range perms {
			if !got[p] {
				t.Errorf("%s: %s not granted after install: %v", id, p, got)
			}
		}
		if on, _ := repo.PluginActive(ctx, id); on {
			t.Errorf("%s starts enabled", id)
		}
	}
	if id, ok := EnabledByDefaultFor("GB"); !ok || id != "com.universaltill.tax-uk" {
		t.Fatalf("EnabledByDefaultFor(GB) = %q, %v", id, ok)
	}
}

// Finding 19: the embedded set is exactly the lock, byte for byte (the tagged
// build cannot compile without the named files; this fails if their bytes
// differ from the pinned hashes).
func TestEmbeddedSetMatchesLock(t *testing.T) {
	realEmbedPresent(t)
	fsys := embeddedFS()
	l, err := compiledLock()
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Plugins) < 3 {
		t.Fatalf("lock has %d plugins, want stripe, sumup and tax-uk", len(l.Plugins))
	}
	for _, e := range l.Plugins {
		if _, err := readVerified(fsys, e.ID, e.WasmPath, e.WasmSHA256); err != nil {
			t.Errorf("%s: %v", e.ID, err)
		}
		if _, err := readVerified(fsys, e.ID, "manifest.json", e.ManifestSHA256); err != nil {
			t.Errorf("%s: %v", e.ID, err)
		}
	}
}

// ut-docs#6565: the Which-reader picker only works when the plugin declares
// the payment.<key>.readers hook; a stale lock embedded plugins without it.
func TestRealBundles_PaymentPluginsDeclareTheReadersHook(t *testing.T) {
	realEmbedPresent(t)
	for id, event := range map[string]string{
		"com.universaltill.payment-stripe": "payment.stripe.readers",
		"com.universaltill.payment-sumup":  "payment.sumup.readers",
	} {
		raw, err := os.ReadFile(filepath.Join("embedded", id, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"event": "`+event+`"`) {
			t.Errorf("%s: embedded manifest does not declare the %s hook", id, event)
		}
	}
}
