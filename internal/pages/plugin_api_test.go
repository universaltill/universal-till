package pages

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/settings"
)

// --- shared helpers for the plugin lifecycle handlers ---------------------

// openRealSchemaPagesDB builds a DB with the real migrated schema. The lifecycle
// write paths (SetPluginActive, PersistManifest, DeletePlugin) touch columns the
// simplified seedForPages fixture doesn't have (e.g. plugins.updated_at,
// installed_from_url), so they need the production schema to exercise the
// success branches rather than only the DB-error branch.
func openRealSchemaPagesDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := appdb.Open(filepath.Join(t.TempDir(), "plugin_api_test.db"))
	if err != nil {
		t.Fatalf("open real-schema db: %v", err)
	}
	t.Cleanup(func() { database.DB.Close() })
	return database.DB
}

// isolatePluginsDir points paths.Plugins() at a throwaway directory so import /
// export / uninstall file operations never touch the repo's ./data tree.
func isolatePluginsDir(t *testing.T) {
	t.Helper()
	orig := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(orig) })
}

func basePluginCfg() *config.Config {
	return &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20},
	}
}

func newPluginAPIDeps(t *testing.T, db *sql.DB, cfg *config.Config) *common.Deps {
	t.Helper()
	if cfg == nil {
		cfg = basePluginCfg()
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	return &common.Deps{
		Cfg:      cfg,
		Db:       db,
		Pm:       pm,
		Settings: settings.NewStore(db),
		Menu:     []common.MenuItem{{Href: "/plugins", Label: "Plugins"}},
	}
}

// seedInstalledPlugin installs a minimal plugin through the real persistence
// path so it shows up in Pm.Installed after a reload.
func seedInstalledPlugin(t *testing.T, db *sql.DB, id, version string) {
	t.Helper()
	m := &plugins.Manifest{
		ID:            id,
		Name:          id,
		Version:       version,
		Entrypoint:    "./plugin",
		Runtime:       "go",
		CanonicalType: "page",
		DeviceArch:    "any",
	}
	if err := plugins.PersistManifest(t.Context(), db, m, plugins.InstallOptions{
		TrustLevel: "untrusted",
		Uploader:   "test",
	}); err != nil {
		t.Fatalf("seed installed plugin %s: %v", id, err)
	}
}

// writePluginBundle serialises m into a manifest.json at the root of a fresh
// .tar.gz — the exact layout Importer.Import expects.
func writePluginBundle(t *testing.T, m plugins.Manifest) string {
	t.Helper()
	bundlePath := filepath.Join(t.TempDir(), "bundle.tar.gz")
	f, err := os.Create(bundlePath)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	manifestBytes, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:     "manifest.json",
		Mode:     0o644,
		Size:     int64(len(manifestBytes)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(manifestBytes); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return bundlePath
}

// importRequest builds the multipart POST the import-from-file handler expects.
func importRequest(t *testing.T, bundlePath, filename string, fields map[string]string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("plugin_file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	src, err := os.Open(bundlePath)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	if _, err := io.Copy(fw, src); err != nil {
		src.Close()
		t.Fatalf("copy bundle: %v", err)
	}
	src.Close()
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/import-from-file", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// signedManifest returns a manifest carrying a valid Ed25519 signature over its
// own canonical form (signature field cleared), plus the hex public key that
// verifies it — mirroring exactly how ManifestVerifier recomputes the canonical
// bytes.
func signedManifest(t *testing.T, id, version string) (plugins.Manifest, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	m := plugins.Manifest{
		ID:            id,
		Name:          id,
		Version:       version,
		Entrypoint:    "./plugin",
		Runtime:       "go",
		CanonicalType: "page",
		DeviceArch:    "any",
	}
	canonical, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal canonical: %v", err)
	}
	m.Signature = hex.EncodeToString(ed25519.Sign(priv, canonical))
	return m, hex.EncodeToString(pub)
}

// --- setPluginActiveHandler (enable / disable) ----------------------------

func TestSetPluginActiveHandler_MissingID_400(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	h := handleEnablePlugin(&common.Deps{})
	req := httptest.NewRequest(http.MethodPost, "/api/plugins//enable", nil)
	req.SetPathValue("id", "")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty id, got %d", rec.Code)
	}
}

func TestSetPluginActiveHandler_DisableThenEnableTogglesInstalled(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	seedInstalledPlugin(t, db, "com.test.toggle", "1.0.0")
	deps := newPluginAPIDeps(t, db, nil)
	if err := deps.Pm.Reload(t.Context()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := deps.Pm.Installed["com.test.toggle"]; !ok {
		t.Fatalf("precondition: plugin should be installed and active")
	}

	mux := http.NewServeMux()
	registerPluginAPI(mux, deps)

	// Disable -> drops out of the active/installed set.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.toggle/disable", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := deps.Pm.Installed["com.test.toggle"]; ok {
		t.Fatalf("disabled plugin must not remain in the active set")
	}

	// Enable -> returns to the active/installed set.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.toggle/enable", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("enable: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := deps.Pm.Installed["com.test.toggle"]; !ok {
		t.Fatalf("re-enabled plugin must return to the active set")
	}
}

