package pages

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
)

// newStoreAPIMux wires registerPluginStoreAPI over a real-schema DB and an
// isolated plugins dir, with the given marketplace config.
func newStoreAPIMux(t *testing.T, mp config.MarketplaceConfig) (*http.ServeMux, *common.Deps) {
	t.Helper()
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	cfg := basePluginCfg()
	cfg.Marketplace = mp
	d := newPluginAPIDeps(t, db, cfg)
	mux := http.NewServeMux()
	registerPluginStoreAPI(mux, d)
	return mux, d
}

// storeRespOf decodes the store API's { "data": …, "error": null|"…" }
// envelope (universal-till/CLAUDE.md, ut-docs#387) into the (ok, message)
// shape every caller here already expects: ok=true with data.message on
// success, ok=false with the error string on failure.
func storeRespOf(t *testing.T, rec *httptest.ResponseRecorder) (bool, string) {
	t.Helper()
	var body struct {
		Data *struct {
			Message string `json:"message"`
		} `json:"data"`
		Error *string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("store response is not the JSON envelope: %v (%s)", err, rec.Body.String())
	}
	if body.Error != nil {
		return false, *body.Error
	}
	if body.Data != nil {
		return true, body.Data.Message
	}
	return false, ""
}

func TestStoreAPI_MissingListingID_400(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: "http://127.0.0.1:0"})

	for _, path := range []string{
		"/api/plugins/store/download",
		"/api/plugins/store/install",
		"/api/plugins/store/delete-download",
	} {
		rec := postForm(mux, path, url.Values{}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s without listing_id = %d, want 400", path, rec.Code)
		}
		if ok, msg := storeRespOf(t, rec); ok || !strings.Contains(msg, "listing_id") {
			t.Fatalf("%s error envelope wrong: success=%v msg=%q", path, ok, msg)
		}
	}
}

func TestStoreAPI_InstallerNotConfigured_500(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	// An invalid public key makes the manifest verifier (and so the
	// installer) unconstructable — every lifecycle action must fail loudly.
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{
		EndpointURL: "http://127.0.0.1:0",
		PublicKey:   "not-hex-at-all",
	})

	form := url.Values{"listing_id": {"lst-1"}}
	for _, path := range []string{
		"/api/plugins/store/download",
		"/api/plugins/store/install",
		"/api/plugins/store/delete-download",
	} {
		rec := postForm(mux, path, form, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s with broken installer = %d, want 500", path, rec.Code)
		}
		if ok, msg := storeRespOf(t, rec); ok || !strings.Contains(msg, "marketplace not configured") {
			t.Fatalf("%s envelope wrong: success=%v msg=%q", path, ok, msg)
		}
	}
}

func TestStoreAPI_DownloadFailureIs502(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	// A marketplace that answers but has nothing (404 everywhere): the
	// download must fail as a gateway error, with the envelope saying why.
	srv := httptest.NewServer(http.HandlerFunc(http.NotFound))
	t.Cleanup(srv.Close)
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: srv.URL})

	rec := postForm(mux, "/api/plugins/store/download", url.Values{"listing_id": {"lst-1"}}, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("download from empty marketplace = %d, want 502 (%s)", rec.Code, rec.Body.String())
	}
	if ok, msg := storeRespOf(t, rec); ok || !strings.Contains(msg, "download failed") {
		t.Fatalf("download envelope wrong: success=%v msg=%q", ok, msg)
	}
}

