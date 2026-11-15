package pages

import (
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#4180: the store is a grid of small tiles. Each tile is ONE link to
// the listing's own page; the description, permission list and action
// buttons live on that page, not on the tile.
func TestPluginStoreTileIsOneLinkToItsDetailPage(t *testing.T) {
	chdirRoot(t)
	initPagesI18n(t)

	items := []storeItem{
		storeItemForTest(storeItem{ListingID: "0b6d3f5e-6a7c", Name: "card reader", Version: "1.0", Type: "payment",
			Description: "Takes contactless payments", Permissions: []string{"http:lan", "storage"}, Tier: "unverified", Vendor: "Acme"}),
		storeItemForTest(storeItem{ListingID: "with icon/slash", Name: "Dark theme", Version: "1.0", Type: "theme",
			Icon: "https://marketplace.test/icons/dark.png", Tier: "official"}),
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/plugins/store", nil)
	httpx.Render("ui/pages/plugins_store.html", map[string]any{
		"title": "Plugin Store", "menuItems": nil, "Items": items, "Categories": storeCategories(items),
	})(rec, req)
	body := rec.Body.String()

	for _, want := range []string{
		`href="/plugins/store/0b6d3f5e-6a7c"`,
		// The listing id is a path segment: a "/" or space in it must not
		// break out of it.
		`href="/plugins/store/with%20icon%2Fslash"`,
		// No icon → lettermark from the name's first letter, uppercased.
		`class="store-lettermark" aria-hidden="true">C</span>`,
		// Icon present → the logo, with the lettermark kept as its fallback.
		`src="https://marketplace.test/icons/dark.png"`,
		`class="store-lettermark" aria-hidden="true" hidden>D</span>`,
		// Search still matches the description.
		`data-desc="Takes contactless payments"`,
		"trust-unverified", "trust-official",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("store grid missing %q", want)
		}
	}
	if got := strings.Count(body, `class="store-tile"`); got != 2 {
		t.Errorf("want 2 tiles, got %d", got)
	}
	// Nothing actionable or long-form on a tile.
	for _, notWant := range []string{`data-store-action`, "perm-desc", "perm-badge", ">Takes contactless payments<",
		"Installing requires manager approval"} {
		if strings.Contains(body, notWant) {
			t.Errorf("store grid tile must not carry %q (it belongs on the detail page)", notWant)
		}
	}
	// The tile's accessible name is the plugin name (aria-labelledby the
	// name element), not the whole tile text.
	if !strings.Contains(body, `aria-labelledby="store-tile-name-0"`) || !strings.Contains(body, `id="store-tile-name-0">card reader</span>`) {
		t.Error("tile link must be labelled by its name element")
	}
}

func storeItemForTest(it storeItem) storeItem {
	it.DetailHref = storeDetailHref(it.ListingID)
	it.Initial = storeInitial(it.Name)
	return it
}

func TestStoreInitial(t *testing.T) {
	for in, want := range map[string]string{"card reader": "C", "  émile": "É", "ابزار": "ا", "": "?", "1Password": "1"} {
		if got := storeInitial(in); got != want {
			t.Errorf("storeInitial(%q) = %q, want %q", in, got, want)
		}
	}
}

// newStoreDetailMux wires the store (list + detail) the way production does
// — next to the plugin settings page whose /plugins/{id}/settings would
// collide with a naive /plugins/store/{id} — over a cached, offline catalog
// snapshot (nil marketplace client: never reaches the network).
func newStoreDetailMux(t *testing.T, plugins ...marketplace.PluginSummary) *http.ServeMux {
	t.Helper()
	mux, _ := newStoreDetailMuxDeps(t, plugins...)
	return mux
}

func newStoreDetailMuxDeps(t *testing.T, plugins ...marketplace.PluginSummary) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	initPagesI18n(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRateBP: 2000}}
	repo := newCatalogRepoWithSnapshot(t, marketplace.CatalogSnapshot{
		Locale: "en-US", DeviceArch: marketplace.DeviceArch(), Plugins: plugins,
	})
	dp := &common.Deps{
		Cfg: cfg, Db: db, State: common.LoadState(t.Context(), settings.NewStore(db), cfg),
		Menu: []common.MenuItem{{Href: "/plugins", Label: "Plugins"}}, Settings: settings.NewStore(db),
		CatalogRepo: repo,
	}
	mux := http.NewServeMux()
	registerPluginSettings(mux, dp)
	registerPluginStore(mux, dp)
	return mux, dp
}

