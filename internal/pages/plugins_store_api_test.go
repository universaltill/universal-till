package pages

import (
	"context"
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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
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
	// Never dialled: port 1 on loopback is refused at once (ut-docs#5080; port 0 is not dialable).
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: "http://127.0.0.1:1"})

	for _, path := range []string{
		"/api/plugins/store/download",
		"/api/plugins/store/install",
		"/api/plugins/store/delete-download",
	} {
		rec := postForm(mux, path, url.Values{}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s without listing_id = %d, want 400", path, rec.Code)
		}
		if ok, msg := storeRespOf(t, rec); ok || msg != "plugins.install.error.invalid_request" {
			t.Fatalf("%s error envelope wrong: success=%v msg=%q", path, ok, msg)
		}
	}
}

func TestStoreAPI_InstallerNotConfigured_500(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	// An invalid public key makes the manifest verifier (and so the
	// installer) unconstructable — every lifecycle action must fail loudly.
	// Never dialled: port 1 on loopback is refused at once (ut-docs#5080; port 0 is not dialable).
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{
		EndpointURL: "http://127.0.0.1:1",
		PublicKey:   "not-hex-at-all",
	})

	form := url.Values{"listing_id": {"lst-1"}, "accept_terms": {"on"}}
	for _, path := range []string{
		"/api/plugins/store/download",
		"/api/plugins/store/install",
		"/api/plugins/store/delete-download",
	} {
		rec := postForm(mux, path, form, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s with broken installer = %d, want 500", path, rec.Code)
		}
		if ok, msg := storeRespOf(t, rec); ok || msg != "plugins.install.error.configuration" {
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

	rec := postForm(mux, "/api/plugins/store/download", url.Values{"listing_id": {"lst-1"}, "accept_terms": {"on"}}, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("download from empty marketplace = %d, want 502 (%s)", rec.Code, rec.Body.String())
	}
	if ok, msg := storeRespOf(t, rec); ok || msg != "plugins.install.error.download_failed" {
		t.Fatalf("download envelope wrong: success=%v msg=%q", ok, msg)
	}
}

// TestStoreAPI_DownloadNotEntitled_403 pins ut-docs#673's actionable-error
// requirement: a paid listing the store isn't entitled to (or a listing
// still awaiting approval) must surface as the specific
// plugins.install.error.not_entitled i18n key, not the generic
// "download failed: <status>" 502 TestStoreAPI_DownloadFailureIs502 covers.
func TestStoreAPI_DownloadNotEntitled_403(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"data":null,"error":{"code":"not_entitled","message":"store is not entitled to download this plugin"}}`))
	}))
	t.Cleanup(srv.Close)
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: srv.URL})

	rec := postForm(mux, "/api/plugins/store/download", url.Values{"listing_id": {"lst-paid"}, "accept_terms": {"on"}}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("download of a not-entitled listing = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	if ok, msg := storeRespOf(t, rec); ok || msg != "plugins.install.error.not_entitled" {
		t.Fatalf("download envelope wrong: success=%v msg=%q, want the not_entitled i18n key", ok, msg)
	}
}

// TestStoreAPI_DownloadListingNotFound_404 pins ut-docs#703: a listing that
// doesn't exist at all (stale catalog cache, a listing removed from the
// marketplace) must surface as plugins.install.error.not_found, not the
// same not_entitled key a real approval gate uses — there's nothing to
// approve here, just a stale catalog.
func TestStoreAPI_DownloadListingNotFound_404(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"data":null,"error":{"code":"listing_not_found","message":"plugin listing not found"}}`))
	}))
	t.Cleanup(srv.Close)
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: srv.URL})

	rec := postForm(mux, "/api/plugins/store/download", url.Values{"listing_id": {"lst-stale"}, "accept_terms": {"on"}}, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("download of an unknown listing = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	if ok, msg := storeRespOf(t, rec); ok || msg != "plugins.install.error.not_found" {
		t.Fatalf("download envelope wrong: success=%v msg=%q, want the not_found i18n key", ok, msg)
	}
}

