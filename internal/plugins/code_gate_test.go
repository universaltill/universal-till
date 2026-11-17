package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// Apple 2.5.2 / ADR-0194: on the App Store channel no marketplace code
// plugin (runtime wasm or go) is ever fetched, imported, persisted or loaded.

func codeGateChannel(t *testing.T, c buildinfo.Channel) {
	t.Helper()
	t.Cleanup(buildinfo.OverrideChannelForTest(c))
}

func TestRefuseCodeRuntime_ByChannel(t *testing.T) {
	cases := []struct {
		ch      buildinfo.Channel
		runtime string
		refused bool
	}{
		{buildinfo.ChannelAppStore, "wasm", true},
		{buildinfo.ChannelAppStore, "go", true},
		{buildinfo.ChannelAppStore, "native", true},
		{buildinfo.ChannelAppStore, "", true},
		{buildinfo.ChannelAppStore, "none", false},
		{buildinfo.ChannelSideload, "wasm", false},
		{buildinfo.ChannelPlay, "wasm", false},
		{buildinfo.ChannelDirect, "go", false},
	}
	for _, c := range cases {
		codeGateChannel(t, c.ch)
		err := RefuseCodeRuntime(c.runtime)
		if c.refused != (err != nil) {
			t.Errorf("%s runtime %q: err = %v, want refused=%v", c.ch, c.runtime, err, c.refused)
		}
		if err != nil && !errors.Is(err, ErrCodePluginRefused) {
			t.Errorf("%s runtime %q: err %v is not ErrCodePluginRefused", c.ch, c.runtime, err)
		}
	}
}

func TestRefuseCodeCanonicalType_AssetOnlyTypesPass(t *testing.T) {
	codeGateChannel(t, buildinfo.ChannelAppStore)
	for _, ty := range []string{"theme", "language", "layout"} {
		if err := RefuseCodeCanonicalType(ty); err != nil {
			t.Errorf("type %q must stay installable on appstore: %v", ty, err)
		}
	}
	for _, ty := range []string{"payment", "page", "integration", ""} {
		if err := RefuseCodeCanonicalType(ty); !errors.Is(err, ErrCodePluginRefused) {
			t.Errorf("type %q: err = %v, want ErrCodePluginRefused", ty, err)
		}
	}
}

func TestClassifyInstallError_CodeRefusedHasItsKey(t *testing.T) {
	codeGateChannel(t, buildinfo.ChannelAppStore)
	f := ClassifyInstallError(RefuseCodeRuntime("wasm"), true)
	if f.MessageKey != CodeInstallRefusedMsgID || f.Retryable {
		t.Fatalf("failure = %+v, want key %q, not retryable", f, CodeInstallRefusedMsgID)
	}
}

type gateServer struct {
	srv  *httptest.Server
	hits atomic.Int32
}

func newGateServer(t *testing.T) *gateServer {
	t.Helper()
	g := &gateServer{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func gateInstaller(t *testing.T, endpoint string, pub ed25519.PublicKey) *MarketplaceInstaller {
	t.Helper()
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: endpoint, APIVersion: "1.0.0", ClientID: "m", StoreID: "s", DeviceID: "d",
		PublicKey: hex.EncodeToString(pub), RequestTimeoutSec: 5,
	}}
	return newTestMarketplaceInstaller(t, cfg, managerTestDB(t))
}

func gateReq(canonicalType string) MarketplaceInstallRequest {
	return MarketplaceInstallRequest{
		ListingID: "listing-1", MerchantID: "m", StoreID: "s", DeviceID: "d",
		DeviceArch: "linux/amd64", CanonicalType: canonicalType,
	}
}

// Install and DownloadToStore refuse a code listing BEFORE any request leaves
// the till (the marketplace sees nothing): hits must stay 0.
func TestAppStore_InstallAndDownloadRefuseBeforeAnyNetwork(t *testing.T) {
	codeGateChannel(t, buildinfo.ChannelAppStore)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	g := newGateServer(t)
	inst := gateInstaller(t, g.srv.URL, pub)

	for _, ty := range []string{"payment", ""} {
		if _, err := inst.Install(context.Background(), gateReq(ty)); !errors.Is(err, ErrCodePluginRefused) {
			t.Errorf("Install type %q: err = %v, want ErrCodePluginRefused", ty, err)
		}
		if _, err := inst.DownloadToStore(context.Background(), gateReq(ty)); !errors.Is(err, ErrCodePluginRefused) {
			t.Errorf("DownloadToStore type %q: err = %v, want ErrCodePluginRefused", ty, err)
		}
	}
	if n := g.hits.Load(); n != 0 {
		t.Fatalf("marketplace saw %d request(s) on appstore, want 0", n)
	}
}

