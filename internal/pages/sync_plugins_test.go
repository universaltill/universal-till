package pages

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#460: plugin install/uninstall on the primary propagates to
// replicas over the existing LAN-sync surface. A replica never receives
// plugin binaries from the primary — it re-fetches and re-verifies the
// listing FROM THE MARKETPLACE ITSELF (the same Ed25519-verified
// MarketplaceInstaller path a manual install uses), triggered by the
// sync pull tick. These tests drive two real till instances (the
// sync_admin_test.go harness shape) against a fake, signing marketplace
// (adapted from internal/plugins/installer_marketplace_test.go — test
// helpers don't export across packages, so the minimal parts are
// duplicated here).

// --- fake marketplace ------------------------------------------------------

// fakeMktRelease is one signed, downloadable version of a listing.
type fakeMktRelease struct {
	artifact []byte
	manifest *plugins.Manifest
	checksum string
}

// fakeMktListing carries every published release of a listing plus which one
// the marketplace currently serves as "latest" (an unpinned token request).
type fakeMktListing struct {
	latest   string
	releases map[string]fakeMktRelease // version -> release
}

type fakeMarketplace struct {
	server     *httptest.Server
	publicKey  ed25519.PublicKey
	privateKey ed25519.PrivateKey

	mu        sync.Mutex
	tokenHits int // POST /v1/downloads/tokens count — proves (non-)reinstall
	listings  map[string]*fakeMktListing
}

func (m *fakeMarketplace) downloadTokenHits() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokenHits
}

// publishVersion adds a signed release for listingID/pluginID and makes it
// the marketplace's latest — the version an UNPINNED token request gets. A
// pinned request (Version set) always gets exactly that release.
func (m *fakeMarketplace) publishVersion(t *testing.T, listingID, pluginID, version string) {
	t.Helper()
	manifest := &plugins.Manifest{
		ID:            pluginID,
		Name:          "Sync Test Plugin " + pluginID,
		Version:       version,
		Entrypoint:    "./plugin-bin",
		Executable:    "plugin-bin",
		Runtime:       "go",
		CanonicalType: "page",
		DeviceArch:    runtime.GOOS + "/" + runtime.GOARCH,
	}
	artifact := signedFakeMktArtifact(t, m.privateKey, manifest)
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.listings[listingID]
	if l == nil {
		l = &fakeMktListing{releases: map[string]fakeMktRelease{}}
		m.listings[listingID] = l
	}
	l.releases[version] = fakeMktRelease{artifact: artifact, manifest: manifest, checksum: sha256Hex(artifact)}
	l.latest = version
}

// publishWasmTaxVersion publishes a signed runtime:"wasm" TAX plugin release
// (ut-docs#368): its manifest registers the tax.rate.ask hook and requests
// events:receive, and its binary is a real compiled wasip1 module (wasmBin)
// so the recovered plugin actually answers asks through the real wazero
// runtime, not a stub.
func (m *fakeMarketplace) publishWasmTaxVersion(t *testing.T, listingID, pluginID, version string, wasmBin []byte) {
	t.Helper()
	manifest := &plugins.Manifest{
		ID:            pluginID,
		Name:          "Sync Tax Plugin " + pluginID,
		Version:       version,
		Entrypoint:    "./plugin.wasm",
		Executable:    "plugin.wasm",
		Runtime:       "wasm",
		CanonicalType: "tax",
		DeviceArch:    "any",
		Hooks:         []plugins.ManifestHook{{Event: "tax.rate.ask", Action: "tax.rate"}},
		Permissions:   []string{"events:receive"},
	}
	artifact := signedFakeMktArtifactWithBinary(t, m.privateKey, manifest, "plugin.wasm", wasmBin)
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.listings[listingID]
	if l == nil {
		l = &fakeMktListing{releases: map[string]fakeMktRelease{}}
		m.listings[listingID] = l
	}
	l.releases[version] = fakeMktRelease{artifact: artifact, manifest: manifest, checksum: sha256Hex(artifact)}
	l.latest = version
}

// publishMismatchedRelease publishes a release keyed under requestedVersion
// (so a pinned download-token request for requestedVersion resolves to it)
// whose signed manifest actually declares actualVersion — a validly signed
// bundle that nonetheless answers a pinned request with the wrong release.
// Simulates ut-docs#479's scenario: a marketplace/backend that doesn't
// hard-error on a version pin but substitutes a different one instead.
func (m *fakeMarketplace) publishMismatchedRelease(t *testing.T, listingID, pluginID, requestedVersion, actualVersion string) {
	t.Helper()
	manifest := &plugins.Manifest{
		ID:            pluginID,
		Name:          "Sync Test Plugin " + pluginID,
		Version:       actualVersion,
		Entrypoint:    "./plugin-bin",
		Executable:    "plugin-bin",
		Runtime:       "go",
		CanonicalType: "page",
		DeviceArch:    runtime.GOOS + "/" + runtime.GOARCH,
	}
	artifact := signedFakeMktArtifact(t, m.privateKey, manifest)
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.listings[listingID]
	if l == nil {
		l = &fakeMktListing{releases: map[string]fakeMktRelease{}}
		m.listings[listingID] = l
	}
	l.releases[requestedVersion] = fakeMktRelease{artifact: artifact, manifest: manifest, checksum: sha256Hex(artifact)}
}