// TestStoreAPI_DownloadRegisteredRequired_403 pins ut-docs#704: an anonymous
// till hitting a "registered" access-tier listing (ADR-0013) gets the cloud's
// registered_required code, which must surface as the actionable
// plugins.install.error.registered_required i18n key ("claim your store"),
// not the generic "download failed: <status>" 502.
func TestStoreAPI_DownloadRegisteredRequired_403(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"data":null,"error":{"code":"registered_required","message":"this plugin requires a claimed store account — sign in to claim your store"}}`))
	}))
	t.Cleanup(srv.Close)
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: srv.URL})

	rec := postForm(mux, "/api/plugins/store/download", url.Values{"listing_id": {"lst-registered"}, "accept_terms": {"on"}}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("download of a registered-required listing = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	if ok, msg := storeRespOf(t, rec); ok || msg != "plugins.install.error.registered_required" {
		t.Fatalf("download envelope wrong: success=%v msg=%q, want the registered_required i18n key", ok, msg)
	}
}

// TestStoreAPI_DownloadTillAuthCodes_403 pins ut-docs#3541: the download-token
// route's till-auth answers surface as actionable pairing messages (403, so
// the UI doesn't read it as the operator's own login expiring), not as the raw
// "download failed: marketplace: <code>" 502.
func TestStoreAPI_DownloadTillAuthCodes_403(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	// ut-docs#5187: data.reason picks replaced/moved; an unclaimed store
	// (no plan/subscription settings) is told to register, an owned one to pair.
	for _, tc := range []struct {
		name, code, data, key string
		paidPlan              bool
	}{
		{"store_auth_required", "store_auth_required", "null", "plugins.install.error.pair_required", false},
		{"device_revoked", "device_revoked", "null", "plugins.install.error.till_removed", false},
		{"device_revoked replaced", "device_revoked", `{"reason":"replaced","replaced_by":"till-2"}`, "plugins.install.error.till_replaced", false},
		{"device_revoked moved", "device_revoked", `{"reason":"moved","moved_to":"store-2"}`, "plugins.install.error.till_moved", false},
		{"token_retired owned", "token_retired", "null", "plugins.install.error.pair_again", true},
		{"token_retired anonymous", "token_retired", "null", "plugins.install.error.register_new", false},
		{"unauthorized anonymous", "unauthorized", "null", "plugins.install.error.register_new", false},
		{"device_mismatch anonymous", "device_mismatch", "null", "plugins.install.error.pair_again", false},
		{"service_unavailable", "service_unavailable", "null", "plugins.install.error.service_unavailable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"data":` + tc.data + `,"error":{"code":"` + tc.code + `","message":"nope"}}`))
			}))
			t.Cleanup(srv.Close)
			mux, d := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: srv.URL})
			if tc.paidPlan {
				if err := d.Settings.Set(t.Context(), entitlement.KeyPlan, "shop"); err != nil {
					t.Fatalf("seed plan: %v", err)
				}
			}

			rec := postForm(mux, "/api/plugins/store/download", url.Values{"listing_id": {"lst-paid"}, "accept_terms": {"on"}}, nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("download = %d, want 403 (%s)", rec.Code, rec.Body.String())
			}
			if ok, msg := storeRespOf(t, rec); ok || msg != tc.key {
				t.Fatalf("envelope wrong: success=%v msg=%q, want %q", ok, msg, tc.key)
			}
		})
	}
}

func TestStoreAPI_InstallWithoutStagedDownload_400(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	// Never dialled: port 1 on loopback is refused at once (ut-docs#5080; port 0 is not dialable).
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{
		EndpointURL: "http://127.0.0.1:1",
		PublicKey:   validStorePublicKey(t),
	})

	rec := postForm(mux, "/api/plugins/store/install", url.Values{"listing_id": {"lst-none"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("install without staged bundle = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if ok, msg := storeRespOf(t, rec); ok || msg != "plugins.install.error.retryable" {
		t.Fatalf("install envelope wrong: success=%v msg=%q", ok, msg)
	}
}

func TestStoreAPI_DeleteDownloadRemovesStagedBundle(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	// Never dialled: port 1 on loopback is refused at once (ut-docs#5080; port 0 is not dialable).
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: "http://127.0.0.1:1"})

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
	// Never dialled: port 1 on loopback is refused at once (ut-docs#5080; port 0 is not dialable).
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: "http://127.0.0.1:1"})

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
	if bad.Error != "plugins.install.error.invalid_request" {
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

// signedAssetOnlyManifest returns a signed, asset-only (runtime "none")
// manifest for id/name — no executable needed to install it — plus the hex
// public key that verifies it.
func signedAssetOnlyManifest(t *testing.T, id, name string) (plugins.Manifest, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	m := plugins.Manifest{
		ID:            id,
		Name:          name,
		Version:       "1.0.0",
		Runtime:       "none",
		CanonicalType: "theme",
		DeviceArch:    "any",
	}
	signPluginManifest(t, &m, priv, nil) // writePluginBundle packs manifest.json only
	return m, hex.EncodeToString(pub)
}

// stageSignedStoreBundle writes m as a bundle plus its metadata under
// paths.Plugins("downloads") exactly as DownloadToStore records them, so a
// POST /api/plugins/store/install for listingID finds it. Returns the staged
// bundle path (a successful install consumes it).
func stageSignedStoreBundle(t *testing.T, m plugins.Manifest, listingID string) string {
	t.Helper()
	bundleSrc := writePluginBundle(t, m)
	downloads := paths.Plugins("downloads")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		t.Fatalf("mkdir downloads: %v", err)
	}
	bundlePath := filepath.Join(downloads, listingID+".tar.gz")
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
		"listing_id":      listingID,
		"version":         m.Version,
		"checksum_sha256": sum,
		"signature":       m.Signature,
		"source_url":      "https://marketplace.example/bundles/" + listingID + ".tar.gz",
		"bundle_path":     bundlePath,
		"downloaded_at":   time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(downloads, listingID+".json"), metaRaw, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	return bundlePath
}

