package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/plugins/oauth"
	"github.com/universaltill/universal-till/internal/settings"
)

// Regression for "no plugins in the POS": the store must list the catalog for a
// freshly enrolled (anonymous, no-entitlements) till. The bug hid every listing
// because it filtered browse to entitled-only, and a fresh store has none. This
// test renders the real /plugins/store page against a marketplace that returns
// plugins but exposes NO entitlements, and asserts the plugins still appear.
func TestPluginStoreShowsCatalogForAnonymousTill(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()

	// Marketplace: catalog has two plugins; the entitlements endpoint returns an
	// empty-but-successful list (the exact shape that triggered the bug).
	mp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/catalog/plugins":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"plugins":[
				{"id":"0b6d3f5e-6a7c-4e0e-9a51-3f1f6c2d9a01","name":"AI Assistant","version":"1.0.1","type":"integration","trustLevel":"official"},
				{"id":"7c1e2a90-4d3b-4f8a-8b6e-2a5d9c0e1f02","name":"Buttons Left Theme","version":"1.0.2","type":"theme","vendor":"Acme","trustLevel":"unverified","paidListing":true}
			]}`))
		case "/ui/api/merchant/entitlements":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":{"entitled":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mp.Close()

	cfg := &config.Config{
		Theme:         "default",
		DefaultLocale: "en-US",
		Locales:       config.Locales{Currency: "GBP", TaxRate: 20},
		Marketplace:   config.MarketplaceConfig{EndpointURL: mp.URL},
	}
	client := marketplace.NewClient(&cfg.Marketplace, oauth.NewTokenClient(&cfg.Marketplace))
	catalogRepo, err := marketplace.NewCatalogRepository(client, t.TempDir())
	if err != nil {
		t.Fatalf("catalog repo: %v", err)
	}

	dp := &common.Deps{
		Cfg:         cfg,
		Db:          db,
		State:       common.LoadState(t.Context(), settings.NewStore(db), cfg),
		Menu:        []common.MenuItem{{Href: "/", Label: "Home"}},
		Settings:    settings.NewStore(db),
		CatalogRepo: catalogRepo,
	}

	mux := http.NewServeMux()
	registerPluginStore(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/plugins/store", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("store page HTTP %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Trust surfaces: the live catalog sends listing UUIDs as ids (never the
	// com.universaltill.* slug) and marks first-party listings
	// trustLevel "official" → official badge (ut-docs#2647); third-party
	// unverified → unverified badge + consent attributes for the install
	// confirm.
	if !strings.Contains(body, "trust-official") {
		t.Error("first-party plugin missing the official badge")
	}
	if n := strings.Count(body, `data-tier="unverified"`); n != 1 {
		t.Errorf("unverified card count = %d, want exactly 1 (only the third-party listing)", n)
	}
	// ut-docs#2647: browse shows the whole public catalog by design, so the
	// "approval filter unavailable" note was wrong on every visit.
	if strings.Contains(body, "approval filter unavailable") {
		t.Error("store still shows the stale 'approval filter unavailable' banner")
	}
	if !strings.Contains(body, "trust-unverified") {
		t.Error("third-party plugin missing the unverified badge")
	}
	if !strings.Contains(body, `data-tier="unverified"`) || !strings.Contains(body, `data-vendor="Acme"`) {
		t.Error("unverified card missing the consent data attributes")
	}
	if !strings.Contains(body, "utTrustPrompt") {
		t.Error("localized trust prompt missing")
	}
	// ut-docs#673: only the paid listing (Buttons Left Theme) gets the paid
	// badge — the free one (AI Assistant) must not.
	if n := strings.Count(body, "paid-badge"); n != 1 {
		t.Errorf("paid-badge count = %d, want exactly 1 (only the paid listing)", n)
	}
	for _, name := range []string{"AI Assistant", "Buttons Left Theme"} {
		if !strings.Contains(body, name) {
			t.Fatalf("store page hid catalog plugin %q (empty-store regression); body:\n%s", name, body)
		}
	}
}

// ut-docs#2647: the badge must follow the catalog's trust level, since the
// live catalog id is a listing UUID. Only the id prefix or the catalog's
// "official" level makes a plugin official.
func TestTrustTierOf(t *testing.T) {
	cases := []struct{ id, tier, want string }{
		{"0b6d3f5e-6a7c-4e0e-9a51-3f1f6c2d9a01", "official", "official"},
		{"0b6d3f5e-6a7c-4e0e-9a51-3f1f6c2d9a01", " Official ", "official"},
		{"com.universaltill.integration-ai", "unverified", "official"},
		{"7c1e2a90-4d3b-4f8a-8b6e-2a5d9c0e1f02", "verified", "verified"},
		{"7c1e2a90-4d3b-4f8a-8b6e-2a5d9c0e1f02", "trusted", "verified"},
		{"7c1e2a90-4d3b-4f8a-8b6e-2a5d9c0e1f02", "unverified", "unverified"},
		{"7c1e2a90-4d3b-4f8a-8b6e-2a5d9c0e1f02", "", "unverified"},
	}
	for _, c := range cases {
		if got := trustTierOf(c.id, c.tier); got != c.want {
			t.Errorf("trustTierOf(%q, %q) = %q, want %q", c.id, c.tier, got, c.want)
		}
	}
}
