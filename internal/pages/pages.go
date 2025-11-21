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
	"github.com/universaltill/universal-till/internal/ui"
)

type MenuItem struct {
	Href  string
	Label string
}

func Init(ctx context.Context, cfg *config.Config, pm *plugins.Manager, db *sql.DB) *http.ServeMux {
	mux := http.NewServeMux()
	log := logging.L()

	// i18n
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), cfg.Locales.Locale)
	if err != nil {
		log.Fatalf("failed to load locales: %v", err)
	}
	httpx.InitI18n(i18n, cfg.Locales.Locale)
	httpx.InitCurrency(cfg.Locales.Currency)

	btnStore := ui.NewButtonStore(db)

	// POS engine uses buttons store for prices
	resolver := ui.PriceResolverAdapter{Store: btnStore}
	engine := pos.NewServiceWithResolver(pos.Config{TaxInclusive: cfg.Locales.TaxInclusive}, resolver)

	// TODO: pull existing handlers into here

	menuItems := []MenuItem{
		{Href: "/", Label: "Home"},
		{Href: "/designer", Label: "Designer"},
		{Href: "/settings", Label: "Settings"},
		{Href: "/plugins", Label: "Plugins"},
	}

	var pageData = map[string]any{
		"title":     "Universal Till",
		"theme":     "monarch", //settings.GetTheme(),
		"menuItems": buildMenu(menuItems, pm.MenuPlugins),
		"config":    *cfg,
	}
	var indexPage IPage = IndexPage{data: pageData}
	var settingPage IPage = SettingPage{data: pageData}

	mux.Handle("/public/", http.StripPrefix("/public/", http.FileServer(http.Dir("web/public"))))

	mux.HandleFunc("/", indexPage.handle(cfg, pm))
	mux.HandleFunc("/settings", settingPage.handle(cfg, pm))
	mux.HandleFunc("/healthz", handleHealth())
	mux.HandleFunc("/basket", handleBasket(pm, engine))

	// later: mount POS UI, buttons, basket, etc.

	return mux
}

func buildMenu(menuItems []MenuItem, menuPlugins map[string]plugins.MenuPlugin) []MenuItem {
	items := menuItems
	for _, p := range menuPlugins {
		if p.Route != "" && p.Label != "" {
			items = append(items, MenuItem{Href: p.Route, Label: p.Label})
		}
	}
	return items
}

func handleHealth() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}
}

func handleBasket(pm *plugins.Manager, engine *pos.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, err := ui.NewBasketView(funcs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		b, _ := engine.Scan("")
		_ = basketView.Render(w, b)

	}
}