// TestStoreAPI_InstallFromStagedBundleSucceeds drives the real "downloaded ->
// installed" transition: a signed asset-only bundle staged exactly where
// DownloadToStore would put it, then POST /api/plugins/store/install.
func TestStoreAPI_InstallFromStagedBundleSucceeds(t *testing.T) {
	t.Setenv("UT_AUTH", "off")

	// A signed, asset-only (runtime "none") manifest — no executable needed.
	m, pubHex := signedAssetOnlyManifest(t, "com.test.storeinstall", "Store Install Plugin")

	// Never dialled: port 1 on loopback is refused at once (ut-docs#5080; port 0 is not dialable).
	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{
		EndpointURL: "http://127.0.0.1:1",
		PublicKey:   pubHex,
	})

	bundlePath := stageSignedStoreBundle(t, m, "lst-store")

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

// TestStoreAPI_InstallFetchesSigningKeyLazily pins ADR-0148 audit item 3
// (ut-docs#3625): no boot-time fetch pins the marketplace signing key any
// more, so the install itself must fetch it. A till with an endpoint but no
// key installs a signed staged bundle, the key is fetched exactly once, and
// a second install reuses it. Without the lazy fetch this install fails
// "marketplace public key not configured".
func TestStoreAPI_InstallFetchesSigningKeyLazily(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	m, pubHex := signedAssetOnlyManifest(t, "com.test.lazykey", "Lazy Key Plugin")

	var keyHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ui/api/signing-key" {
			http.NotFound(w, r)
			return
		}
		keyHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{
			"algorithm":      "ed25519",
			"public_key_hex": pubHex,
		}})
	}))
	t.Cleanup(srv.Close)
	// The fetched key lives in enroll's package state; reset it so no later
	// test sees it.
	enroll.Init(t.Context(), &config.Config{}, emptyKV{}, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, emptyKV{}, &sync.WaitGroup{}) })

	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: srv.URL + "/api"})

	stageSignedStoreBundle(t, m, "lst-lazy")
	rec := postForm(mux, "/api/plugins/store/install", url.Values{"listing_id": {"lst-lazy"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("store install = %d (%s)", rec.Code, rec.Body.String())
	}
	if _, ok := d.Pm.Installed["com.test.lazykey"]; !ok {
		t.Fatal("plugin not installed after the lazy key fetch")
	}
	if n := keyHits.Load(); n != 1 {
		t.Fatalf("signing-key requests = %d, want 1", n)
	}

	stageSignedStoreBundle(t, m, "lst-lazy")
	if rec := postForm(mux, "/api/plugins/store/install", url.Values{"listing_id": {"lst-lazy"}}, nil); rec.Code != http.StatusOK {
		t.Fatalf("second store install = %d (%s)", rec.Code, rec.Body.String())
	}
	if n := keyHits.Load(); n != 1 {
		t.Fatalf("signing-key requests after a second install = %d, want 1 (key reused)", n)
	}
}

func TestStoreAPI_DownloadUnreachable_TranslatedKey(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: "http://127.0.0.1:1"})

	rec := postForm(mux, "/api/plugins/store/download", url.Values{"listing_id": {"lst-1"}, "accept_terms": {"on"}}, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("download with marketplace down = %d, want 502 (%s)", rec.Code, rec.Body.String())
	}
	ok, msg := storeRespOf(t, rec)
	if ok || msg != "plugins.install.error.unreachable" {
		t.Fatalf("envelope wrong: success=%v msg=%q", ok, msg)
	}
	for _, leak := range []string{"127.0.0.1", "dial", "request failed"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("body leaks %q: %s", leak, rec.Body.String())
		}
	}
	// A download is not an install: no install-status record.
	if _, found, err := plugins.NewInstallStatusStore(d.Db).Get(t.Context(), "lst-1"); err != nil || found {
		t.Fatalf("failed download saved a status record (found=%v err=%v)", found, err)
	}
}

