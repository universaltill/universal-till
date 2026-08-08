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
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
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
		{"plugin-bin", []byte("binary"), 0o755},
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

	// Two legacy, UI-unreferenced install paths (review MINOR B) also write
	// a `plugins` row directly and need the same guard: /upload resolves a
	// listing off the marketplace catalog, /marketplace/install takes a
	// caller-supplied URL. Neither is linked from web/ today, but both are
	// live routes a replica could still be told to hit.
	for _, tc := range []struct {
		name, url, body, contentType string
	}{
		{"upload", "/api/plugins/upload", "id=com.test.upload-me", "application/x-www-form-urlencoded"},
		{"marketplace-install", "/api/plugins/marketplace/install",
			"id=com.test.mkt-me&version=1.0.0&package_url=http://example.invalid/x.zip&sha256=deadbeef",
			"application/x-www-form-urlencoded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.url, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusConflict {
				t.Fatalf("expected 409 on a replica, got %d (body=%s)", rec.Code, rec.Body.String())
			}
			if _, errMsg := decode(rec); errMsg != "plugins.install.error.replica_use_primary" {
				t.Fatalf("expected the replica_use_primary message key, got %q", errMsg)
			}
		})
	}

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

// The reload-and-rebuild sequence (Pm.Reload + Menu reassignment) now fires
// from the background sync-pull goroutine every 30s as well as from HTTP
// handlers — Deps.ReloadPlugins must serialize it (PluginMu) so concurrent
// writers can never corrupt the Installed map / Menu (run with -race; the
// unserialized sequence fails the detector). Write-side only by design:
// lock-free READERS of d.Menu/d.Pm.Installed are a separate, pre-existing
// gap outside this test's scope.
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