// Regression (ut-docs#387): the lifecycle handlers in this file used to
// respond bare {"success":…, "message":…}, not the { "data": …, "error":
// null|"…" } envelope universal-till/CLAUDE.md mandates. Pins both branches
// for setPluginActiveHandler: success carries the message under data with
// error:null, and a genuine failure (closed DB) carries data:null with the
// error message.
func TestSetPluginActiveHandler_ResponseEnvelope(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	seedInstalledPlugin(t, db, "com.test.envelope", "1.0.0")
	deps := newPluginAPIDeps(t, db, nil)
	if err := deps.Pm.Reload(t.Context()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	h := handleDisablePlugin(deps)

	req := httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.envelope/disable", nil)
	req.SetPathValue("id", "com.test.envelope")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var okResp struct {
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &okResp); err != nil {
		t.Fatalf("decode success envelope: %v (%s)", err, rec.Body.String())
	}
	if okResp.Error != nil {
		t.Fatalf("expected error:null on success, got %v", okResp.Error)
	}
	if okResp.Data.Message == "" {
		t.Fatalf("expected data.message populated, got %+v", okResp)
	}

	// Force the DB-error branch: a closed DB makes SetPluginActive fail.
	db.Close()
	req2 := httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.envelope/disable", nil)
	req2.SetPathValue("id", "com.test.envelope")
	rec2 := httptest.NewRecorder()
	h(rec2, req2)
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 with a closed DB, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var errResp struct {
		Data  any    `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error envelope: %v (%s)", err, rec2.Body.String())
	}
	if errResp.Data != nil {
		t.Fatalf("expected data:null on error, got %v", errResp.Data)
	}
	if !strings.Contains(errResp.Error, "failed to disabled plugin") {
		t.Fatalf("expected the failure reason in error, got %q", errResp.Error)
	}
}

// --- handleUninstallPlugin -------------------------------------------------

func TestHandleUninstallPlugin_EmptyID_400(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	h := handleUninstallPlugin(&common.Deps{})
	req := httptest.NewRequest(http.MethodPost, "/api/plugins//uninstall", nil)
	req.SetPathValue("id", "")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty id, got %d", rec.Code)
	}
}

func TestHandleUninstallPlugin_RemovesInstalledPlugin(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	seedInstalledPlugin(t, db, "com.test.remove", "2.0.0")
	deps := newPluginAPIDeps(t, db, nil)
	if err := deps.Pm.Reload(t.Context()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := deps.Pm.Installed["com.test.remove"]; !ok {
		t.Fatalf("precondition: plugin should be installed")
	}

	mux := http.NewServeMux()
	registerPluginAPI(mux, deps)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.remove/uninstall", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("uninstall: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := deps.Pm.Installed["com.test.remove"]; ok {
		t.Fatalf("uninstalled plugin must be gone from the active set")
	}
}

// --- handleUpdatePlugin ----------------------------------------------------

func TestHandleUpdatePlugin_MissingID_400(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	db := openRealSchemaPagesDB(t)
	deps := newPluginAPIDeps(t, db, nil)
	h := handleUpdatePlugin(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/plugins//update", nil)
	req.SetPathValue("id", "")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty id, got %d", rec.Code)
	}
}

func TestHandleUpdatePlugin_NotInstalled_404(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	db := openRealSchemaPagesDB(t)
	deps := newPluginAPIDeps(t, db, nil)
	h := handleUpdatePlugin(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/nope/update", nil)
	req.SetPathValue("id", "nope")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a plugin that isn't installed, got %d", rec.Code)
	}
}

// A manually imported plugin has no marketplace listing recorded in the
// install-status store, so it cannot be updated from the marketplace: the
// handler must 404 rather than blindly reinstalling.
func TestHandleUpdatePlugin_InstalledButNoListing_404(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	seedInstalledPlugin(t, db, "com.test.manual", "1.0.0")
	deps := newPluginAPIDeps(t, db, nil)
	if err := deps.Pm.Reload(t.Context()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	h := handleUpdatePlugin(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.manual/update", nil)
	req.SetPathValue("id", "com.test.manual")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a plugin with no marketplace listing, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "marketplace listing") {
		t.Fatalf("expected an explanatory 'no marketplace listing' message, got %q", rec.Body.String())
	}
}

// --- handleRollbackPlugin --------------------------------------------------

func TestHandleRollbackPlugin_Validation(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	h := handleRollbackPlugin(&common.Deps{})

	t.Run("empty id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/plugins//rollback", strings.NewReader(`{"version":"1.0.0"}`))
		req.SetPathValue("id", "")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for empty id, got %d", rec.Code)
		}
	})

	t.Run("invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/x/rollback", strings.NewReader(`{not json`))
		req.SetPathValue("id", "x")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for invalid body, got %d", rec.Code)
		}
	})

	t.Run("missing version", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/x/rollback", strings.NewReader(`{"version":""}`))
		req.SetPathValue("id", "x")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for missing version, got %d", rec.Code)
		}
	})
}

func TestHandleRollbackPlugin_UnknownVersion_500(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	seedInstalledPlugin(t, db, "com.test.rb", "1.0.0")
	deps := newPluginAPIDeps(t, db, nil)
	h := handleRollbackPlugin(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.rb/rollback", strings.NewReader(`{"version":"9.9.9"}`))
	req.SetPathValue("id", "com.test.rb")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when the target version isn't stored on disk, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleCheckUpdates ----------------------------------------------------

func TestHandleCheckUpdates_NoCatalog_503(t *testing.T) {
	h := handleCheckUpdates(&common.Deps{}) // CatalogRepo nil
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/check-updates", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when the marketplace catalog isn't configured, got %d", rec.Code)
	}
}

func TestHandleCheckUpdates_NoInstalledPlugins_200Empty(t *testing.T) {
	db := openRealSchemaPagesDB(t)
	deps := newPluginAPIDeps(t, db, nil)
	deps.CatalogRepo = newCatalogRepoWithSnapshot(t, marketplace.CatalogSnapshot{})
	h := handleCheckUpdates(deps)
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/check-updates", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Count int `json:"count"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("expected error:null on success, got %v", resp.Error)
	}
	if resp.Data.Count != 0 {
		t.Fatalf("expected 0 updates, got %+v", resp)
	}
}

// --- handleImportFromFile --------------------------------------------------

func TestHandleImportFromFile_MissingFile_400(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	deps := newPluginAPIDeps(t, db, nil)
	mux := http.NewServeMux()
	registerPluginAPI(mux, deps)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("trust_level", "untrusted")
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/import-from-file", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when plugin_file is absent, got %d", rec.Code)
	}
}

