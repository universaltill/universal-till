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

	// SumUp demo path (no reader id): approves, and an amount ending .13 declines.
	out, err := authorize(t, rt, sumup, "payment.sumup.authorize", 1000)
	if err != nil || !strings.Contains(out, `"outcome":"approved"`) {
		t.Fatalf("sumup demo approve = %q, %v", out, err)
	}
	_, err = authorize(t, rt, sumup, "payment.sumup.authorize", 1013)
	if err == nil || !strings.Contains(err.Error(), "exit_code(2)") { // the plugins' declined exit code
		t.Fatalf("sumup demo .13 = %v, want a decline (exit code 2)", err)
	}
	// Stripe without a key declines at once, with no network use.
	_, err = authorize(t, rt, stripe, "payment.stripe.authorize", 1000)
	if err == nil || !strings.Contains(err.Error(), "exit_code(2)") {
		t.Fatalf("stripe with no key = %v, want a decline (exit code 2)", err)
	}

	// Tamper with the installed wasm: the loader backstop refuses to load it.
	wasm := filepath.Join(paths.Plugins(), sumup, "1.5.1", "bin", "plugin.wasm")
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