// newFakeMarketplace serves the minimal marketplace surface a real install
// needs: download-token issue + bundle download. Deliberately NOT served:
// /v1/stores/register and /v1/auth/merchant-token (404) — enrolment and
// token failures must stay non-fatal on this path, exactly as offline-first
// demands, and serving register would mutate the enroll package's process
// globals across tests.
func newFakeMarketplace(t *testing.T, pluginIDByListing map[string]string) *fakeMarketplace {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	m := &fakeMarketplace{publicKey: publicKey, privateKey: privateKey, listings: map[string]*fakeMktListing{}}
	for listingID, pluginID := range pluginIDByListing {
		m.publishVersion(t, listingID, pluginID, "1.0.0")
	}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/downloads/tokens":
			var req struct {
				ListingID string `json:"listing_id"`
				Version   string `json:"version"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			m.mu.Lock()
			m.tokenHits++
			listing, ok := m.listings[req.ListingID]
			var release fakeMktRelease
			version := req.Version
			if ok {
				if version == "" {
					version = listing.latest // unpinned = whatever is latest NOW
				}
				release, ok = listing.releases[version]
			}
			m.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"token":               "tok-" + req.ListingID,
					"bundle_url":          m.server.URL + "/bundles/" + req.ListingID + "/" + version + ".tar.gz",
					"release_id":          "release-" + req.ListingID + "-" + version,
					"version":             release.manifest.Version,
					"checksum_sha256":     release.checksum,
					"signature":           release.manifest.Signature,
					"expires_at":          time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
					"resumable_supported": false,
				},
				"error": nil,
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/bundles/"):
			parts := strings.SplitN(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/bundles/"), ".tar.gz"), "/", 2)
			if len(parts) != 2 {
				http.NotFound(w, r)
				return
			}
			m.mu.Lock()
			var release fakeMktRelease
			listing, ok := m.listings[parts[0]]
			if ok {
				release, ok = listing.releases[parts[1]]
			}
			m.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(release.artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.server.Close)
	return m
}

func (m *fakeMarketplace) config() config.MarketplaceConfig {
	return config.MarketplaceConfig{
		EndpointURL:       m.server.URL,
		APIVersion:        "1.0.0",
		ClientID:          "merchant-1",
		ClientSecret:      "secret-1",
		StoreID:           "store-1",
		DeviceID:          "device-1",
		PublicKey:         hex.EncodeToString(m.publicKey),
		RequestTimeoutSec: 30,
	}
}

// signedFakeMktArtifact signs the manifest (same canonical form the verifier
// checks) and packs manifest.json + an executable stub into a .tar.gz.
func signedFakeMktArtifact(t *testing.T, privateKey ed25519.PrivateKey, manifest *plugins.Manifest) []byte {
	t.Helper()
	return signedFakeMktArtifactWithBinary(t, privateKey, manifest, "plugin-bin", []byte("binary"))
}

// signedFakeMktArtifactWithBinary is signedFakeMktArtifact with the packed
// binary's name and content under the caller's control — the wasm tax
// fixture (ut-docs#368) ships a real compiled module as plugin.wasm.
func signedFakeMktArtifactWithBinary(t *testing.T, privateKey ed25519.PrivateKey, manifest *plugins.Manifest, binName string, binData []byte) []byte {
	t.Helper()
	canonical := *manifest
	canonical.Signature = ""
	canonicalBytes, err := json.Marshal(canonical)
	if err != nil {
		t.Fatalf("marshal canonical manifest: %v", err)
	}
	manifest.Signature = hex.EncodeToString(ed25519.Sign(privateKey, canonicalBytes))

	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	var archive bytes.Buffer
	gzWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzWriter)
	for _, f := range []struct {
		name string
		data []byte
		mode int64
	}{
		{"manifest.json", manifestBytes, 0o644},
		{binName, binData, 0o755},
	} {
		if err := tarWriter.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.data))}); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tarWriter.Write(f.data); err != nil {
			t.Fatalf("write tar data: %v", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzWriter.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	manifest.ArtifactHash = sha256Hex(archive.Bytes())
	// Re-pack with the artifact hash embedded? No — the marketplace installer
	// accepts an empty manifest.ArtifactHash (only a NON-empty one is compared
	// to the download checksum), and re-packing would change the checksum
	// again. Clear it so the packed manifest and the served checksum agree.
	manifest.ArtifactHash = ""
	return archive.Bytes()
}

// sha256Hex lives in plugin_api_legacy_test.go (same package).

// --- two-till harness ------------------------------------------------------

// withPaths runs fn with the process-global data root pointed at dir, then
// restores the previous root. This is the cheap per-till scoping the shared
// `paths` global allows: each simulated till's installed plugin FILES live
// in that till's own tree, so (for example) a prune on the replica provably
// cannot touch the primary's files — previously both tills shared one
// plugin dir, which hid exactly that class of bug. Operations stay strictly
// sequential; this is scoping, not a concurrency mechanism.
func withPaths(dir string, fn func()) {
	prev := paths.DataDir()
	paths.Init(dir)
	defer paths.Init(prev)
	fn()
}

// syncPluginsPrimary is a real primary till behind a real HTTP server, with
// its marketplace config pointed at the fake marketplace and its plugin
// files scoped to its own data dir.
type syncPluginsPrimary struct {
	dp      *common.Deps
	server  *httptest.Server
	dataDir string

	mu      sync.Mutex
	visited []string
}

func (p *syncPluginsPrimary) hit(path string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, v := range p.visited {
		if v == path {
			return true
		}
	}
	return false
}

// install runs a marketplace install on the primary (the manual install
// button's path) inside the primary's own paths scope.
func (p *syncPluginsPrimary) install(t *testing.T, listingID string) {
	t.Helper()
	var err error
	withPaths(p.dataDir, func() { _, err = cloudInstallPlugin(t.Context(), p.dp, listingID) })
	if err != nil {
		t.Fatalf("install %s on primary: %v", listingID, err)
	}
}

// uninstall runs an uninstall on the primary inside its own paths scope.
func (p *syncPluginsPrimary) uninstall(t *testing.T, pluginID string) {
	t.Helper()
	var err error
	withPaths(p.dataDir, func() { _, err = cloudRemovePlugin(t.Context(), p.dp, pluginID) })
	if err != nil {
		t.Fatalf("uninstall %s on primary: %v", pluginID, err)
	}
}

// hasPluginFiles reports whether this till's OWN plugin tree holds files for
// pluginID — the per-till isolation the harness now actually provides.
func (p *syncPluginsPrimary) hasPluginFiles(pluginID string) bool {
	_, err := os.Stat(filepath.Join(p.dataDir, "plugins", pluginID))
	return err == nil
}

func newSyncPluginsPrimary(t *testing.T, mkt *fakeMarketplace) *syncPluginsPrimary {
	t.Helper()
	dataDir := t.TempDir()
	var dp *common.Deps
	withPaths(dataDir, func() { dp = newMigratedSyncDeps(t, "primary.db") })
	dp.Cfg.Marketplace = mkt.config()
	if _, err := data.NewTillsRepo(dp.Db).InsertTill(t.Context(), "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	p := &syncPluginsPrimary{dp: dp, dataDir: dataDir}
	mux := http.NewServeMux()
	registerSyncAdmin(mux, dp)
	wrapped := http.NewServeMux()
	wrapped.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.visited = append(p.visited, r.URL.Path)
		p.mu.Unlock()
		mux.ServeHTTP(w, r)
	})
	p.server = httptest.NewServer(wrapped)
	t.Cleanup(p.server.Close)
	return p
}

// syncPluginsReplica is a replica till whose plugin files are scoped to its
// own data dir, separate from the primary's.
type syncPluginsReplica struct {
	dp      *common.Deps
	dataDir string
}

// tick runs one sync pull tick inside the replica's own paths scope.
func (r *syncPluginsReplica) tick(t *testing.T, client *http.Client) {
	t.Helper()
	withPaths(r.dataDir, func() { syncPullTick(t.Context(), r.dp, client, func(context.Context) {}) })
}

func (r *syncPluginsReplica) hasPluginFiles(pluginID string) bool {
	_, err := os.Stat(filepath.Join(r.dataDir, "plugins", pluginID))
	return err == nil
}

func newSyncPluginsReplica(t *testing.T, mkt *fakeMarketplace, primaryURL string) *syncPluginsReplica {
	t.Helper()
	dataDir := t.TempDir()
	var dp *common.Deps
	withPaths(dataDir, func() { dp = newPullTestReplica(t, primaryURL) })
	dp.Cfg.Marketplace = mkt.config()
	return &syncPluginsReplica{dp: dp, dataDir: dataDir}
}

// initTestPaths points the process-global plugin/data tree at a temp dir so
// installs never land in the repo tree ("./data" fallback).
func initTestPaths(t *testing.T) {
	t.Helper()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
}

// seedFileImportedPlugin plants an installed plugin that did NOT come from
// the marketplace: plugins + plugin_catalog rows, but no
// plugin_install_status row (import-from-file records no listing). This is
// the class ut-docs#460 deliberately does NOT propagate.
func seedFileImportedPlugin(t *testing.T, dp *common.Deps, pluginID string) {
	t.Helper()
	ctx := t.Context()
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES (?, '1.0.0', ?, 'go', './plugin', 'file://import', 'deadbeef', '2.5.0', '1.0', '2026-01-01T00:00:00Z')`,
		pluginID, "File Import "+pluginID); err != nil {
		t.Fatalf("seed plugin_catalog: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugins (id, name, version, entrypoint, runtime)
VALUES (?, ?, '1.0.0', './plugin', 'go')`,
		pluginID, "File Import "+pluginID); err != nil {
		t.Fatalf("seed plugins: %v", err)
	}
}

func pluginInstalledVersion(t *testing.T, dp *common.Deps, pluginID string) (string, bool) {
	t.Helper()
	var version string
	err := dp.Db.QueryRowContext(t.Context(), `SELECT version FROM plugins WHERE id = ?`, pluginID).Scan(&version)
	if err != nil {
		return "", false
	}
	return version, true
}

func installStatusState(t *testing.T, dp *common.Deps, listingID string) (string, bool) {
	t.Helper()
	rec, ok, err := plugins.NewInstallStatusStore(dp.Db).Get(t.Context(), listingID)
	if err != nil {
		t.Fatalf("read install status: %v", err)
	}
	return string(rec.State), ok
}

// --- primary-side endpoint: GET /api/sync/plugins --------------------------

func TestSyncPluginsAPI_RejectsUnauthorized(t *testing.T) {
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{})
	primary := newSyncPluginsPrimary(t, mkt)

	resp, err := http.Get(primary.server.URL + "/api/sync/plugins")
	if err != nil {
		t.Fatalf("GET /api/sync/plugins: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no bearer, got %d", resp.StatusCode)
	}
}

func TestSyncPluginsAPI_DumpsActiveMarketplaceSetOnly(t *testing.T) {
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-alpha": "com.test.sync-alpha"})
	primary := newSyncPluginsPrimary(t, mkt)
	ctx := t.Context()

	// A marketplace install on the primary (the manual install button's path).
	if _, err := cloudInstallPlugin(ctx, primary.dp, "listing-alpha"); err != nil {
		t.Fatalf("install on primary: %v", err)
	}
	// A file-imported plugin (no listing_id) must NOT appear in the dump —
	// intentional ut-docs#460 scope boundary.
	seedFileImportedPlugin(t, primary.dp, "com.test.fileimport")

	get := func(query string) (int, string, []map[string]any) {
		req, _ := http.NewRequest(http.MethodGet, primary.server.URL+"/api/sync/plugins"+query, nil)
		req.Header.Set("Authorization", "Bearer token-abc")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /api/sync/plugins: %v", err)
		}
		defer resp.Body.Close()
		var out struct {
			Data struct {
				Version   string `json:"version"`
				Unchanged bool   `json:"unchanged"`
				Bundle    struct {
					Plugins []map[string]any `json:"plugins"`
				} `json:"bundle"`
			} `json:"data"`
		}
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatalf("decode response: %v", err)
			}
		}
		if out.Data.Unchanged {
			return resp.StatusCode, out.Data.Version, nil
		}
		return resp.StatusCode, out.Data.Version, out.Data.Bundle.Plugins
	}

	code, version, rows := get("")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly the one marketplace-installed plugin in the bundle, got %+v", rows)
	}
	row := rows[0]
	if row["listing_id"] != "listing-alpha" || row["plugin_id"] != "com.test.sync-alpha" || row["version"] != "1.0.0" {
		t.Fatalf("unexpected bundle row: %+v", row)
	}

	// Matching-fingerprint poll returns unchanged with no payload.
	code, _, rows = get("?have=" + version)
	if code != http.StatusOK {
		t.Fatalf("expected 200 on unchanged poll, got %d", code)
	}
	if rows != nil {
		t.Fatalf("expected no bundle payload on an unchanged poll, got %+v", rows)
	}
}

// --- replica pull tick: install propagation --------------------------------

// Acceptance (a) + (d): the primary's install happens while the replica is
// NOT ticking (offline); a single later tick converges the replica with no
// extra manual step. Acceptance (e), primary side: a file-imported plugin on
// the primary is not propagated.
func TestSyncPullTick_InstallsPrimaryMarketplacePluginsOnReplica(t *testing.T) {
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-alpha": "com.test.sync-alpha"})
	primary := newSyncPluginsPrimary(t, mkt)
	ctx := t.Context()

	// Replica exists but does not tick while the primary changes ("offline").
	replica := newSyncPluginsReplica(t, mkt, primary.server.URL)

	primary.install(t, "listing-alpha")
	seedFileImportedPlugin(t, primary.dp, "com.test.fileimport")

	hitsAfterPrimaryInstall := mkt.downloadTokenHits()

	// One tick after the fact must converge.
	client := &http.Client{Timeout: 5 * time.Second}
	replica.tick(t, client)

	if v, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); !ok || v != "1.0.0" {
		t.Fatalf("expected the primary's marketplace plugin installed on the replica at 1.0.0, got (%q, %v)", v, ok)
	}
	if state, ok := installStatusState(t, replica.dp, "listing-alpha"); !ok || state != string(plugins.InstallStateActive) {
		t.Fatalf("expected an active install-status record on the replica, got (%q, %v)", state, ok)
	}
	// Loadable: the replica's plugin manager picked it up on reload.
	if _, ok := replica.dp.Pm.Installed["com.test.sync-alpha"]; !ok {
		t.Fatalf("expected the plugin loaded in the replica's plugin manager, got %+v", replica.dp.Pm.Installed)
	}
	// The replica re-fetched from the MARKETPLACE (verified), not the primary
	// — and the files landed in the REPLICA's own tree, not the primary's.
	if mkt.downloadTokenHits() != hitsAfterPrimaryInstall+1 {
		t.Fatalf("expected exactly one marketplace download for the replica install, hits %d -> %d",
			hitsAfterPrimaryInstall, mkt.downloadTokenHits())
	}
	if !replica.hasPluginFiles("com.test.sync-alpha") {
		t.Fatalf("expected the plugin's files in the replica's own plugin tree")
	}
	// File-imported plugin did not propagate.
	if _, ok := pluginInstalledVersion(t, replica.dp, "com.test.fileimport"); ok {
		t.Fatalf("file-imported plugin must NOT propagate to the replica (ut-docs#460 scope boundary)")
	}
	// The tick's plugin work must not skip the stock section that follows.
	if !primary.hit("/api/sync/stock") {
		t.Fatalf("expected the stock sync section to still run after plugin sync")
	}
	if v, _, _ := replica.dp.Settings.Get(ctx, "sync.plugins_version"); v == "" {
		t.Fatalf("expected sync.plugins_version recorded after a converged plugin pull")
	}

	// A second tick with nothing changed must not reinstall (unchanged poll,
	// steady-state reconcile finds no local drift).
	before := mkt.downloadTokenHits()
	replica.tick(t, client)
	if mkt.downloadTokenHits() != before {
		t.Fatalf("expected no marketplace re-download on an unchanged poll, hits %d -> %d", before, mkt.downloadTokenHits())
	}
}

// --- replica pull tick: uninstall propagation ------------------------------

// Acceptance (b) + (d): uninstall on the primary while the replica isn't
// ticking; a single later tick removes it. Acceptance (e), replica side: a
// replica-local file-imported plugin survives the prune even though the
// primary has never heard of it.
func TestSyncPullTick_UninstallsRemovedPluginsOnReplica(t *testing.T) {
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-alpha": "com.test.sync-alpha"})
	primary := newSyncPluginsPrimary(t, mkt)
	replica := newSyncPluginsReplica(t, mkt, primary.server.URL)
	client := &http.Client{Timeout: 5 * time.Second}

	primary.install(t, "listing-alpha")
	replica.tick(t, client)
	if _, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); !ok {
		t.Fatalf("precondition: replica should have converged to installed")
	}

	// A local file-imported plugin on the REPLICA — never on the primary.
	seedFileImportedPlugin(t, replica.dp, "com.test.localimport")

	// Primary uninstalls while the replica is not ticking. Its OWN files go;
	// the replica's copy (in the replica's separate tree) must not.
	primary.uninstall(t, "com.test.sync-alpha")
	if primary.hasPluginFiles("com.test.sync-alpha") {
		t.Fatalf("expected the primary's plugin files removed by its own uninstall")
	}
	if !replica.hasPluginFiles("com.test.sync-alpha") {
		t.Fatalf("the primary's uninstall must not reach into the replica's plugin tree")
	}

	replica.tick(t, client)

	if _, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); ok {
		t.Fatalf("expected the plugin uninstalled from the replica after the primary removed it")
	}
	if _, ok := installStatusState(t, replica.dp, "listing-alpha"); ok {
		t.Fatalf("expected the replica's install-status record cleared after uninstall")
	}
	if _, ok := replica.dp.Pm.Installed["com.test.sync-alpha"]; ok {
		t.Fatalf("expected the plugin dropped from the replica's plugin manager")
	}
	if replica.hasPluginFiles("com.test.sync-alpha") {
		t.Fatalf("expected the replica's own plugin files removed by the synced uninstall")
	}
	// The replica-local file import is untouched (no listing_id on either side).
	if _, ok := pluginInstalledVersion(t, replica.dp, "com.test.localimport"); !ok {
		t.Fatalf("replica-local file-imported plugin must survive the prune (ut-docs#460 scope boundary)")
	}
}

// --- replica-side guard on the manual endpoints ----------------------------

// Acceptance (c): a replica rejects direct install/import/uninstall with a
// clear, translated pointer to the primary — the exact precedent
// /api/sync/enroll-token set (reject, no proxying) — and nothing changes
// locally.
func TestPluginAPIHandlers_RejectMutationsOnReplica(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initTestPaths(t)
	dp := newMigratedSyncDeps(t, "replica.db")
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	// An installed plugin to (fail to) uninstall.
	seedFileImportedPlugin(t, dp, "com.test.keepme")

	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)

	decode := func(rec *httptest.ResponseRecorder) (any, string) {
		var out struct {
			Data  any    `json:"data"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode envelope: %v (body=%s)", err, rec.Body.String())
		}
		return out.Data, out.Error
	}

	t.Run("install-from-marketplace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/install-from-marketplace",
			strings.NewReader(`{"listing_id":"listing-alpha"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("expected 409 on a replica, got %d (body=%s)", rec.Code, rec.Body.String())
		}
		if _, errMsg := decode(rec); errMsg != "plugins.install.error.replica_use_primary" {
			t.Fatalf("expected the replica_use_primary message key, got %q", errMsg)
		}
		// Nothing was installed or even recorded as requested.
		if _, ok := installStatusState(t, dp, "listing-alpha"); ok {
			t.Fatalf("expected no install-status record written by a rejected replica install")
		}
	})

	// File import is deliberately NOT guarded: it has no listing_id, so the
	// sync loop can neither propagate nor prune it — no drift is possible,
	// and a "use the primary" message here would be false (a file import on
	// the primary never arrives anywhere else). Offline provisioning stays
	// per-till, replicas included (ADR-0011 amendment scope boundary).
	t.Run("import-from-file is NOT guarded", func(t *testing.T) {
		var body bytes.Buffer
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/import-from-file", &body)
		req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusConflict {
			t.Fatalf("import-from-file must not be replica-guarded (got 409): file imports are per-till by design")
		}
		// It proceeds into the handler proper and fails on the empty
		// multipart body — proof the guard didn't intercept it.
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for the empty multipart body, got %d (body=%s)", rec.Code, rec.Body.String())
		}
	})

	// Enable/disable/update/rollback all mutate local plugin state a replica
	// has no way to reconcile back on its own — same guard, same envelope.
	for _, tc := range []struct {
		name, url string
	}{
		{"enable", "/api/plugins/com.test.keepme/enable"},
		{"disable", "/api/plugins/com.test.keepme/disable"},
		{"update", "/api/plugins/com.test.keepme/update"},
		{"rollback", "/api/plugins/com.test.keepme/rollback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.url, strings.NewReader(`{"version":"1.0.0"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusConflict {
				t.Fatalf("expected 409 on a replica, got %d (body=%s)", rec.Code, rec.Body.String())
			}
			if _, errMsg := decode(rec); errMsg != "plugins.manage.error.replica_use_primary" {
				t.Fatalf("expected the manage replica_use_primary message key, got %q", errMsg)
			}
		})
	}

	// The two legacy, UI-unreferenced install paths this block used to cover
	// (/api/plugins/upload, /api/plugins/marketplace/install) were guarded
	// here (review MINOR B) rather than fixed: they wrote a `plugins` row
	// verifying only a caller/catalog-supplied SHA256, no Ed25519 signature
	// check at all, in contradiction of this repo's "never run an
	// unverified plugin" rule. ut-docs#480 removed both outright instead —
	// replica-guarding a route doesn't fix an unverified install on the
	// primary, and neither route was reachable from any UI or other repo
	// (2026-07-30 QUEUE-ARCHIVE.md finding already recommended removal).
	// See TestLegacyInstallEndpoints_Removed in plugin_api_legacy_test.go.

	t.Run("uninstall", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.keepme/uninstall", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("expected 409 on a replica, got %d (body=%s)", rec.Code, rec.Body.String())
		}
		if _, errMsg := decode(rec); errMsg != "plugins.uninstall.error.replica_use_primary" {
			t.Fatalf("expected the replica_use_primary message key, got %q", errMsg)
		}
		if _, ok := pluginInstalledVersion(t, dp, "com.test.keepme"); !ok {
			t.Fatalf("expected the plugin still installed after the rejected uninstall")
		}
	})

	// The same requests on a PRIMARY (no sync.primary_url) must NOT hit the
	// guard — proven by not getting a 409 back (they fail later, for their
	// own reasons, in these minimal deps).
	t.Run("primary is not guarded", func(t *testing.T) {
		if err := data.NewSettingsRepo(dp.Db).ClearReplicaIdentity(ctx); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.keepme/uninstall", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusConflict {
			t.Fatalf("primary must not be blocked by the replica guard (got 409)")
		}
	})
}

