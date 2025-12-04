package pages

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

func Init(ctx context.Context, cfg *config.Config, pm *plugins.Manager, db *sql.DB) *http.ServeMux {
	log := logging.L()
	mux := http.NewServeMux()

	// settings + state
	setStore := settings.NewStore(db)
	state := common.LoadState(ctx, setStore, cfg)
	// Ensure defaults are persisted (e.g., theme).
	common.SaveState(ctx, setStore, common.RuntimeState{
		Theme:                  state.Theme,
		Currency:               state.Currency,
		Country:                state.Country,
		Region:                 state.Region,
		TaxInclusive:           state.TaxInclusive,
		TaxRatePct:             state.TaxRatePct,
		AllowNegativeInventory: state.AllowNegativeInventory,
	})

	// i18n / currency
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), cfg.Locales.Locale)
	if err != nil {
		log.Fatalf("failed to load locales: %v", err)
	}
	httpx.InitI18n(i18n, cfg.Locales.Locale)
	httpx.InitCurrency(state.Currency)

	btnStore := ui.NewButtonStore(db)
	resolver := ui.PriceResolverAdapter{Store: btnStore}
	engine := pos.NewServiceWithResolver(pos.Config{
		TaxInclusive:       state.TaxInclusive,
		TaxRateBasisPoints: state.TaxRatePct * 100,
	}, resolver)

	baseMenu := []common.MenuItem{
		{Href: "/", Label: "Home"},
		{Href: "/designer", Label: "Designer"},
		{Href: "/inventory", Label: "Inventory"},
		{Href: "/settings", Label: "Settings"},
		{Href: "/plugins", Label: "Plugins"},
		{Href: "/catalog", Label: "Catalog"},
	}

	dp := &common.Deps{
		Cfg:      cfg,
		Pm:       pm,
		Db:       db,
		Settings: setStore,
		State:    state,
		BaseMenu: baseMenu,
		Menu:     common.BuildMenu(baseMenu, pm),
		Engine:   engine,
		BtnStore: btnStore,
	}

	// Register routes
	registerStatic(mux)
	registerIndex(mux, dp)
	registerDesigner(mux, dp)
	registerSettings(mux, dp)
	registerPluginsPage(mux, dp)
	registerPluginAPI(mux, dp)
	registerButtonsAPI(mux, dp)
	registerPOSAPI(mux, dp)
	registerInventoryAPI(mux, dp)
	registerInventoryPage(mux, dp)
	catalog.Register(mux, dp)
	registerBasket(mux, dp)
	registerHealth(mux)
	registerExternalProxy(mux, dp)

	return mux
}
