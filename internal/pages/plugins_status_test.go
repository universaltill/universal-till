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

func TestPluginsPageEmbedsLifecycleStatusAndRetryData(t *testing.T) {
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
			{
				ID:            "p1",
				ListingID:     "listing-active",
				Name:          "Active Plugin",
				Version:       "1.2.3",
				Description:   "Shows active state",
				DeveloperID:   "vendor-a",
				CanonicalType: "payment",
				DeviceArch:    runtime.GOOS + "/" + runtime.GOARCH,
			},
			{
				ListingID:     "listing-installing",
				Name:          "Installing Plugin",
				Version:       "2.0.0",
				Description:   "Shows in-flight state",
				DeveloperID:   "vendor-b",
				CanonicalType: "receipt",
				DeviceArch:    runtime.GOOS + "/" + runtime.GOARCH,
			},
			{
				ListingID:     "listing-failed",
				Name:          "Failed Plugin",
				Version:       "3.1.0",
				Description:   "Shows retryable failure",
				DeveloperID:   "vendor-c",
				CanonicalType: "analytics",
				DeviceArch:    runtime.GOOS + "/" + runtime.GOARCH,
			},
			{
				ListingID:     "listing-failed-no-retry",
				Name:          "Failed Non Retryable Plugin",
				Version:       "3.2.0",
				Description:   "Shows non-retryable failure",
				DeveloperID:   "vendor-d",
				CanonicalType: "analytics",
				DeviceArch:    runtime.GOOS + "/" + runtime.GOARCH,
			},
		},
		SnapshotVersion: 1,
		FetchedAt:       time.Now(),
		Locale:          "en",
		DeviceArch:      runtime.GOOS + "/" + runtime.GOARCH,
	})

	statusStore := plugins.NewInstallStatusStore(db)
	if err := statusStore.Save(t.Context(), plugins.InstallStatusRecord{
		ListingID:      "listing-installing",
		PluginName:     "Installing Plugin",
		TargetVersion:  "2.0.0",
		CurrentVersion: "2.0.0",
		State:          plugins.InstallStateInstalling,
	}); err != nil {
		t.Fatalf("save installing status: %v", err)
	}
	if err := statusStore.Save(t.Context(), plugins.InstallStatusRecord{
		ListingID:      "listing-failed",
		PluginName:     "Failed Plugin",
		TargetVersion:  "3.1.0",
		CurrentVersion: "3.1.0",
		State:          plugins.InstallStateFailed,
		MessageKey:     "plugins.install.error.retryable",
		Retryable:      true,
	}); err != nil {
		t.Fatalf("save failed status: %v", err)
	}
	if err := statusStore.Save(t.Context(), plugins.InstallStatusRecord{
		ListingID:      "listing-failed-no-retry",
		PluginName:     "Failed Non Retryable Plugin",
		TargetVersion:  "3.2.0",
		CurrentVersion: "3.2.0",
		State:          plugins.InstallStateFailed,
		MessageKey:     "plugins.install.error.invalid_package",
		Retryable:      false,
	}); err != nil {
		t.Fatalf("save non-retryable failed status: %v", err)
	}

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
	registerPluginsPage(mux, deps)

	req := httptest.NewRequest(http.MethodGet, "/plugins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugins failed: code %d body %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, want := range []string{
		`"id":"listing-active"`,
		`"currentVersion":"1.0"`,
		`"state":"active"`,
		`"state":"installing"`,
		`"state":"failed"`,
		`"statusMessageKey":"plugins.install.error.retryable"`,
		`"statusMessageKey":"plugins.install.error.invalid_package"`,
		`"retryable":true`,
		`"retryable":false`,
		`Current version:`,
		`Retry install`,
		`data-testid="plugin-retry-install"`,
		`/api/plugins/install-from-marketplace`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected plugins page to contain %q", want)
		}
	}
}

func TestInstallFromMarketplaceFailurePersistsOperatorVisibleStatus(t *testing.T) {
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

	var resp struct {
		Success    bool   `json:"success"`
		Message    string `json:"message"`
		MessageKey string `json:"message_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Success {
		t.Fatalf("expected failed install response")
	}
	if resp.MessageKey != "plugins.install.error.configuration" {
		t.Fatalf("message key = %q, want configuration failure", resp.MessageKey)
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
