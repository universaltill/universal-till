//go:build bundledplugins

package bundled

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/netaccess"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Review (ut-docs#6626): the real UK VAT wasm, embedded from the pinned
// release, is loaded by the wasm runtime on the App Store channel once it is
// enabled (the other real-wasm test only installs it). A tax plugin that is
// installed but refused by the loader is 'broken', and a broken tax plugin
// blocks every sale line (ut-docs#368).
func TestRealBundles_UKTaxLoadsOnAppStoreWhenEnabled(t *testing.T) {
	realEmbedPresent(t)
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	netaccess.SetDemo(true)
	t.Cleanup(func() { netaccess.SetDemo(false) })
	d := openTestDB(t)
	ctx := context.Background()
	if _, err := Sync(ctx, d.DB); err != nil {
		t.Fatal(err)
	}
	const tax = "com.universaltill.tax-uk"
	repo := data.NewPluginRepo(d.DB)
	if err := repo.SetPluginActive(ctx, nil, tax, true); err != nil {
		t.Fatal(err)
	}
	Register()
	t.Cleanup(func() { plugins.SetBundledCodeVerifier(nil) })
	rt := plugins.NewWasmRuntime(paths.Plugins())
	t.Cleanup(func() { rt.Close(ctx) })
	rt.Sync(ctx, d.DB)
	_, state, found, err := data.NewSyncPluginsRepo(d.DB).InstalledPluginVersion(ctx, tax)
	if err != nil || !found || state == data.PluginStateBroken {
		t.Fatalf("the real UK VAT plugin is %q after load (found=%v err=%v), want loadable", state, found, err)
	}
	_, err = authorize(t, rt, tax, "tax.rate.ask", 1000)
	if err != nil && strings.Contains(err.Error(), "module not loaded") {
		t.Fatalf("the enabled real UK VAT wasm was not loaded on appstore: %v", err)
	}

	// Every locked extra file (locales, content) is on disk with its pinned hash.
	l, _ := compiledLock()
	for _, e := range l.Plugins {
		if !verifyOnDisk(e, paths.Plugins(e.ID, e.Version)) {
			t.Errorf("%s: installed files do not match the lock", e.ID)
		}
		for rel, want := range e.Files {
			if _, err := readVerified(embeddedFS(), e.ID, rel, want); err != nil {
				t.Errorf("%s: embedded %s: %v", e.ID, rel, err)
			}
		}
	}
}

// Review 2: a build that embeds nothing vouches for nothing, even for the
// very bytes the committed lock pins (the lock is compiled into every build).
func TestRealBundles_VerifyNeedsTheEmbeddedSet(t *testing.T) {
	realEmbedPresent(t)
	l, err := compiledLock()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range l.Plugins {
		wasm, err := readVerified(embeddedFS(), e.ID, e.WasmPath, e.WasmSHA256)
		if err != nil {
			t.Fatal(err)
		}
		path := "/data/plugins/" + e.ID + "/" + e.Version + "/" + e.WasmPath
		if !Verify(e.ID, e.Version, path, wasm) {
			t.Fatalf("%s: the locked bytes at the locked path do not verify", e.ID)
		}
		restore := OverrideAvailableForTest(false)
		ok := Verify(e.ID, e.Version, path, wasm)
		restore()
		if ok {
			t.Fatalf("%s: a build without the embedded set vouched for a wasm", e.ID)
		}
	}
}
