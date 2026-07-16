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
				{"id":"11111111-1111-1111-1111-111111111111","name":"AI Assistant","version":"1.0.1","type":"integration","trustLevel":"unverified"},
				{"id":"22222222-2222-2222-2222-222222222222","name":"Buttons Left Theme","version":"1.0.2","type":"theme","trustLevel":"unverified"}
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
	for _, name := range []string{"AI Assistant", "Buttons Left Theme"} {
		if !strings.Contains(body, name) {
			t.Fatalf("store page hid catalog plugin %q (empty-store regression); body:\n%s", name, body)
		}
	}
}
