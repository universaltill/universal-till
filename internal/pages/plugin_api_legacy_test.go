package pages

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// --- helpers for the legacy inline handlers in registerPluginAPI ----------
//
// These endpoints (/api/plugins/upload, /api/plugins/marketplace,
// /api/plugins/marketplace/install, permissions grant/revoke, trust) predate
// the T017 lifecycle handlers and are not wired to any UI template. They
// still ship, so their contract (gates, validation, error mapping, install
// side effects) is pinned here. NOTE: the two install endpoints are known
// half-implementations — they persist a manifest row but delete the artifact
// and extract nothing, so the "installed" plugin has no runnable code. That
// gap is a product decision (remove vs finish), tracked in ut-docs/QUEUE.md,
// deliberately not blessed nor "fixed" here.

// legacyCatalogPlugin is one entry the stub marketplace catalog serves.
type legacyCatalogPlugin struct {
	ListingID   string `json:"listing_id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	ArtifactURL string `json:"artifact_url"`
	SHA256      string `json:"sha256"`
}

// legacyMarketplaceStub serves /v1/catalog/plugins with the given entries and
// /artifact/<listing_id> with the given payloads, recording catalog queries.
func legacyMarketplaceStub(t *testing.T, plugins []legacyCatalogPlugin, artifacts map[string][]byte) (*httptest.Server, *url.Values) {
	t.Helper()
	var lastQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/catalog/plugins":
			lastQuery = r.URL.Query()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"plugins": plugins})
		case strings.HasPrefix(r.URL.Path, "/artifact/"):
			id := strings.TrimPrefix(r.URL.Path, "/artifact/")
			body, ok := artifacts[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &lastQuery
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newLegacyMux builds a mux with registerPluginAPI wired to a real-schema DB
// and the given marketplace endpoint.
func newLegacyMux(t *testing.T, endpointURL string) (*http.ServeMux, *common.Deps) {
	t.Helper()
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	cfg := basePluginCfg()
	cfg.Marketplace = config.MarketplaceConfig{EndpointURL: endpointURL}
	d := newPluginAPIDeps(t, db, cfg)
	mux := http.NewServeMux()
	registerPluginAPI(mux, d)
	return mux, d
}

// --- /api/plugins/marketplace (catalog proxy) -----------------------------

func TestLegacyMarketplaceList_ForwardsCatalogAndArch(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	srv, lastQuery := legacyMarketplaceStub(t, []legacyCatalogPlugin{
		{ListingID: "com.test.a", Name: "Plugin A", Version: "1.0.0"},
	}, nil)
	mux, _ := newLegacyMux(t, srv.URL)

	// Default arch: the till's own GOOS/GOARCH.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/marketplace", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET marketplace = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if !strings.Contains(rec.Body.String(), "Plugin A") {
		t.Fatalf("catalog body not forwarded: %s", rec.Body.String())
	}
	wantArch := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
	if got := lastQuery.Get("device_arch"); got != wantArch {
		t.Fatalf("device_arch = %q, want %q", got, wantArch)
	}
	if lastQuery.Get("capability") != "" {
		t.Fatalf("capability sent without being asked: %q", lastQuery.Get("capability"))
	}

	// Explicit os/arch/capability filters pass through.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/marketplace?os=linux&arch=arm64&capability=payment", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered GET = %d, want 200", rec.Code)
	}
	if got := lastQuery.Get("device_arch"); got != "linux/arm64" {
		t.Fatalf("device_arch = %q, want linux/arm64", got)
	}
	if got := lastQuery.Get("capability"); got != "payment" {
		t.Fatalf("capability = %q, want payment", got)
	}
}

func TestLegacyMarketplaceList_MethodAndUpstreamErrors(t *testing.T) {
	t.Setenv("UT_AUTH", "off")

	// Wrong method.
	srv, _ := legacyMarketplaceStub(t, nil, nil)
	mux, _ := newLegacyMux(t, srv.URL)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/plugins/marketplace", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST marketplace = %d, want 405", rec.Code)
	}

	// Upstream non-200 status propagates.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "catalog down", http.StatusBadGateway)
	}))
	t.Cleanup(upstream.Close)
	mux2, _ := newLegacyMux(t, upstream.URL)
	rec = httptest.NewRecorder()
	mux2.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/marketplace", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("upstream 502 propagated as %d, want 502", rec.Code)
	}

	// Unreachable marketplace: 500 with an error message.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close() // now guaranteed-unreachable URL
	mux3, _ := newLegacyMux(t, dead.URL)
	rec = httptest.NewRecorder()
	mux3.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/marketplace", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("unreachable marketplace = %d, want 500", rec.Code)
	}
}

// --- /api/plugins/upload (install by listing id) --------------------------

func TestLegacyUpload_Validation(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	srv, _ := legacyMarketplaceStub(t, nil, nil)
	mux, _ := newLegacyMux(t, srv.URL)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/upload", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET upload = %d, want 405", rec.Code)
	}

	if rec := postForm(mux, "/api/plugins/upload", url.Values{}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing id = %d, want 400", rec.Code)
	}

	// Unknown listing id -> 404 (catalog is empty).
	if rec := postForm(mux, "/api/plugins/upload", url.Values{"id": {"com.test.ghost"}}, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown listing = %d, want 404", rec.Code)
	}
}

func TestLegacyUpload_ChecksumMismatch_400(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	artifact := []byte("real artifact bytes")
	// Catalog entry advertises the WRONG hash for the artifact it serves.
	pluginsList := []legacyCatalogPlugin{{
		ListingID: "com.test.sum",
		Name:      "Sum Plugin",
		Version:   "1.0.0",
		SHA256:    sha256Hex([]byte("different bytes")),
	}}
	srv, _ := legacyMarketplaceStub(t, pluginsList, map[string][]byte{"com.test.sum": artifact})
	// The stub's URL exists only after creation; patch the shared slice.
	pluginsList[0].ArtifactURL = srv.URL + "/artifact/com.test.sum"

	mux, _ := newLegacyMux(t, srv.URL)
	rec := postForm(mux, "/api/plugins/upload", url.Values{"id": {"com.test.sum"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("checksum mismatch = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "checksum mismatch") {
		t.Fatalf("error should say checksum mismatch, got: %s", rec.Body.String())
	}
}

func TestLegacyUpload_InstallsListedPlugin(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	artifact := []byte("legacy artifact payload")
	pluginsList := []legacyCatalogPlugin{{
		ListingID: "com.test.legacyup",
		Name:      "Legacy Upload Plugin",
		Version:   "1.2.3",
		SHA256:    sha256Hex(artifact),
	}}
	srv, _ := legacyMarketplaceStub(t, pluginsList, map[string][]byte{"com.test.legacyup": artifact})
	pluginsList[0].ArtifactURL = srv.URL + "/artifact/com.test.legacyup"

	mux, d := newLegacyMux(t, srv.URL)
	rec := postForm(mux, "/api/plugins/upload", url.Values{"id": {"com.test.legacyup"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload install = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var name, version string
	if err := d.Db.QueryRow(`SELECT name, version FROM plugins WHERE id = 'com.test.legacyup'`).Scan(&name, &version); err != nil {
		t.Fatalf("installed plugin row missing: %v", err)
	}
	if name != "Legacy Upload Plugin" || version != "1.2.3" {
		t.Fatalf("stored name/version = %q/%q, want catalog values", name, version)
	}
	// The install must be visible to the plugin manager after Reload.
	if _, ok := d.Pm.Installed["com.test.legacyup"]; !ok {
		t.Fatalf("plugin manager not reloaded with new install")
	}
}

// A legitimate plugin name containing JSON-special characters must install
// with the exact name preserved. The legacy handler used to synthesize the
// manifest via fmt.Sprintf into a JSON string literal — any quote in the
// catalog name produced invalid JSON (install failed 500), and a crafted
// name could inject arbitrary manifest fields.
func TestLegacyUpload_PluginNameWithQuotesInstalls(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	artifact := []byte("quoted name artifact")
	quoted := `Legacy "Quoted" Plugin`
	pluginsList := []legacyCatalogPlugin{{
		ListingID: "com.test.quoted",
		Name:      quoted,
		Version:   "1.0.0",
		SHA256:    sha256Hex(artifact),
	}}
	srv, _ := legacyMarketplaceStub(t, pluginsList, map[string][]byte{"com.test.quoted": artifact})
	pluginsList[0].ArtifactURL = srv.URL + "/artifact/com.test.quoted"

	mux, d := newLegacyMux(t, srv.URL)
	rec := postForm(mux, "/api/plugins/upload", url.Values{"id": {"com.test.quoted"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("install with quoted name = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var name string
	if err := d.Db.QueryRow(`SELECT name FROM plugins WHERE id = 'com.test.quoted'`).Scan(&name); err != nil {
		t.Fatalf("installed plugin row missing: %v", err)
	}
	if name != quoted {
		t.Fatalf("stored name = %q, want %q", name, quoted)
	}
}

// --- /api/plugins/marketplace/install (install by URL) --------------------

func TestLegacyMarketplaceInstall_Validation(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	srv, _ := legacyMarketplaceStub(t, nil, nil)
	mux, _ := newLegacyMux(t, srv.URL)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/marketplace/install", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET install = %d, want 405", rec.Code)
	}

	// Each required field missing -> 400.
	full := url.Values{
		"id":          {"com.test.x"},
		"version":     {"1.0.0"},
		"package_url": {"http://example.invalid/p.tar.gz"},
		"sha256":      {"abc"},
	}
	for _, missing := range []string{"id", "version", "package_url", "sha256"} {
		form := url.Values{}
		for k, v := range full {
			if k != missing {
				form[k] = v
			}
		}
		if rec := postForm(mux, "/api/plugins/marketplace/install", form, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("missing %s = %d, want 400", missing, rec.Code)
		}
	}
}

// A catalog entry whose artifact URL is dead (404) must fail the install
// loudly — a real-world case whenever a release artifact is pruned.
func TestLegacyInstalls_DeadArtifactURL_500(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	pluginsList := []legacyCatalogPlugin{{
		ListingID: "com.test.deadurl",
		Name:      "Dead URL Plugin",
		Version:   "1.0.0",
		SHA256:    sha256Hex([]byte("whatever")),
	}}
	srv, _ := legacyMarketplaceStub(t, pluginsList, nil) // no artifacts served
	pluginsList[0].ArtifactURL = srv.URL + "/artifact/com.test.deadurl"
	mux, _ := newLegacyMux(t, srv.URL)

	rec := postForm(mux, "/api/plugins/upload", url.Values{"id": {"com.test.deadurl"}}, nil)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "download failed") {
		t.Fatalf("upload with dead artifact = %d (%s), want 500 download failed", rec.Code, rec.Body.String())
	}

	rec = postForm(mux, "/api/plugins/marketplace/install", url.Values{
		"id":          {"com.test.deadurl"},
		"version":     {"1.0.0"},
		"package_url": {srv.URL + "/artifact/com.test.deadurl"},
		"sha256":      {sha256Hex([]byte("whatever"))},
	}, nil)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "download failed") {
		t.Fatalf("marketplace install with dead artifact = %d (%s), want 500 download failed", rec.Code, rec.Body.String())
	}
}

func TestLegacyMarketplaceInstall_ChecksumMismatch_400(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	artifact := []byte("mp install artifact")
	srv, _ := legacyMarketplaceStub(t, nil, map[string][]byte{"com.test.mpsum": artifact})
	mux, _ := newLegacyMux(t, srv.URL)

	rec := postForm(mux, "/api/plugins/marketplace/install", url.Values{
		"id":          {"com.test.mpsum"},
		"version":     {"1.0.0"},
		"package_url": {srv.URL + "/artifact/com.test.mpsum"},
		"sha256":      {sha256Hex([]byte("not the artifact"))},
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("checksum mismatch = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestLegacyMarketplaceInstall_InstallsAndDefaultsTrust(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	artifact := []byte("mp trusted artifact")
	srv, _ := legacyMarketplaceStub(t, nil, map[string][]byte{"com.test.mpok": artifact})
	mux, d := newLegacyMux(t, srv.URL)

	rec := postForm(mux, "/api/plugins/marketplace/install", url.Values{
		"id":          {"com.test.mpok"},
		"version":     {"2.0.0"},
		"package_url": {srv.URL + "/artifact/com.test.mpok"},
		"sha256":      {sha256Hex(artifact)},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("marketplace install = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var trust, version string
	if err := d.Db.QueryRow(`SELECT trust_level, version FROM plugins WHERE id = 'com.test.mpok'`).Scan(&trust, &version); err != nil {
		t.Fatalf("installed plugin row missing: %v", err)
	}
	if trust != "untrusted" {
		t.Fatalf("default trust_level = %q, want untrusted", trust)
	}
	if version != "2.0.0" {
		t.Fatalf("version = %q, want 2.0.0", version)
	}
}

// Same manifest-synthesis bug as the upload path: a version (or id) with a
// quote used to break the fmt.Sprintf-built JSON. Must install verbatim.
func TestLegacyMarketplaceInstall_VersionWithQuoteInstalls(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	artifact := []byte("mp quoted artifact")
	version := `1.0.0-beta"x`
	srv, _ := legacyMarketplaceStub(t, nil, map[string][]byte{"com.test.mpquoted": artifact})
	mux, d := newLegacyMux(t, srv.URL)

	rec := postForm(mux, "/api/plugins/marketplace/install", url.Values{
		"id":          {"com.test.mpquoted"},
		"version":     {version},
		"package_url": {srv.URL + "/artifact/com.test.mpquoted"},
		"sha256":      {sha256Hex(artifact)},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("install with quoted version = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var got string
	if err := d.Db.QueryRow(`SELECT version FROM plugins WHERE id = 'com.test.mpquoted'`).Scan(&got); err != nil {
		t.Fatalf("installed plugin row missing: %v", err)
	}
	if got != version {
		t.Fatalf("stored version = %q, want %q", got, version)
	}
}

// --- permissions grant/revoke and trust level ------------------------------

func TestLegacyPermissions_GrantRevoke(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	srv, _ := legacyMarketplaceStub(t, nil, nil)
	mux, d := newLegacyMux(t, srv.URL)
	seedInstalledPlugin(t, d.Db, "com.test.perms", "1.0.0")

	// Method + validation gates.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/permissions/grant", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET grant = %d, want 405", rec.Code)
	}
	if rec := postForm(mux, "/api/plugins/permissions/grant", url.Values{"plugin_id": {"com.test.perms"}}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("grant without permission = %d, want 400", rec.Code)
	}
	if rec := postForm(mux, "/api/plugins/permissions/revoke", url.Values{"permission": {"pos.read"}}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("revoke without plugin_id = %d, want 400", rec.Code)
	}

	// Granting a permission the plugin never declared must fail — the domain
	// layer only flips pre-declared permission rows, it never invents one.
	undeclared := url.Values{"plugin_id": {"com.test.perms"}, "permission": {"pos.read"}}
	if rec := postForm(mux, "/api/plugins/permissions/grant", undeclared, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("grant of undeclared permission = %d, want 500", rec.Code)
	}

	// Declare the permission (as a manifest install would), then grant.
	if _, err := d.Db.Exec(`INSERT INTO plugin_permissions(id, plugin_id, permission, granted) VALUES ('perm-1','com.test.perms','pos.read',0)`); err != nil {
		t.Fatalf("seed declared permission: %v", err)
	}

	// Grant persists granted=1.
	form := url.Values{"plugin_id": {"com.test.perms"}, "permission": {"pos.read"}}
	if rec := postForm(mux, "/api/plugins/permissions/grant", form, nil); rec.Code != http.StatusOK {
		t.Fatalf("grant = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var granted int
	if err := d.Db.QueryRow(`SELECT granted FROM plugin_permissions WHERE plugin_id='com.test.perms' AND permission='pos.read'`).Scan(&granted); err != nil {
		t.Fatalf("granted permission row missing: %v", err)
	}
	if granted != 1 {
		t.Fatalf("granted = %d, want 1", granted)
	}

	// Revoke flips it back.
	if rec := postForm(mux, "/api/plugins/permissions/revoke", form, nil); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if err := d.Db.QueryRow(`SELECT granted FROM plugin_permissions WHERE plugin_id='com.test.perms' AND permission='pos.read'`).Scan(&granted); err != nil {
		t.Fatalf("permission row missing after revoke: %v", err)
	}
	if granted != 0 {
		t.Fatalf("granted after revoke = %d, want 0", granted)
	}
}

func TestLegacyTrustLevel_UpdateAndValidation(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	srv, _ := legacyMarketplaceStub(t, nil, nil)
	mux, d := newLegacyMux(t, srv.URL)
	seedInstalledPlugin(t, d.Db, "com.test.trust", "1.0.0")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/trust", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET trust = %d, want 405", rec.Code)
	}
	if rec := postForm(mux, "/api/plugins/trust", url.Values{"plugin_id": {"com.test.trust"}}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("trust without level = %d, want 400", rec.Code)
	}
	// An out-of-vocabulary trust level is rejected by the domain layer.
	if rec := postForm(mux, "/api/plugins/trust", url.Values{"plugin_id": {"com.test.trust"}, "trust_level": {"royal"}}, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("invalid trust level = %d, want 500", rec.Code)
	}

	if rec := postForm(mux, "/api/plugins/trust", url.Values{"plugin_id": {"com.test.trust"}, "trust_level": {"trusted"}}, nil); rec.Code != http.StatusOK {
		t.Fatalf("trust update = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var trust string
	if err := d.Db.QueryRow(`SELECT trust_level FROM plugins WHERE id='com.test.trust'`).Scan(&trust); err != nil {
		t.Fatalf("plugin row missing: %v", err)
	}
	if trust != "trusted" {
		t.Fatalf("trust_level = %q, want trusted", trust)
	}
}