func TestStoreAPI_InstallFailure_RecordsFailedStatus(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{
		EndpointURL: "http://127.0.0.1:1",
		PublicKey:   validStorePublicKey(t),
	})

	rec := postForm(mux, "/api/plugins/store/install", url.Values{"listing_id": {"lst-none"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("install = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if _, msg := storeRespOf(t, rec); msg != "plugins.install.error.retryable" {
		t.Fatalf("msg = %q", msg)
	}
	st, found, err := plugins.NewInstallStatusStore(d.Db).Get(t.Context(), "lst-none")
	if err != nil || !found {
		t.Fatalf("no failed record saved (found=%v err=%v)", found, err)
	}
	if st.State != plugins.InstallStateFailed || st.MessageKey != "plugins.install.error.retryable" || !st.Retryable {
		t.Fatalf("record = %+v", st)
	}
}

func TestStoreAPI_InstallFailure_KeepsActiveRecord(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{
		EndpointURL: "http://127.0.0.1:1",
		PublicKey:   validStorePublicKey(t),
	})
	d.Pm.Installed["com.test.kept"] = plugins.Plugin{}
	store := plugins.NewInstallStatusStore(d.Db)
	if err := store.Save(t.Context(), plugins.InstallStatusRecord{
		ListingID: "lst-kept", PluginID: "com.test.kept", PluginName: "Kept",
		CurrentVersion: "1.0.0", State: plugins.InstallStateActive,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := postForm(mux, "/api/plugins/store/install", url.Values{"listing_id": {"lst-kept"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("install = %d (%s)", rec.Code, rec.Body.String())
	}
	st, found, err := store.Get(t.Context(), "lst-kept")
	if err != nil || !found || st.State != plugins.InstallStateActive || st.PluginID != "com.test.kept" {
		t.Fatalf("active record was overwritten: %+v found=%v err=%v", st, found, err)
	}
}

// A stale active record (its plugin is no longer installed) is replaced by
// the failure, so the tile shows the failed tag (ut-docs#6548 review).
func TestStoreAPI_InstallFailure_OverwritesStaleActiveRecord(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{
		EndpointURL: "http://127.0.0.1:1",
		PublicKey:   validStorePublicKey(t),
	})
	store := plugins.NewInstallStatusStore(d.Db)
	if err := store.Save(t.Context(), plugins.InstallStatusRecord{
		ListingID: "lst-stale", PluginID: "com.test.gone", PluginName: "Gone",
		CurrentVersion: "1.0.0", State: plugins.InstallStateActive,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := postForm(mux, "/api/plugins/store/install", url.Values{"listing_id": {"lst-stale"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("install = %d (%s)", rec.Code, rec.Body.String())
	}
	st, found, err := store.Get(t.Context(), "lst-stale")
	if err != nil || !found || st.State != plugins.InstallStateFailed {
		t.Fatalf("stale active record not replaced by failed: %+v found=%v err=%v", st, found, err)
	}
}

func TestStoreAPI_Forbidden_TranslatedKey(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, _ := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: "http://127.0.0.1:1"})
	for _, path := range []string{
		"/api/plugins/store/download",
		"/api/plugins/store/install",
		"/api/plugins/store/delete-download",
	} {
		rec := postForm(mux, path, url.Values{"listing_id": {"lst-1"}}, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s without a session = %d, want 403", path, rec.Code)
		}
		if _, msg := storeRespOf(t, rec); msg != "plugins.install.error.forbidden" {
			t.Fatalf("%s msg = %q", path, msg)
		}
	}
}