func TestStoreAPI_InstallWithoutStagedDownload_400(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{
		EndpointURL: "http://127.0.0.1:0",
		PublicKey:   validStorePublicKey(t),
	})

	rec := postForm(mux, "/api/plugins/store/install", url.Values{"listing_id": {"lst-none"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("install without staged bundle = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if ok, msg := storeRespOf(t, rec); ok || !strings.Contains(msg, "install failed") {
		t.Fatalf("install envelope wrong: success=%v msg=%q", ok, msg)
	}
}

func TestStoreAPI_DeleteDownloadRemovesStagedBundle(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: "http://127.0.0.1:0"})

	// Stage fake download files where the installer keeps them.
	downloads := paths.Plugins("downloads")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		t.Fatalf("mkdir downloads: %v", err)
	}
	bundle := filepath.Join(downloads, "lst-del.tar.gz")
	meta := filepath.Join(downloads, "lst-del.json")
	for _, p := range []string{bundle, meta} {
		if err := os.WriteFile(p, []byte("staged"), 0o644); err != nil {
			t.Fatalf("stage %s: %v", p, err)
		}
	}

	rec := postForm(mux, "/api/plugins/store/delete-download", url.Values{"listing_id": {"lst-del"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete-download = %d (%s)", rec.Code, rec.Body.String())
	}
	for _, p := range []string{bundle, meta} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("staged file %s still present after delete", p)
		}
	}

	// Deleting again (nothing staged) is idempotent, not an error.
	rec = postForm(mux, "/api/plugins/store/delete-download", url.Values{"listing_id": {"lst-del"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("second delete-download = %d, want 200", rec.Code)
	}
}

// Regression (ut-docs#387): the store API's respond closure used to write a
// bare {"success":…, "message":…} body, not the { "data": …, "error":
// null|"…" } envelope universal-till/CLAUDE.md mandates. Pins the raw shape
// directly (storeRespOf above hides it behind a compatibility shim).
func TestStoreAPI_ResponsesUseDataErrorEnvelope(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: "http://127.0.0.1:0"})

	// Failure: data null, error carries the message.
	rec := postForm(mux, "/api/plugins/store/download", url.Values{}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var bad struct {
		Data  any    `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bad); err != nil {
		t.Fatalf("decode error envelope: %v (%s)", err, rec.Body.String())
	}
	if bad.Data != nil {
		t.Fatalf("expected data:null on error, got %v", bad.Data)
	}
	if !strings.Contains(bad.Error, "listing_id") {
		t.Fatalf("expected the validation message in error, got %q", bad.Error)
	}

	// Success: data populated (message under data.message), error null.
	rec = postForm(mux, "/api/plugins/store/delete-download", url.Values{"listing_id": {"lst-envelope"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var ok struct {
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil {
		t.Fatalf("decode success envelope: %v (%s)", err, rec.Body.String())
	}
	if ok.Error != nil {
		t.Fatalf("expected error:null on success, got %v", ok.Error)
	}
	if ok.Data.Message == "" {
		t.Fatalf("expected data.message populated, got %+v", ok)
	}
}

// validStorePublicKey returns some syntactically valid Ed25519 public key hex.
func validStorePublicKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return hex.EncodeToString(pub)
}

// TestStoreAPI_InstallFromStagedBundleSucceeds drives the real "downloaded ->
// installed" transition: a signed asset-only bundle staged exactly where
// DownloadToStore would put it, then POST /api/plugins/store/install.
func TestStoreAPI_InstallFromStagedBundleSucceeds(t *testing.T) {
	t.Setenv("UT_AUTH", "off")

	// A signed, asset-only (runtime "none") manifest — no executable needed.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	m := plugins.Manifest{
		ID:            "com.test.storeinstall",
		Name:          "Store Install Plugin",
		Version:       "1.0.0",
		Runtime:       "none",
		CanonicalType: "theme",
		DeviceArch:    "any",
	}
	canonical, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal canonical: %v", err)
	}
	m.Signature = hex.EncodeToString(ed25519.Sign(priv, canonical))

	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{
		EndpointURL: "http://127.0.0.1:0",
		PublicKey:   hex.EncodeToString(pub),
	})

	// Stage the bundle + metadata exactly as DownloadToStore records them.
	bundleSrc := writePluginBundle(t, m)
	downloads := paths.Plugins("downloads")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		t.Fatalf("mkdir downloads: %v", err)
	}
	bundlePath := filepath.Join(downloads, "lst-store.tar.gz")
	src, err := os.Open(bundleSrc)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	dst, err := os.Create(bundlePath)
	if err != nil {
		t.Fatalf("create staged bundle: %v", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatalf("copy bundle: %v", err)
	}
	src.Close()
	dst.Close()
	sum, err := plugins.ComputeSHA256(bundlePath)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	metaRaw, err := json.Marshal(map[string]any{
		"listing_id":      "lst-store",
		"version":         m.Version,
		"checksum_sha256": sum,
		"signature":       m.Signature,
		"source_url":      "https://marketplace.example/bundles/lst-store.tar.gz",
		"bundle_path":     bundlePath,
		"downloaded_at":   time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(downloads, "lst-store.json"), metaRaw, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	rec := postForm(mux, "/api/plugins/store/install", url.Values{"listing_id": {"lst-store"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("store install = %d (%s)", rec.Code, rec.Body.String())
	}
	if ok, msg := storeRespOf(t, rec); !ok || !strings.Contains(msg, "Store Install Plugin") {
		t.Fatalf("install envelope wrong: success=%v msg=%q", ok, msg)
	}

	// The plugin is persisted, visible to the manager, and status-tracked.
	var name string
	if err := d.Db.QueryRow(`SELECT name FROM plugins WHERE id='com.test.storeinstall'`).Scan(&name); err != nil {
		t.Fatalf("installed plugin row missing: %v", err)
	}
	if name != "Store Install Plugin" {
		t.Fatalf("stored name = %q", name)
	}
	if _, ok := d.Pm.Installed["com.test.storeinstall"]; !ok {
		t.Fatalf("plugin manager not reloaded after store install")
	}
	statuses, err := plugins.NewInstallStatusStore(d.Db).List(t.Context())
	if err != nil {
		t.Fatalf("list install statuses: %v", err)
	}
	st, ok := statuses["lst-store"]
	if !ok || st.State != plugins.InstallStateActive {
		t.Fatalf("install status = %+v, want active for lst-store", st)
	}

	// The staged bundle was consumed by the successful install.
	if _, err := os.Stat(bundlePath); !os.IsNotExist(err) {
		t.Fatalf("staged bundle should be removed after install")
	}
}