func TestPluginStoreDetailPageRendersEverythingTheCardUsedTo(t *testing.T) {
	t.Setenv("UT_AUTH", "off") // gate: cashier_sale_only_test.go
	mux := newStoreDetailMux(t,
		marketplace.PluginSummary{ID: "7c1e2a90-4d3b", Name: "Card reader", Version: "2.4.0", CanonicalType: "payment",
			Vendor: "Acme", TrustTier: "unverified", PaidListing: true, Description: "Takes contactless payments at the counter.",
			Permissions: []string{"http:lan", "storage"}},
		marketplace.PluginSummary{ID: "other", Name: "Other", Version: "1.0", CanonicalType: "theme"},
	)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins/store/7c1e2a90-4d3b", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET detail = %d: %.800s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`href="/plugins/store"`, // back to the store
		"Card reader", "Acme", "v2.4.0", "Takes contactless payments at the counter.",
		"trust-unverified", "paid-badge", "Payment",
		`class="store-lettermark" aria-hidden="true">C</span>`,
		// Permissions: badges + the visible plain-language line (ADR-0121 §2).
		"perm-badge", "perm-desc-item", html.EscapeString(httpx.T("en", "plugins.permissions.desc.http_lan")),
		// Actions over the same API, with the unverified-publisher consent.
		`data-store-action="download" data-listing-id="7c1e2a90-4d3b"`,
		`data-tier="unverified"`, `data-vendor="Acme"`,
		// The consent text is a plain JS string — no stray literal quotes
		// around it in the confirm() dialog.
		`window.utTrustPrompt = "This plugin is from an unverified publisher: %s.`,
		"'plugins.install.error.not_entitled':",
		"Installing requires manager approval",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
	if strings.Contains(body, ">Other<") {
		t.Error("detail page must show only its own listing")
	}
	if strings.Contains(body, "showModal(") {
		t.Error("no showModal on till surfaces")
	}
}

func TestPluginStoreDetailUnknownListingIs404(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux := newStoreDetailMux(t, marketplace.PluginSummary{ID: "known", Name: "Known", Version: "1.0", CanonicalType: "theme"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins/store/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown listing = %d, want 404", rec.Code)
	}
	if want := httpx.T("en", "plugins.install.error.not_found"); !strings.Contains(rec.Body.String(), html.EscapeString(want)) {
		t.Errorf("404 page lacks %q", want)
	}
}

// Go's ServeMux panics at registration when two patterns overlap with
// neither more specific: GET /plugins/store/{id} and GET /plugins/{id}/settings
// both match /plugins/store/settings. The registration must not panic, the
// store detail must still be served at /plugins/store/{id}, the plugin
// settings page must still route, and other /plugins/x/y paths stay 404.
func TestPluginStoreDetailRouteCoexistsWithPluginSettings(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux := newStoreDetailMux(t, marketplace.PluginSummary{ID: "abc", Name: "Abc", Version: "1.0", CanonicalType: "theme"})
	for path, wantPattern := range map[string]string{
		"/plugins/store/abc":      storeDetailPattern,
		"/plugins/foo/settings":   "GET /plugins/{id}/settings",
		"/plugins/store":          "/plugins/store",
		"/plugins/store/settings": "GET /plugins/{id}/settings", // a plugin with id "store"; never a listing UUID
	} {
		_, got := mux.Handler(httptest.NewRequest(http.MethodGet, path, nil))
		if got != wantPattern {
			t.Errorf("%s routed to %q, want %q", path, got, wantPattern)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins/other/thing", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/plugins/other/thing = %d, want 404", rec.Code)
	}
}
