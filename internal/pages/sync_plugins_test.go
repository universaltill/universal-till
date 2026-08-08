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

type fakeMktListing struct {
	artifact []byte
	manifest *plugins.Manifest
	checksum string
}

type fakeMarketplace struct {
	server    *httptest.Server
	publicKey ed25519.PublicKey

	mu        sync.Mutex
	tokenHits int // POST /v1/downloads/tokens count — proves (non-)reinstall
	listings  map[string]fakeMktListing
}

func (m *fakeMarketplace) downloadTokenHits() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokenHits
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
	m := &fakeMarketplace{publicKey: publicKey, listings: map[string]fakeMktListing{}}
	for listingID, pluginID := range pluginIDByListing {
		manifest := &plugins.Manifest{
			ID:            pluginID,
			Name:          "Sync Test Plugin " + pluginID,
			Version:       "1.0.0",
			Entrypoint:    "./plugin-bin",
			Executable:    "plugin-bin",
			Runtime:       "go",
			CanonicalType: "page",
			DeviceArch:    runtime.GOOS + "/" + runtime.GOARCH,
		}
		artifact := signedFakeMktArtifact(t, privateKey, manifest)
		m.listings[listingID] = fakeMktListing{
			artifact: artifact,
			manifest: manifest,
			checksum: sha256Hex(artifact),
		}
	}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/downloads/tokens":
			var req struct {
				ListingID string `json:"listing_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			m.mu.Lock()
			m.tokenHits++
			listing, ok := m.listings[req.ListingID]
			m.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"token":               "tok-" + req.ListingID,
					"bundle_url":          m.server.URL + "/bundles/" + req.ListingID + ".tar.gz",
					"release_id":          "release-" + req.ListingID,
					"version":             listing.manifest.Version,
					"checksum_sha256":     listing.checksum,
					"signature":           listing.manifest.Signature,
					"expires_at":          time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
					"resumable_supported": false,
				},
				"error": nil,
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/bundles/"):
			listingID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/bundles/"), ".tar.gz")
			m.mu.Lock()
			listing, ok := m.listings[listingID]
			m.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(listing.artifact)
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

// syncPluginsPrimary is a real primary till behind a real HTTP server, with
// its marketplace config pointed at the fake marketplace.
type syncPluginsPrimary struct {
	dp     *common.Deps
	server *httptest.Server

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

func newSyncPluginsPrimary(t *testing.T, mkt *fakeMarketplace) *syncPluginsPrimary {
	t.Helper()
	dp := newMigratedSyncDeps(t, "primary.db")
	dp.Cfg.Marketplace = mkt.config()
	if _, err := data.NewTillsRepo(dp.Db).InsertTill(t.Context(), "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	p := &syncPluginsPrimary{dp: dp}
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

func newSyncPluginsReplica(t *testing.T, mkt *fakeMarketplace, primaryURL string) *common.Deps {
	t.Helper()
	dp := newPullTestReplica(t, primaryURL)
	dp.Cfg.Marketplace = mkt.config()
	return dp
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

	if _, err := cloudInstallPlugin(ctx, primary.dp, "listing-alpha"); err != nil {
		t.Fatalf("install on primary: %v", err)
	}
	seedFileImportedPlugin(t, primary.dp, "com.test.fileimport")

	hitsAfterPrimaryInstall := mkt.downloadTokenHits()

	// One tick after the fact must converge.
	client := &http.Client{Timeout: 5 * time.Second}
	syncPullTick(ctx, replica, client, func(context.Context) {})

	if v, ok := pluginInstalledVersion(t, replica, "com.test.sync-alpha"); !ok || v != "1.0.0" {
		t.Fatalf("expected the primary's marketplace plugin installed on the replica at 1.0.0, got (%q, %v)", v, ok)
	}
	if state, ok := installStatusState(t, replica, "listing-alpha"); !ok || state != string(plugins.InstallStateActive) {
		t.Fatalf("expected an active install-status record on the replica, got (%q, %v)", state, ok)
	}
	// Loadable: the replica's plugin manager picked it up on reload.
	if _, ok := replica.Pm.Installed["com.test.sync-alpha"]; !ok {
		t.Fatalf("expected the plugin loaded in the replica's plugin manager, got %+v", replica.Pm.Installed)
	}
	// The replica re-fetched from the MARKETPLACE (verified), not the primary.
	if mkt.downloadTokenHits() != hitsAfterPrimaryInstall+1 {
		t.Fatalf("expected exactly one marketplace download for the replica install, hits %d -> %d",
			hitsAfterPrimaryInstall, mkt.downloadTokenHits())
	}
	// File-imported plugin did not propagate.
	if _, ok := pluginInstalledVersion(t, replica, "com.test.fileimport"); ok {
		t.Fatalf("file-imported plugin must NOT propagate to the replica (ut-docs#460 scope boundary)")
	}
	// The tick's plugin work must not skip the stock section that follows.
	if !primary.hit("/api/sync/stock") {
		t.Fatalf("expected the stock sync section to still run after plugin sync")
	}
	if v, _, _ := replica.Settings.Get(ctx, "sync.plugins_version"); v == "" {
		t.Fatalf("expected sync.plugins_version recorded after a converged plugin pull")
	}

	// A second tick with nothing changed must not reinstall (unchanged poll).
	before := mkt.downloadTokenHits()
	syncPullTick(ctx, replica, client, func(context.Context) {})
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
	ctx := t.Context()
	replica := newSyncPluginsReplica(t, mkt, primary.server.URL)
	client := &http.Client{Timeout: 5 * time.Second}

	if _, err := cloudInstallPlugin(ctx, primary.dp, "listing-alpha"); err != nil {
		t.Fatalf("install on primary: %v", err)
	}
	syncPullTick(ctx, replica, client, func(context.Context) {})
	if _, ok := pluginInstalledVersion(t, replica, "com.test.sync-alpha"); !ok {
		t.Fatalf("precondition: replica should have converged to installed")
	}

	// A local file-imported plugin on the REPLICA — never on the primary.
	seedFileImportedPlugin(t, replica, "com.test.localimport")

	// Primary uninstalls while the replica is not ticking.
	if _, err := cloudRemovePlugin(ctx, primary.dp, "com.test.sync-alpha"); err != nil {
		t.Fatalf("uninstall on primary: %v", err)
	}

	syncPullTick(ctx, replica, client, func(context.Context) {})

	if _, ok := pluginInstalledVersion(t, replica, "com.test.sync-alpha"); ok {
		t.Fatalf("expected the plugin uninstalled from the replica after the primary removed it")
	}
	if _, ok := installStatusState(t, replica, "listing-alpha"); ok {
		t.Fatalf("expected the replica's install-status record cleared after uninstall")
	}
	if _, ok := replica.Pm.Installed["com.test.sync-alpha"]; ok {
		t.Fatalf("expected the plugin dropped from the replica's plugin manager")
	}
	// The replica-local file import is untouched (no listing_id on either side).
	if _, ok := pluginInstalledVersion(t, replica, "com.test.localimport"); !ok {
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

	t.Run("import-from-file", func(t *testing.T) {
		var body bytes.Buffer
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/import-from-file", &body)
		req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("expected 409 on a replica, got %d (body=%s)", rec.Code, rec.Body.String())
		}
		if _, errMsg := decode(rec); errMsg != "plugins.install.error.replica_use_primary" {
			t.Fatalf("expected the replica_use_primary message key, got %q", errMsg)
		}
	})

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
