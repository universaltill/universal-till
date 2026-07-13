package pages

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

func Init(ctx context.Context, cfg *config.Config, pm *plugins.Manager, db *sql.DB, catalogRepo *marketplace.CatalogRepository) http.Handler {
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
	pm.SetLocalizer(i18n) // language-pack plugins merge into the translator
	httpx.InitCurrency(state.Currency)
	// Dedicated till: larger touch targets, no text selection (UT_KIOSK=1).
	httpx.InitKiosk(os.Getenv("UT_KIOSK") == "1")
	// Interface scale: the saved setting wins; UT_UI_SCALE env is the
	// provisioning default for a till that hasn't been configured yet.
	if state.UIScale <= 0 {
		state.UIScale = 1
		if v := strings.TrimSpace(os.Getenv("UT_UI_SCALE")); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				state.UIScale = f
			}
		}
	}
	httpx.InitUIScale(state.UIScale)

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
		{Href: "/shifts", Label: "Shifts"},
		{Href: "/journal", Label: "Journal"},
		{Href: "/reports", Label: "Reports"},
		{Href: "/settings", Label: "Settings"},
		{Href: "/plugins", Label: "Plugins"},
		{Href: "/catalog", Label: "Catalog"},
		{Href: "/users", Label: "Users"},
	}

	// One auth service for the whole till: login, sessions AND manager-PIN
	// approvals share a single device-wide lockout.
	authSvc := auth.NewService(db)

	dp := &common.Deps{
		Cfg:         cfg,
		Pm:          pm,
		Db:          db,
		Settings:    setStore,
		State:       state,
		BaseMenu:    baseMenu,
		Menu:        common.BuildMenu(baseMenu, pm),
		Engine:      engine,
		BtnStore:    btnStore,
		CatalogRepo: catalogRepo,
		AuthSvc:     authSvc,
	}

	// Register routes
	registerStatic(mux)
	registerIndex(mux, dp)
	registerDesigner(mux, dp)
	registerSettings(mux, dp)
	registerThemes(mux, dp)
	registerPluginsPage(mux, dp)
	registerPluginAPI(mux, dp)
	registerPluginPages(mux, dp)
	registerButtonsAPI(mux, dp)
	registerPOSAPI(mux, dp)
	registerHoldAPI(mux, dp)
	registerInventoryAPI(mux, dp)
	registerInventoryPage(mux, dp)
	registerShiftsAPI(mux, dp)
	registerShiftsPage(mux, dp)
	registerReportsPage(mux, dp)
	registerHelp(mux, dp)
	catalog.Register(mux, dp)
	registerBasket(mux, dp)
	registerJournal(mux, dp)
	registerHealth(mux)
	registerExternalProxy(mux, dp)
	registerPluginStore(mux, dp) // Marketplace plugin store
	registerMarketplaceV1Stub(mux, dp)

	// Operator PIN login (docs: architecture/pos-auth.md). UT_AUTH=off is
	// the CI/dev-tooling escape hatch; a real till runs with auth on.
	registerAuth(mux, dp, authSvc)
	registerUsers(mux, dp, authSvc)
	if auth.Disabled(os.Getenv("UT_AUTH")) {
		log.Warnf("UT_AUTH=off — operator login disabled")
		return mux
	}
	return auth.Middleware(mux, authSvc)
}
