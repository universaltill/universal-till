package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/settings"
)

func TestStorePageShowsLifecycleStatusAndInstalledSplit(t *testing.T) {
	chdirRoot(t)
	initPagesI18n(t)

	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)

	repo := newCatalogRepoWithSnapshot(t, marketplace.CatalogSnapshot{
		Plugins: []marketplace.PluginSummary{
			{ListingID: "listing-active", Name: "Active Plugin", Version: "1.2.3", CanonicalType: "payment"},
			{ListingID: "listing-failed", Name: "Failed Plugin", Version: "3.1.0", CanonicalType: "page"},
			{ListingID: "listing-fresh", Name: "Fresh Plugin", Version: "0.9.0", CanonicalType: "page"},
		},
	})

	statusStore := plugins.NewInstallStatusStore(db)
	// Installed and active (plugin row seeded by seedForPages as com.demo.active).
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('com.demo.active','Active Plugin','1.2.3',1)`); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	if err := statusStore.Save(t.Context(), plugins.InstallStatusRecord{
		ListingID: "listing-active", PluginID: "com.demo.active", CurrentVersion: "1.2.3",
		State: plugins.InstallStateActive,
	}); err != nil {
		t.Fatalf("save active status: %v", err)
	}
	// Failed, retryable: operator must see why and be able to retry.
	if err := statusStore.Save(t.Context(), plugins.InstallStatusRecord{
		ListingID: "listing-failed", State: plugins.InstallStateFailed,
		MessageKey: "plugins.install.error.retryable", Retryable: true,
	}); err != nil {
		t.Fatalf("save failed status: %v", err)
	}

	if err := pm.Reload(t.Context()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	deps := &common.Deps{
		Cfg: cfg, Db: db, State: state, Pm: pm,
		Menu:        []common.MenuItem{{Href: "/plugins", Label: "Plugins"}},
		Settings:    settings.NewStore(db),
		CatalogRepo: repo,
	}

	mux := http.NewServeMux()
	registerPluginStore(mux, deps)

	req := httptest.NewRequest(http.MethodGet, "/plugins/store", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugins/store failed: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{
		// Installed listing: no store actions, points at the manager page.
		"Active Plugin",
		`data-testid="plugin-install-failed"`, // failure chip
		// Failed listing stays actionable: Download retry affordance present.
		`data-store-action="download" data-listing-id="listing-failed"`,
		// Fresh listing is downloadable.
		`data-store-action="download" data-listing-id="listing-fresh"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected store page to contain %q", want)
		}
	}
	// The installed card must NOT offer download/install.
	if strings.Contains(body, `data-store-action="download" data-listing-id="listing-active"`) ||
		strings.Contains(body, `data-store-action="install" data-listing-id="listing-active"`) {
		t.Fatal("installed listing must not offer store actions")
	}
}

func TestInstallFromMarketplaceFailurePersistsOperatorVisibleStatus(t *testing.T) {
	t.Setenv("UT_AUTH", "off") // bypass the manager gate to reach the handler's own logic
	chdirRoot(t)

	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	cfg := &config.Config{
		Theme: "default",
		Locales: config.Locales{
			Currency: "GBP",
			Locale:   "en",
			TaxRate:  20,
		},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://marketplace.test",
			ClientID:    "merchant-1",
			StoreID:     "store-1",
			DeviceID:    "device-1",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)

	repo := newCatalogRepoWithSnapshot(t, marketplace.CatalogSnapshot{
		Plugins: []marketplace.PluginSummary{
			{
				ListingID:     "listing-failure",
				Name:          "Config Failure Plugin",
				Version:       "1.0.0",
				Description:   "Should fail before download",
				DeveloperID:   "vendor-failure",
				CanonicalType: "payment",
				DeviceArch:    runtime.GOOS + "/" + runtime.GOARCH,
			},
		},
		SnapshotVersion: 1,
		FetchedAt:       time.Now(),
		Locale:          "en",
		DeviceArch:      runtime.GOOS + "/" + runtime.GOARCH,
	})

	deps := &common.Deps{
		Cfg:         cfg,
		Db:          db,
		State:       state,
		Menu:        []common.MenuItem{{Href: "/plugins", Label: "Plugins"}},
		Pm:          pm,
		Settings:    settings.NewStore(db),
		CatalogRepo: repo,
	}

	mux := http.NewServeMux()
	registerPluginAPI(mux, deps)

	body, err := json.Marshal(map[string]string{"listing_id": "listing-failure"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/install-from-marketplace", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d body %s", rec.Code, rec.Body.String())
	}

	// Envelope: { "data": null, "error": "…" } (universal-till/CLAUDE.md,
	// ut-docs#387) -- writeInstallResponse puts messageKey in error when no
	// plain message is set, which is every failure call site in this handler.
	var resp struct {
		Data  any    `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data != nil {
		t.Fatalf("expected data:null on a failed install, got %v", resp.Data)
	}
	if resp.Error != "plugins.install.error.configuration" {
		t.Fatalf("error = %q, want configuration failure", resp.Error)
	}

	record, ok, err := plugins.NewInstallStatusStore(db).Get(context.Background(), "listing-failure")
	if err != nil {
		t.Fatalf("load persisted status: %v", err)
	}
	if !ok {
		t.Fatalf("expected persisted failed install status")
	}
	if record.State != plugins.InstallStateFailed {
		t.Fatalf("state = %q, want %q", record.State, plugins.InstallStateFailed)
	}
	if record.MessageKey != "plugins.install.error.configuration" {
		t.Fatalf("status message key = %q", record.MessageKey)
	}
	if record.Retryable {
		t.Fatalf("configuration failure should not be retryable")
	}
}

func initPagesI18n(t *testing.T) {
	t.Helper()

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	httpx.InitCurrency("GBP")
}

func newCatalogRepoWithSnapshot(t *testing.T, snapshot marketplace.CatalogSnapshot) *marketplace.CatalogRepository {
	t.Helper()

	cacheDir := t.TempDir()
	repo, err := marketplace.NewCatalogRepository(nil, cacheDir)
	if err != nil {
		t.Fatalf("new catalog repo: %v", err)
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "catalog-snapshot.json"), data, 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	return repo
}
