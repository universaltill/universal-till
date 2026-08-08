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
// These endpoints (/api/plugins/marketplace, permissions grant/revoke,
// trust) predate the T017 lifecycle handlers and are not wired to any UI
// template. They still ship, so their contract (gates, validation, error
// mapping) is pinned here.
//
// /api/plugins/upload and /api/plugins/marketplace/install used to live
// here too. Both were known half-implementations (ut-docs QUEUE-ARCHIVE.md,
// 2026-07-30 finding): they persisted a manifest DB row but deleted the
// downloaded artifact and extracted nothing, so the "installed" plugin had
// no runnable code — and neither performed any Ed25519 manifest-signature
// verification (they checked only a caller/catalog-supplied SHA256), in
// direct contradiction of this repo's "never run an unverified plugin"
// rule. Confirmed unreferenced by any UI template, JS, or other repo, and
// with the 2026-07-30 finding already recommending removal, ut-docs#480
// removed both rather than bolting Ed25519 + real extraction onto
// intentionally-dead code — see TestLegacyInstallEndpoints_Removed below.

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

// sha256Hex is a shared test helper (also used by sync_plugins_test.go).
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

// --- /api/plugins/upload and /api/plugins/marketplace/install: removed ----
//
// ut-docs#480: both endpoints installed a plugin verifying only a
// caller/catalog-supplied SHA256 — no Ed25519 manifest-signature check at
// all, in direct contradiction of this repo's "never run an unverified
// plugin" rule — and (per the 2026-07-30 QUEUE-ARCHIVE.md finding) never
// actually produced a runnable plugin in the first place, since the
// downloaded artifact was deleted and nothing was ever extracted. Neither
// was reachable from any UI template, JS, or other repo. Rather than bolt
// real Ed25519 verification and extraction onto intentionally-dead code,
// both routes were deleted outright: the marketplace-listing install flow
// they duplicated already exists, fully Ed25519-verified, at
// POST /api/plugins/install-from-marketplace (handleInstallFromMarketplace).
// This is a stronger guarantee than "rejects a bad signature" — there is no
// unverified *legacy* install route left (import-from-file and the store
// API are separate, existing routes, both Ed25519-verified in their normal
// path, unaffected by this change).
// Both requests below are built to be forms the OLD handlers would have
// happily accepted and installed (matching catalog entry + correct
// checksum, or a fully-populated field set) — an unknown id would 404
// under the old code too and prove nothing. Only a request the old code
// would have returned 200 for, now returning 404, demonstrates the route
// itself is gone rather than just still rejecting this particular input.
func TestLegacyInstallEndpoints_Removed(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	artifact := []byte("would-have-installed artifact")
	pluginsList := []legacyCatalogPlugin{{
		ListingID: "com.test.wouldinstall",
		Name:      "Would Install Plugin",
		Version:   "1.0.0",
	}}
	srv, _ := legacyMarketplaceStub(t, pluginsList, map[string][]byte{"com.test.wouldinstall": artifact})
	pluginsList[0].ArtifactURL = srv.URL + "/artifact/com.test.wouldinstall"
	pluginsList[0].SHA256 = sha256Hex(artifact)
	mux, _ := newLegacyMux(t, srv.URL)

	if rec := postForm(mux, "/api/plugins/upload", url.Values{"id": {"com.test.wouldinstall"}}, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("POST /api/plugins/upload (catalog-matched, correct checksum) = %d, want 404 (route must not exist)", rec.Code)
	}

	rec := postForm(mux, "/api/plugins/marketplace/install", url.Values{
		"id":          {"com.test.wouldinstall"},
		"version":     {"1.0.0"},
		"package_url": {pluginsList[0].ArtifactURL},
		"sha256":      {pluginsList[0].SHA256},
	}, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /api/plugins/marketplace/install (fully valid form) = %d, want 404 (route must not exist)", rec.Code)
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