// An asset-only listing still goes to the marketplace on appstore.
func TestAppStore_AssetOnlyListingStillReachesMarketplace(t *testing.T) {
	codeGateChannel(t, buildinfo.ChannelAppStore)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	g := newGateServer(t)
	inst := gateInstaller(t, g.srv.URL, pub)
	_, err := inst.Install(context.Background(), gateReq("theme"))
	if errors.Is(err, ErrCodePluginRefused) {
		t.Fatalf("a theme must not be refused as code: %v", err)
	}
	if g.hits.Load() == 0 {
		t.Fatal("a theme listing never reached the marketplace")
	}
}

// The backstop: a bundle whose manifest says wasm (whatever the catalog type
// claimed) is refused after verification and before the live dir is touched.
func TestAppStore_InstallBundleBackstopRefusesWasmManifest(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := &Manifest{ID: "com.test.gate", Name: "Gate", Version: "1.0.0", Entrypoint: "./plugin-bin",
		Executable: "plugin-bin", Runtime: "wasm", CanonicalType: "page", DeviceArch: "linux/amd64"}
	artifact := signedMarketplaceArtifactWithManifest(t, priv, m)
	sum := checksumSHA256Hex(t, artifact)

	run := func(t *testing.T, ch buildinfo.Channel) error {
		codeGateChannel(t, ch)
		inst := gateInstaller(t, "http://127.0.0.1:1", pub)
		bundle := filepath.Join(t.TempDir(), "b.tar.gz")
		if err := os.WriteFile(bundle, artifact, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := inst.installBundleFile(context.Background(), bundleInstallSpec{
			BundlePath: bundle, ListingID: "listing-1", Checksum: sum, Signature: m.Signature,
			DeviceArch: "linux/amd64",
		})
		if ch == buildinfo.ChannelAppStore {
			assertPluginNotInstalled(t, inst.db, m.ID)
		}
		return err
	}
	if err := run(t, buildinfo.ChannelAppStore); !errors.Is(err, ErrCodePluginRefused) {
		t.Fatalf("appstore: err = %v, want ErrCodePluginRefused", err)
	}
	if err := run(t, buildinfo.ChannelDirect); errors.Is(err, ErrCodePluginRefused) {
		t.Fatalf("direct must not refuse a wasm bundle: %v", err)
	}
}

func TestAppStore_ImportRefusesCodePlugin(t *testing.T) {
	m := []byte(`{"id":"com.test.impgate","name":"I","version":"1.0.0","entrypoint":"./p.wasm","runtime":"wasm","canonical_type":"page","device_arch":"any"}`)
	for _, c := range []struct {
		ch      buildinfo.Channel
		refused bool
	}{{buildinfo.ChannelAppStore, true}, {buildinfo.ChannelSideload, false}, {buildinfo.ChannelPlay, false}, {buildinfo.ChannelDirect, false}} {
		codeGateChannel(t, c.ch)
		imp, base := newTestImporter(t)
		archive := filepath.Join(t.TempDir(), "p.zip")
		writeZipArchive(t, archive, map[string][]byte{"manifest.json": m, "p.wasm": []byte("x")})
		_, err := imp.Import(context.Background(), &ImportRequest{FilePath: archive, TrustLevel: "untrusted", SkipSignature: true})
		if c.refused {
			if !errors.Is(err, ErrCodePluginRefused) {
				t.Fatalf("%s: err = %v, want ErrCodePluginRefused", c.ch, err)
			}
			if matches, _ := filepath.Glob(filepath.Join(base, "com.test.impgate", "*")); len(matches) != 0 {
				t.Fatalf("a refused import left files behind: %v", matches)
			}
		} else if errors.Is(err, ErrCodePluginRefused) {
			t.Fatalf("%s must not refuse a wasm import: %v", c.ch, err)
		}
	}
}

// PersistManifest is the backstop for every path that lands a row (restore,
// replica, cloud directive): code is refused unless the bundled loader
// vouched for the bytes.
func TestAppStore_PersistManifestRefusesCodeUnlessBundledVerified(t *testing.T) {
	codeGateChannel(t, buildinfo.ChannelAppStore)
	db := managerTestDB(t)
	m := &Manifest{ID: "com.test.persist", Name: "P", Version: "1.0.0", Entrypoint: "./p.wasm",
		Runtime: "wasm", CanonicalType: "page", DeviceArch: "any"}
	err := PersistManifest(context.Background(), db, m, InstallOptions{TrustLevel: "system"})
	if !errors.Is(err, ErrCodePluginRefused) {
		t.Fatalf("system trust alone must not open the gate: %v", err)
	}
	if err := PersistManifest(context.Background(), db, m, InstallOptions{TrustLevel: "system", BundledLockVerified: true}); err != nil {
		t.Fatalf("lock-verified bundled persist: %v", err)
	}
}

func TestSideloadPersistManifestStillAcceptsCode(t *testing.T) {
	for _, ch := range []buildinfo.Channel{buildinfo.ChannelSideload, buildinfo.ChannelPlay, buildinfo.ChannelDirect} {
		codeGateChannel(t, ch)
		db := managerTestDB(t)
		m := &Manifest{ID: "com.test.persist2", Name: "P", Version: "1.0.0", Entrypoint: "./p.wasm",
			Runtime: "wasm", CanonicalType: "page", DeviceArch: "any"}
		if err := PersistManifest(context.Background(), db, m, InstallOptions{TrustLevel: "trusted"}); err != nil {
			t.Fatalf("%s: %v", ch, err)
		}
	}
}

// Updates: the checker reports nothing for an installed code plugin on
// appstore (they move only with an app release) and never reaches the network.
func TestAppStore_UpdateCheckerSkipsCodePlugins(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.test.upd", "Updatable", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET author = 'dev-1' WHERE id = 'com.test.upd'`); err != nil {
		t.Fatal(err)
	}
	seedInstalledPlugin(t, db, "com.test.theme", "Themed", "1.0.0", "none", true)
	if _, err := db.Exec(`UPDATE plugins SET author = 'dev-1' WHERE id = 'com.test.theme'`); err != nil {
		t.Fatal(err)
	}
	repo := seededCatalogRepo(t, []marketplace.PluginSummary{
		{DeveloperID: "dev-1", Name: "Updatable", Version: "2.0.0"},
		{DeveloperID: "dev-1", Name: "Themed", Version: "2.0.0"},
	})
	for _, c := range []struct {
		ch   buildinfo.Channel
		want []string
	}{
		{buildinfo.ChannelAppStore, []string{"com.test.theme"}},
		{buildinfo.ChannelDirect, []string{"com.test.theme", "com.test.upd"}},
	} {
		codeGateChannel(t, c.ch)
		got, err := NewUpdateChecker(db, repo, "en", "any").CheckForUpdates(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, u := range got {
			ids[u.PluginID] = true
		}
		if len(ids) != len(c.want) {
			t.Fatalf("%s: updates = %v, want %v", c.ch, ids, c.want)
		}
		for _, w := range c.want {
			if !ids[w] {
				t.Fatalf("%s: missing update for %s (%v)", c.ch, w, ids)
			}
		}
	}
}

// Loader backstop: on appstore a wasm row loads only when the bundled
// verifier vouches for its exact bytes; a row that arrived any other way
// (restore, replica) is never compiled or loaded.
func TestAppStore_WasmSyncLoadsOnlyBundledVerifiedRows(t *testing.T) {
	guest := buildHostfnGuest(t)
	raw, err := os.ReadFile(guest)
	if err != nil {
		t.Fatal(err)
	}
	setup := func(t *testing.T) (*WasmRuntime, *sql.DB) {
		db := managerTestDB(t)
		base := t.TempDir()
		seedInstalledPlugin(t, db, "com.test.loadgate", "L", "1.0.0", "wasm", true)
		dir := filepath.Join(base, "com.test.loadgate", "1.0.0")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "run"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		w := NewWasmRuntime(base)
		t.Cleanup(func() { w.Close(context.Background()) })
		return w, db
	}
	loaded := func(w *WasmRuntime) bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		_, ok := w.modules["com.test.loadgate"]
		return ok
	}

	t.Run("appstore without verifier", func(t *testing.T) {
		codeGateChannel(t, buildinfo.ChannelAppStore)
		SetBundledCodeVerifier(nil)
		w, db := setup(t)
		w.Sync(context.Background(), db)
		if loaded(w) {
			t.Fatal("an unbundled wasm row was loaded on appstore")
		}
	})
	t.Run("appstore verifier says no", func(t *testing.T) {
		codeGateChannel(t, buildinfo.ChannelAppStore)
		SetBundledCodeVerifier(func(id, version, path string, wasm []byte) bool { return false })
		t.Cleanup(func() { SetBundledCodeVerifier(nil) })
		w, db := setup(t)
		w.Sync(context.Background(), db)
		if loaded(w) {
			t.Fatal("a row the verifier refused was loaded")
		}
	})
	t.Run("appstore verifier says yes", func(t *testing.T) {
		codeGateChannel(t, buildinfo.ChannelAppStore)
		SetBundledCodeVerifier(func(id, version, path string, wasm []byte) bool {
			return id == "com.test.loadgate" && version == "1.0.0"
		})
		t.Cleanup(func() { SetBundledCodeVerifier(nil) })
		w, db := setup(t)
		w.Sync(context.Background(), db)
		if !loaded(w) {
			t.Fatal("a verified bundled row was not loaded")
		}
	})
	t.Run("direct unchanged", func(t *testing.T) {
		codeGateChannel(t, buildinfo.ChannelDirect)
		SetBundledCodeVerifier(nil)
		w, db := setup(t)
		w.Sync(context.Background(), db)
		if !loaded(w) {
			t.Fatal("marketplace wasm no longer loads on direct")
		}
	})
}