// TDD / trust boundary: when a marketplace public key IS configured, importing a
// bundle whose manifest carries an INVALID Ed25519 signature must be rejected —
// CLAUDE.md: "Never run an unverified plugin." The handler previously always
// constructed the verifier with an empty key (NewManifestVerifier("")), so the
// configured key was ignored and the signature was never checked: a tampered
// bundle imported cleanly. This test failed (200 + installed) before the fix
// and passes (400 + not installed) after passing d.Cfg.Marketplace.PublicKey.
func TestHandleImportFromFile_RejectsInvalidSignatureWhenPublicKeyConfigured(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	isolatePluginsDir(t)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	cfg := basePluginCfg()
	cfg.Marketplace.PublicKey = hex.EncodeToString(pub)

	db := openRealSchemaPagesDB(t)
	deps := newPluginAPIDeps(t, db, cfg)
	mux := http.NewServeMux()
	registerPluginAPI(mux, deps)

	m := plugins.Manifest{
		ID:            "com.test.badsig",
		Name:          "Bad Sig",
		Version:       "1.0.0",
		Entrypoint:    "./plugin",
		Runtime:       "go",
		CanonicalType: "page",
		DeviceArch:    "any",
		Signature:     hex.EncodeToString(make([]byte, ed25519.SignatureSize)), // valid hex, wrong signature
	}
	bundle := writePluginBundle(t, m)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, importRequest(t, bundle, "badsig.tar.gz", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid signature against the configured key, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := deps.Pm.Installed["com.test.badsig"]; ok {
		t.Fatalf("a plugin failing signature verification must not be installed")
	}
}

func TestHandleImportFromFile_AcceptsValidSignatureWhenPublicKeyConfigured(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	isolatePluginsDir(t)
	m, pubHex := signedManifest(t, "com.test.goodsig", "1.0.0")
	cfg := basePluginCfg()
	cfg.Marketplace.PublicKey = pubHex

	db := openRealSchemaPagesDB(t)
	deps := newPluginAPIDeps(t, db, cfg)
	mux := http.NewServeMux()
	registerPluginAPI(mux, deps)

	bundle := writePluginBundle(t, m)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, importRequest(t, bundle, "goodsig.tar.gz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a correctly signed bundle, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := deps.Pm.Installed["com.test.goodsig"]; !ok {
		t.Fatalf("a correctly signed plugin should be installed")
	}
}

// With no marketplace public key configured (the dev / offline default), the
// verifier has nothing to check against and an unsigned bundle imports — this
// pins that fallback so the signature-enforcement change above doesn't silently
// break offline provisioning on tills that never set a key.
func TestHandleImportFromFile_NoPublicKeyImportsUnsignedBundle(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	deps := newPluginAPIDeps(t, db, nil) // cfg has empty Marketplace.PublicKey
	mux := http.NewServeMux()
	registerPluginAPI(mux, deps)

	m := plugins.Manifest{
		ID:            "com.test.unsigned",
		Name:          "Unsigned",
		Version:       "1.0.0",
		Entrypoint:    "./plugin",
		Runtime:       "go",
		CanonicalType: "page",
		DeviceArch:    "any",
	}
	bundle := writePluginBundle(t, m)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, importRequest(t, bundle, "unsigned.tar.gz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 importing an unsigned bundle with no key configured, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := deps.Pm.Installed["com.test.unsigned"]; !ok {
		t.Fatalf("unsigned bundle should import when no key is configured")
	}
}

// --- handleExportPlugin (success path) -------------------------------------

// Round-trip: a signed bundle imported through import-from-file lands on disk in
// the layout the exporter reads, so export streams it straight back as a
// downloadable .tar.gz with the bundle checksum header set.
func TestHandleExportPlugin_StreamsImportedBundle(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	isolatePluginsDir(t)
	m, pubHex := signedManifest(t, "com.test.export", "3.1.4")
	cfg := basePluginCfg()
	cfg.Marketplace.PublicKey = pubHex

	db := openRealSchemaPagesDB(t)
	deps := newPluginAPIDeps(t, db, cfg)
	mux := http.NewServeMux()
	registerPluginAPI(mux, deps)

	bundle := writePluginBundle(t, m)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, importRequest(t, bundle, "export.tar.gz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("import precondition failed: %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/com.test.export/export?version=3.1.4", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/gzip" {
		t.Fatalf("expected application/gzip, got %q", ct)
	}
	if rec.Header().Get("X-Bundle-SHA256") == "" {
		t.Fatalf("expected X-Bundle-SHA256 header on the exported bundle")
	}
	if rec.Body.Len() == 0 {
		t.Fatalf("expected a non-empty exported bundle")
	}
}