// BLOCKER 2 (ut-docs#460 review): the plugin STORE page's lifecycle API is
// reachable on a replica too and used to bypass the replica rule entirely —
// download and install both need the same primary-authoritative guard as
// handleInstallFromMarketplace. Mirrors
// TestPluginAPIHandlers_RejectMutationsOnReplica.
func TestPluginStoreHandlers_RejectMutationsOnReplica(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initTestPaths(t)
	dp := newMigratedSyncDeps(t, "replica.db")
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginStoreAPI(mux, dp)

	post := func(action string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/store/"+action,
			strings.NewReader("listing_id=listing-alpha"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	for _, action := range []string{"download", "install"} {
		t.Run(action, func(t *testing.T) {
			rec := post(action)
			if rec.Code != http.StatusConflict {
				t.Fatalf("expected 409 on a replica, got %d (body=%s)", rec.Code, rec.Body.String())
			}
			var out struct {
				Data  any    `json:"data"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode envelope: %v (body=%s)", err, rec.Body.String())
			}
			if out.Error != "plugins.install.error.replica_use_primary" {
				t.Fatalf("expected the replica_use_primary message key, got %q", out.Error)
			}
		})
	}

	// On a PRIMARY the guard must not fire (the requests fail later, for
	// their own reasons — unreachable marketplace — but never with a 409).
	t.Run("primary is not guarded", func(t *testing.T) {
		if err := data.NewSettingsRepo(dp.Db).ClearReplicaIdentity(ctx); err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"download", "install"} {
			if rec := post(action); rec.Code == http.StatusConflict {
				t.Fatalf("primary must not be blocked by the replica guard on store/%s (got 409)", action)
			}
		}
	})
}

// Steady state must NOT be a no-op (ut-docs#460 review): an Unchanged poll
// only says the primary hasn't moved — local drift (here: the plugin removed
// out from under the sync, as a failed reload or manual tampering would)
// must still be detected and healed on the very next tick, from the cached
// last-applied bundle, without the primary's fingerprint ever changing.
func TestSyncPullTick_HealsLocalDriftOnUnchangedPoll(t *testing.T) {
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-alpha": "com.test.sync-alpha"})
	primary := newSyncPluginsPrimary(t, mkt)
	ctx := t.Context()
	replica := newSyncPluginsReplica(t, mkt, primary.server.URL)
	client := &http.Client{Timeout: 5 * time.Second}

	primary.install(t, "listing-alpha")
	replica.tick(t, client)
	if _, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); !ok {
		t.Fatalf("precondition: replica should have converged to installed")
	}
	versionBefore, _, _ := replica.dp.Settings.Get(ctx, "sync.plugins_version")
	if versionBefore == "" {
		t.Fatalf("precondition: fingerprint should be recorded after convergence")
	}

	// Local drift on the replica: the plugin vanishes locally while the
	// primary's set (and therefore the wire fingerprint) is unchanged.
	withPaths(replica.dataDir, func() {
		if _, err := cloudRemovePlugin(ctx, replica.dp, "com.test.sync-alpha"); err != nil {
			t.Fatalf("simulate local drift: %v", err)
		}
	})
	if _, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); ok {
		t.Fatalf("precondition: drift should have removed the plugin locally")
	}

	// Next tick: the wire poll is Unchanged (fingerprint matches), but the
	// reconcile loop must still run against the cached rows and reinstall.
	before := mkt.downloadTokenHits()
	replica.tick(t, client)

	if v, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); !ok || v != "1.0.0" {
		t.Fatalf("expected local drift healed from the cached bundle on an Unchanged poll, got (%q, %v)", v, ok)
	}
	if mkt.downloadTokenHits() != before+1 {
		t.Fatalf("expected exactly one marketplace re-download to heal the drift, hits %d -> %d",
			before, mkt.downloadTokenHits())
	}
	if versionAfter, _, _ := replica.dp.Settings.Get(ctx, "sync.plugins_version"); versionAfter != versionBefore {
		t.Fatalf("healing local drift must not move the pull fingerprint (%q -> %q)", versionBefore, versionAfter)
	}
}

// The replica must converge to the PRIMARY's recorded version, not whatever
// the marketplace currently serves as latest (ut-docs#460 review): a primary
// deliberately held at an older release would otherwise silently fork every
// replica onto a different version.
func TestSyncPullTick_PinsPrimaryVersionNotMarketplaceLatest(t *testing.T) {
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-alpha": "com.test.sync-alpha"})
	primary := newSyncPluginsPrimary(t, mkt)
	ctx := t.Context()
	replica := newSyncPluginsReplica(t, mkt, primary.server.URL)
	client := &http.Client{Timeout: 5 * time.Second}

	// Primary installs while 1.0.0 is latest; the marketplace then publishes
	// 2.0.0. The primary stays (deliberately) on 1.0.0.
	primary.install(t, "listing-alpha")
	mkt.publishVersion(t, "listing-alpha", "com.test.sync-alpha", "2.0.0")
	if v, _ := pluginInstalledVersion(t, primary.dp, "com.test.sync-alpha"); v != "1.0.0" {
		t.Fatalf("precondition: primary should be pinned at 1.0.0, got %q", v)
	}

	// Fresh replica converges: must land on the primary's 1.0.0, NOT the
	// marketplace's latest 2.0.0.
	replica.tick(t, client)
	if v, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); !ok || v != "1.0.0" {
		t.Fatalf("expected the replica pinned to the primary's version 1.0.0, got (%q, %v)", v, ok)
	}

	// A version MISMATCH (not just missing-vs-installed) must trigger a
	// reinstall back to the primary's exact version: force the replica onto
	// latest (2.0.0), then let the reconcile loop pull it back.
	withPaths(replica.dataDir, func() {
		if _, err := cloudInstallPlugin(ctx, replica.dp, "listing-alpha"); err != nil { // unpinned = latest
			t.Fatalf("force replica onto latest: %v", err)
		}
	})
	if v, _ := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); v != "2.0.0" {
		t.Fatalf("precondition: replica should now be drifted to 2.0.0, got %q", v)
	}

	replica.tick(t, client)
	if v, _ := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); v != "1.0.0" {
		t.Fatalf("expected the version mismatch healed back to the primary's pinned 1.0.0, got %q", v)
	}
}

// ut-docs#479: cloudInstallPluginVersion must not silently accept an install
// whose returned version disagrees with the one it pinned. Today's fake
// marketplace hard-errors on an unknown version like the real one does, so
// this forces the mismatch a step further back — a validly signed release
// published under the REQUESTED version's key whose manifest actually
// declares a different version, exactly the shape a future/different
// backend answering the pin with the wrong release would produce.
func TestSyncPullTick_VersionMismatchFromMarketplaceFailsConvergence(t *testing.T) {
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-alpha": "com.test.sync-alpha"})
	primary := newSyncPluginsPrimary(t, mkt)
	replica := newSyncPluginsReplica(t, mkt, primary.server.URL)
	client := &http.Client{Timeout: 5 * time.Second}

	primary.install(t, "listing-alpha") // primary pinned at 1.0.0

	// The marketplace answers a pinned "1.0.0" request with a validly signed
	// bundle that actually declares "9.9.9".
	mkt.publishMismatchedRelease(t, "listing-alpha", "com.test.sync-alpha", "1.0.0", "9.9.9")

	fingerprintBefore, _, _ := replica.dp.Settings.Get(t.Context(), "sync.plugins_version")

	replica.tick(t, client)

	if state, ok := installStatusState(t, replica.dp, "listing-alpha"); !ok || state != string(plugins.InstallStateFailed) {
		t.Fatalf("expected install status Failed after a version mismatch, got (%q, %v)", state, ok)
	}
	fingerprintAfter, _, _ := replica.dp.Settings.Get(t.Context(), "sync.plugins_version")
	if fingerprintAfter != fingerprintBefore {
		t.Fatalf("fingerprint must not advance when convergence failed: before=%q after=%q", fingerprintBefore, fingerprintAfter)
	}
	// The mismatched version must not be left "installed and active" behind
	// the status-table flag alone — round-1 review (ut-docs#479) found that
	// installer.Install had already persisted it (files, plugins/
	// plugin_catalog rows, granted permissions) before the version check
	// ever ran, so ANY reload before the next successful retry would have
	// wired the wrong, unrequested version into the live menu/WASM runtime.
	if v, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); ok {
		t.Fatalf("mismatched version must be rolled back, not left installed: got version %q", v)
	}
	// A clean rollback removes the plugin's whole directory on disk, not
	// just its DB row — asserted explicitly so a future change that drops
	// the files-removal step doesn't regress silently.
	if replica.hasPluginFiles("com.test.sync-alpha") {
		t.Fatalf("mismatched version's files must be removed on rollback, found some under the plugin's directory")
	}

	// Retried on the next tick: once the marketplace serves the requested
	// version correctly, the replica converges.
	mkt.publishVersion(t, "listing-alpha", "com.test.sync-alpha", "1.0.0")
	replica.tick(t, client)
	if v, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); !ok || v != "1.0.0" {
		t.Fatalf("expected the retry to converge once the marketplace serves the pinned version, got (%q, %v)", v, ok)
	}
	if state, ok := installStatusState(t, replica.dp, "listing-alpha"); !ok || state != string(plugins.InstallStateActive) {
		t.Fatalf("expected install status Active after the retry converged, got (%q, %v)", state, ok)
	}
}

// ut-docs#495 (round 2 of the #479 fix): a version mismatch on an IN-PLACE
// UPGRADE — not a fresh install — must not fully uninstall the plugin. The
// replica already has a previously-converged good version installed; when
// the primary moves to a new version and the marketplace answers that pin
// with the wrong release, the replica must land back on the prior good
// version, active, files intact — not uninstalled.
func TestSyncPullTick_VersionMismatchOnUpgradePreservesPriorGoodVersion(t *testing.T) {
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-alpha": "com.test.sync-alpha"})
	primary := newSyncPluginsPrimary(t, mkt)
	replica := newSyncPluginsReplica(t, mkt, primary.server.URL)
	client := &http.Client{Timeout: 5 * time.Second}

	// Primary installs 1.0.0 for real; replica converges to it. This is the
	// "previously-installed good version" the upgrade attempt below must not
	// destroy.
	primary.install(t, "listing-alpha")
	replica.tick(t, client)
	if v, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); !ok || v != "1.0.0" {
		t.Fatalf("precondition: replica should be converged at 1.0.0, got (%q, %v)", v, ok)
	}
	if !replica.hasPluginFiles("com.test.sync-alpha") {
		t.Fatalf("precondition: replica should have plugin files for 1.0.0")
	}

	// The marketplace publishes a REAL 2.0.0 and the primary genuinely
	// upgrades to it (a correct, matching install — not the mismatch itself).
	mkt.publishVersion(t, "listing-alpha", "com.test.sync-alpha", "2.0.0")
	primary.install(t, "listing-alpha") // unpinned = latest = 2.0.0
	if v, _ := pluginInstalledVersion(t, primary.dp, "com.test.sync-alpha"); v != "2.0.0" {
		t.Fatalf("precondition: primary should be at 2.0.0, got %q", v)
	}

	// NOW corrupt what the marketplace serves for a PINNED "2.0.0" request —
	// simulating a future/different backend answering the pin with the wrong
	// release, same shape as ut-docs#479's own test, but this time the
	// replica already has an installed version to protect.
	mkt.publishMismatchedRelease(t, "listing-alpha", "com.test.sync-alpha", "2.0.0", "9.9.9")

	replica.tick(t, client) // replica sees primary at 2.0.0, attempts the pinned upgrade, hits the mismatch

	if v, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); !ok || v != "1.0.0" {
		t.Fatalf("expected the prior good version 1.0.0 to stay installed and active after the mismatch, got (%q, %v)", v, ok)
	}
	if !replica.hasPluginFiles("com.test.sync-alpha") {
		t.Fatalf("expected the plugin's files to survive the failed upgrade attempt")
	}
	// The status record must say Active, not Failed: the plugin IS genuinely
	// still installed and active (just not at the version this pinned
	// attempt wanted). Reviewer round 1 (ut-docs#495) caught that a Failed
	// record here breaks BOTH the very protection this test checks (the next
	// tick's "is there a prior good version" check requires State==Active —
	// see TestSyncPullTick_VersionMismatchOnUpgradeSurvivesRepeatedTicks
	// below) and the prune loop's ability to ever remove this plugin again
	// (see TestSyncPullTick_RolledBackPluginStaysPrunableAfterPrimaryRemovesIt).
	if state, ok := installStatusState(t, replica.dp, "listing-alpha"); !ok || state != string(plugins.InstallStateActive) {
		t.Fatalf("expected install status Active (the plugin is genuinely still active, just at the old version), got (%q, %v)", state, ok)
	}

	// Regression coverage for the sibling fresh-install case (#479): still
	// covered by TestSyncPullTick_VersionMismatchFromMarketplaceFailsConvergence
	// above, unchanged.
}

// ut-docs#495 round 1 review: the sync-pull loop retries every ~30s, so a
// backend that persistently answers a pin with the wrong release (the
// realistic case, not a one-off) must not be able to destroy the prior good
// version merely by outlasting a single tick. Confirmed as a real bug in
// round 1 (a status record left at Failed after a successful rollback lost
// the "is there a prior good version" signal on the very next retry) —
// this test ticks TWICE against the same still-mismatching marketplace.
func TestSyncPullTick_VersionMismatchOnUpgradeSurvivesRepeatedTicks(t *testing.T) {
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-alpha": "com.test.sync-alpha"})
	primary := newSyncPluginsPrimary(t, mkt)
	replica := newSyncPluginsReplica(t, mkt, primary.server.URL)
	client := &http.Client{Timeout: 5 * time.Second}

	primary.install(t, "listing-alpha")
	replica.tick(t, client)
	mkt.publishVersion(t, "listing-alpha", "com.test.sync-alpha", "2.0.0")
	primary.install(t, "listing-alpha")
	mkt.publishMismatchedRelease(t, "listing-alpha", "com.test.sync-alpha", "2.0.0", "9.9.9")

	replica.tick(t, client) // tick 1: mismatch -> rollback to 1.0.0
	if v, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); !ok || v != "1.0.0" {
		t.Fatalf("precondition: tick 1 should roll back to 1.0.0, got (%q, %v)", v, ok)
	}

	replica.tick(t, client) // tick 2: the SAME mismatch, 30s later in real life
	if v, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); !ok || v != "1.0.0" {
		t.Fatalf("expected the prior good version to survive a SECOND mismatched tick too, got (%q, %v)", v, ok)
	}
	if !replica.hasPluginFiles("com.test.sync-alpha") {
		t.Fatalf("expected plugin files to survive a second mismatched tick")
	}

	replica.tick(t, client) // tick 3, for good measure — this must not be a one-tick reprieve
	if v, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); !ok || v != "1.0.0" {
		t.Fatalf("expected the prior good version to survive a THIRD mismatched tick, got (%q, %v)", v, ok)
	}
}

// ut-docs#495 round 1 review: a rolled-back plugin's install-status record
// must still be reachable by convergePluginSet's prune loop (which only
// ever prunes an Active record) — otherwise, once the primary legitimately
// removes the listing, the replica can never uninstall it. Confirmed as a
// real bug in round 1 (the mismatch branch left the record at Failed after
// a successful rollback).
func TestSyncPullTick_RolledBackPluginStaysPrunableAfterPrimaryRemovesIt(t *testing.T) {
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-alpha": "com.test.sync-alpha"})
	primary := newSyncPluginsPrimary(t, mkt)
	replica := newSyncPluginsReplica(t, mkt, primary.server.URL)
	client := &http.Client{Timeout: 5 * time.Second}

	primary.install(t, "listing-alpha")
	replica.tick(t, client)
	mkt.publishVersion(t, "listing-alpha", "com.test.sync-alpha", "2.0.0")
	primary.install(t, "listing-alpha")
	mkt.publishMismatchedRelease(t, "listing-alpha", "com.test.sync-alpha", "2.0.0", "9.9.9")

	replica.tick(t, client) // mismatch -> rollback to 1.0.0, active
	if v, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); !ok || v != "1.0.0" {
		t.Fatalf("precondition: should have rolled back to 1.0.0, got (%q, %v)", v, ok)
	}

	// The shop owner removes the plugin on the primary entirely (not an
	// upgrade — a real, legitimate removal).
	primary.uninstall(t, "com.test.sync-alpha")

	replica.tick(t, client) // must prune the replica's rolled-back copy too

	if v, ok := pluginInstalledVersion(t, replica.dp, "com.test.sync-alpha"); ok {
		t.Fatalf("expected the replica to prune the plugin once the primary removed it, but it's still installed at %q", v)
	}
	if replica.hasPluginFiles("com.test.sync-alpha") {
		t.Fatalf("expected the replica's plugin files to be removed once pruned")
	}
}

// The reload-and-rebuild sequence (Pm.Reload + Menu reassignment) now fires
// from the background sync-pull goroutine every 30s as well as from HTTP
// handlers — Deps.ReloadPlugins must serialize it (PluginMu) so concurrent
// writers can never corrupt the Installed map / Menu (run with -race; the
// unserialized sequence fails the detector). Write-side only: see
// TestReloadPlugins_ConcurrentReadersSurviveRace immediately below for the
// read-side counterpart (ut-docs#478).
func TestReloadPlugins_SerializesConcurrentReloadAndRebuild(t *testing.T) {
	initTestPaths(t)
	dp := newMigratedSyncDeps(t, "reload-race.db")

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				_ = dp.ReloadPlugins(context.Background())
			}
		}()
	}
	wg.Wait()
}

// ut-docs#478: handlers that READ Menu/Pm.Installed/Pm.MenuPlugins (via
// MenuSnapshot / InstalledPlugin / MenuPluginByKey) must be safe to run
// concurrently with ReloadPlugins — Manager.Reload reassigns Installed AND
// MenuPlugins to fresh maps and repopulates both key-by-key, so an unlocked
// concurrent reader of either is a fatal concurrent map access, not just a
// stale read. (Review round 1 caught MenuPlugins missing from this test —
// the original sweep covered the two fields the ticket named and missed
// this third one reassigned in the very same critical section.) Run with
// -race: before the read-side RLock this crashes the detector rather than
// passing quietly.
func TestReloadPlugins_ConcurrentReadersSurviveRace(t *testing.T) {
	initTestPaths(t)
	dp := newMigratedSyncDeps(t, "reload-read-race.db")

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// One writer goroutine hammering the exact reload-and-rebuild sequence
	// this issue is about.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = dp.ReloadPlugins(context.Background())
			}
		}
	}()

	// Several reader goroutines doing what every d.Menu-rendering handler
	// and the plugin-management API handlers do.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = dp.MenuSnapshot()
					_, _ = dp.InstalledPlugin("com.test.sync-alpha")
					_, _ = dp.MenuPluginByKey("some-menu-plugin-key")
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// --- ut-docs#368: join-snapshot row without bytes ---------------------------

// buildTaxAskGuest compiles the wasip1 tax-answering test guest once per
// test. The build runs from THIS package's directory (cwd may have been
// moved to the repo root by chdirRoot).
func buildTaxAskGuest(t *testing.T) []byte {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	out := filepath.Join(t.TempDir(), "taxask.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/taxask_guest")
	cmd.Dir = filepath.Dir(file)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 tax guest: %v\n%s", err, raw)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read built guest: %v", err)
	}
	return raw
}

// seedJoinSnapshotTaxPlugin plants on the replica exactly what a join
// snapshot produces for a marketplace-installed wasm tax plugin: every DB
// row (registry, hooks, permission grant, install-status bookkeeping) at
// the primary's version — and NO file on disk.
func seedJoinSnapshotTaxPlugin(t *testing.T, dp *common.Deps, listingID, pluginID string) {
	t.Helper()
	ctx := t.Context()
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES (?, '1.0.0', ?, 'wasm', './plugin.wasm', 'https://mkt/pkg', 'deadbeef', '0.1.0', '1.0', '2026-01-01T00:00:00Z')`,
		pluginID, "Sync Tax Plugin "+pluginID); err != nil {
		t.Fatalf("seed plugin_catalog: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugins (id, name, version, install_state, entrypoint, runtime, is_active, trust_level)
VALUES (?, ?, '1.0.0', 'installed', './plugin.wasm', 'wasm', 1, 'trusted')`,
		pluginID, "Sync Tax Plugin "+pluginID); err != nil {
		t.Fatalf("seed plugins: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
VALUES (?, ?, 'tax.rate.ask', 'tax.rate', 1)`, "hook-"+pluginID, pluginID); err != nil {
		t.Fatalf("seed plugin_hooks: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
VALUES (?, ?, 'events:receive', 1)`, "perm-"+pluginID, pluginID); err != nil {
		t.Fatalf("seed plugin_permissions: %v", err)
	}
	if err := plugins.NewInstallStatusStore(dp.Db).Save(ctx, plugins.InstallStatusRecord{
		ListingID: listingID, PluginID: pluginID, PluginName: "Sync Tax Plugin " + pluginID,
		CurrentVersion: "1.0.0", State: plugins.InstallStateActive,
	}); err != nil {
		t.Fatalf("seed install status: %v", err)
	}
}

// pluginInstallState reads a plugin's install_state straight from the row.
func pluginInstallState(t *testing.T, dp *common.Deps, pluginID string) string {
	t.Helper()
	var state string
	if err := dp.Db.QueryRowContext(t.Context(),
		`SELECT COALESCE(install_state, '') FROM plugins WHERE id = ?`, pluginID).Scan(&state); err != nil {
		t.Fatalf("read install_state: %v", err)
	}
	return state
}

// pluginsPageBody renders GET /plugins for dp and returns the body — the
// Alpine JSON payload inside it carries each plugin's "state", which drives
// the Broken ⚠ chip.
func pluginsPageBody(t *testing.T, dp *common.Deps) string {
	t.Helper()
	mux := http.NewServeMux()
	registerPluginsPage(mux, dp)
	req := httptest.NewRequest(http.MethodGet, "/plugins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugins: %d (%s)", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// The full ut-docs#368 story on the real two-till harness: a replica's join
// snapshot registers a wasm tax plugin whose FILE never arrived (rows, not
// bytes). Boot-time Sync must mark it broken (visible state + a real tally,
// not a silent continue); a checkout on a line the plugin owns must fail
// CLOSED with a translated message (never silently fall back to the base
// rate — that's mis-taxed sales); and one ordinary sync tick must self-heal
// the till end to end: verified re-install from the marketplace, row back
// to 'installed', tax answering again, Broken chip gone.
func TestSyncPullTick_JoinSnapshotBrokenTaxPluginFailsClosedThenSelfHeals(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{})
	primary := newSyncPluginsPrimary(t, mkt)
	guest := buildTaxAskGuest(t)
	mkt.publishWasmTaxVersion(t, "listing-tax", "com.test.sync-tax", "1.0.0", guest)
	replica := newSyncPluginsReplica(t, mkt, primary.server.URL)
	client := &http.Client{Timeout: 5 * time.Second}
	ctx := t.Context()

	// The primary installs the tax plugin for real (its own files, its own
	// tree) — this is what puts the listing in the primary's registry dump.
	primary.install(t, "listing-tax")

	// The replica "joined": every DB row landed, no file did.
	seedJoinSnapshotTaxPlugin(t, replica.dp, "listing-tax", "com.test.sync-tax")
	if replica.hasPluginFiles("com.test.sync-tax") {
		t.Fatal("precondition: the replica must have NO plugin files")
	}

	// (1) Boot-time load: Sync marks it broken and counts the failure.
	start := time.Now()
	replica.dp.Pm.Wasm.Sync(ctx, replica.dp.Db)
	if got := pluginInstallState(t, replica.dp, "com.test.sync-tax"); got != "broken" {
		t.Fatalf("expected install_state 'broken' after the failed load, got %q", got)
	}
	if state, ok := installStatusState(t, replica.dp, "listing-tax"); !ok || state != string(plugins.InstallStateFailed) {
		t.Fatalf("expected a Failed install-status record while broken, got (%q, %v)", state, ok)
	}
	foundTally := false
	for _, p := range logging.Recent() {
		if p.At.After(start.Add(-time.Second)) && strings.Contains(p.Msg, "wasm sync:") &&
			strings.Contains(p.Msg, "1 failed") && strings.Contains(p.Msg, "com.test.sync-tax") {
			foundTally = true
			break
		}
	}
	if !foundTally {
		t.Fatal("expected the wasm sync tally to count and name the failed load")
	}
	// The Broken chip's data is on the plugins page.
	if body := pluginsPageBody(t, replica.dp); !strings.Contains(body, `"state":"broken"`) {
		t.Fatalf("expected the plugins page payload to carry state broken, got: %s", body)
	}

	// (2) Checkout on a line whose tax code that plugin owns: blocked with
	// the translated message, and NO sale recorded. The item is a real
	// catalog row — sale_lines FKs items, and the post-heal checkout below
	// must complete for real.
	itemID, err := data.NewCatalogRepo(replica.dp.Db).CreateItem(ctx, catalogtypes.ItemInput{
		Name: "Taxed Widget", BasePrice: 1000, IsActive: true,
	})
	if err != nil {
		t.Fatalf("seed item: %v", err)
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, stubResolver{
		"TAXED": {SKU: "TAXED", Name: "Taxed Widget", Qty: 1, PriceCents: 1000, ItemID: itemID, TaxCodeID: "tax_std", TaxRateBP: 2000},
	})
	engine.SetTaxRateAsker(&pluginTaxRateAsker{db: replica.dp.Db})
	replica.dp.Engine = engine
	posMux := http.NewServeMux()
	registerPOSAPI(posMux, replica.dp)
	if _, err := engine.Scan("TAXED"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	salesBefore := countSalesRows(t, replica.dp)
	rec := posPostForm(posMux, "/api/pos/tender", "method=cash&amount=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the toast-rendered basket (200), got %d: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "tax plugin is being repaired") {
		t.Fatalf("expected the translated tax-unavailable message, got: %s", body)
	}
	if got := countSalesRows(t, replica.dp); got != salesBefore {
		t.Fatalf("a blocked checkout must record NO sale (silent base-rate fallback is the bug): %d -> %d", salesBefore, got)
	}

	// (3) One ordinary sync tick self-heals: verified re-fetch from the
	// marketplace, pinned to the primary's version.
	hitsBefore := mkt.downloadTokenHits()
	replica.tick(t, client)
	if mkt.downloadTokenHits() != hitsBefore+1 {
		t.Fatalf("expected exactly one marketplace re-download to heal the broken plugin, hits %d -> %d",
			hitsBefore, mkt.downloadTokenHits())
	}
	if got := pluginInstallState(t, replica.dp, "com.test.sync-tax"); got != "installed" {
		t.Fatalf("expected install_state back to 'installed' after the healing tick, got %q", got)
	}
	if state, ok := installStatusState(t, replica.dp, "listing-tax"); !ok || state != string(plugins.InstallStateActive) {
		t.Fatalf("expected the install-status record back to Active, got (%q, %v)", state, ok)
	}
	if !replica.hasPluginFiles("com.test.sync-tax") {
		t.Fatal("expected the plugin's files re-fetched into the replica's own tree")
	}
	// The Broken chip is gone.
	if body := pluginsPageBody(t, replica.dp); strings.Contains(body, `"state":"broken"`) {
		t.Fatalf("expected no broken state on the plugins page after heal, got: %s", body)
	}

	// (4) Tax resumes working normally: the REAL wasm module answers the
	// ask (900bp override), and the same checkout now completes.
	rate, ok, blocked := (&pluginTaxRateAsker{db: replica.dp.Db}).AskTaxRateBP(
		pos.BasketLine{ItemID: "itm-taxed", TaxCodeID: "tax_std", TaxRateBP: 2000}, "")
	if !ok || rate != 900 || blocked {
		t.Fatalf("expected the healed plugin's real answer (900,true,false), got (%d,%v,blocked=%v)", rate, ok, blocked)
	}
	rec = posPostForm(posMux, "/api/pos/tender", "method=cash&amount=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the checkout to complete after heal, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countSalesRows(t, replica.dp); got != salesBefore+1 {
		t.Fatalf("expected the sale recorded after heal: %d -> %d", salesBefore, got)
	}
}
