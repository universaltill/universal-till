package pages

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

type deps struct {
	cfg      *config.Config
	pm       *plugins.Manager
	settings *settings.Store
	state    runtimeState
	menu     []menuItem
	engine   *pos.Service
	btnStore *ui.ButtonStore
}

func Init(ctx context.Context, cfg *config.Config, pm *plugins.Manager, db *sql.DB) *http.ServeMux {
	log := logging.L()
	mux := http.NewServeMux()

	// settings + state
	setStore := settings.NewStore(db)
	state := loadState(ctx, setStore, cfg)
	// Ensure defaults are persisted (e.g., theme).
	saveState(ctx, setStore, state)

	// i18n / currency
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), cfg.Locales.Locale)
	if err != nil {
		log.Fatalf("failed to load locales: %v", err)
	}
	httpx.InitI18n(i18n, cfg.Locales.Locale)
	httpx.InitCurrency(state.Currency)

	btnStore := ui.NewButtonStore(db)
	resolver := ui.PriceResolverAdapter{Store: btnStore}
	engine := pos.NewServiceWithResolver(pos.Config{TaxInclusive: state.TaxInclusive}, resolver)

	baseMenu := []menuItem{
		{Href: "/", Label: "Home"},
		{Href: "/designer", Label: "Designer"},
		{Href: "/settings", Label: "Settings"},
		{Href: "/plugins", Label: "Plugins"},
	}

	dp := &deps{
		cfg:      cfg,
		pm:       pm,
		settings: setStore,
		state:    state,
		menu:     buildMenu(baseMenu, pm),
		engine:   engine,
		btnStore: btnStore,
	}

	// Register routes
	registerStatic(mux)
	registerIndex(mux, dp)
	registerDesigner(mux, dp)
	registerSettings(mux, dp)
	registerPluginsPage(mux, dp)
	registerButtonsAPI(mux, dp)
	registerPOSAPI(mux, dp)
	registerBasket(mux, dp)
	registerHealth(mux)
	registerExternalProxy(mux, dp)

	return mux
}
