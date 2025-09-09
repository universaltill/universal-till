package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/common"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

var version = "0.1.0"

func main() {
	cfg := common.ConfigFromEnv()
	logger := log.New(os.Stdout, "[edge] ", log.LstdFlags)

	// Data dir for buttons.json
	dataDir := "./data"
	_ = os.MkdirAll(dataDir, 0o755)

	// Buttons: store
	btnStore := ui.NewButtonStore(dataDir)

	// Settings store
	preferSQLite := os.Getenv("UT_STORE") == "sqlite"
	settings := common.NewSettingsStore(dataDir, preferSQLite)

	// If SQLite is enabled and a legacy buttons.json exists, migrate once
	if os.Getenv("UT_STORE") == "sqlite" {
		legacyPath := filepath.Join(dataDir, "buttons.json")
		if b, err := os.ReadFile(legacyPath); err == nil && len(b) > 0 {
			var list []ui.Button
			if err := json.Unmarshal(b, &list); err == nil {
				if s, ok := btnStore.(*ui.SQLiteButtonStore); ok {
					_ = s.Save(list)
					_ = os.Rename(legacyPath, legacyPath+".migrated")
					log.Printf("migrated %d buttons to sqlite", len(list))
				}
			}
		}
	}

	// i18n
	i18n, err := common.NewI18n(filepath.Join("web", "locales"), cfg.DefaultLocale)
	if err != nil {
		logger.Fatalf("failed to load locales: %v", err)
	}
	httpx.InitI18n(i18n, cfg.DefaultLocale)
	httpx.InitCurrency(cfg.Currency)

	// POS engine uses buttons store for prices
	resolver := ui.PriceResolverAdapter{Store: btnStore}
	engine := pos.NewServiceWithResolver(pos.Config{TaxInclusive: cfg.TaxInclusive}, resolver)

	mux := httpx.NewMux()

	// Static (CSS/JS)
	mux.Handle("/public/", http.StripPrefix("/public/", http.FileServer(http.Dir("web/public"))))
	if cfg.SamplesDir != "" {
		mux.Handle("/samples/", http.StripPrefix("/samples/", http.FileServer(http.Dir(cfg.SamplesDir))))
	}

	// Pages
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{
			"title":   "Universal Till",
			"samples": cfg.SamplesDir != "",
			"theme":   settings.GetTheme(),
		}
		httpx.Render("ui/pages/index.html", data)(w, r)
	})
	mux.HandleFunc("/designer", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{
			"title": "Designer",
			"theme": settings.GetTheme(),
		}
		httpx.Render("ui/pages/designer.html", data)(w, r)
	})
	mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{
			"title":    "Settings",
			"theme":    settings.GetTheme(),
			"settings": settings.GetAll(),
		}
		httpx.Render("ui/pages/settings.html", data)(w, r)
	})

	// Set theme
	mux.HandleFunc("/api/settings/theme", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		_ = settings.SetTheme(r.Form.Get("theme"))
		w.WriteHeader(http.StatusNoContent)
	})

	// UI fragments (GET) — construct renderers with request-scoped funcs
	mux.HandleFunc("/ui/buttons", func(w http.ResponseWriter, r *http.Request) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		renderer, err := ui.NewRenderer(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "index.html"),
			filepath.Join("web", "ui", "partials", "buttons.html"),
			funcs,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		btnHTTP := &ui.ButtonsHTTP{Store: btnStore, View: renderer}
		btnHTTP.List(w, r)
	})
	mux.HandleFunc("/ui/basket", func(w http.ResponseWriter, r *http.Request) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, err := ui.NewBasketView(funcs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		b, _ := engine.Scan("")
		_ = basketView.Render(w, b)
	})

	// Buttons admin (POST)
	mux.HandleFunc("/api/buttons/add", func(w http.ResponseWriter, r *http.Request) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		renderer, err := ui.NewRenderer(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "index.html"),
			filepath.Join("web", "ui", "partials", "buttons_admin.html"),
			funcs,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		btnHTTP := &ui.ButtonsHTTP{Store: btnStore, View: renderer}
		btnHTTP.Add(w, r)
	})
	mux.HandleFunc("/api/buttons/remove", func(w http.ResponseWriter, r *http.Request) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		renderer, err := ui.NewRenderer(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "index.html"),
			filepath.Join("web", "ui", "partials", "buttons_admin.html"),
			funcs,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		btnHTTP := &ui.ButtonsHTTP{Store: btnStore, View: renderer}
		btnHTTP.Remove(w, r)
	})

	// POS actions
	mux.HandleFunc("/api/pos/scan", func(w http.ResponseWriter, r *http.Request) {
		code := ""
		qty := 1
		if r.Header.Get("Content-Type") == "application/json" {
			type In struct {
				Code string `json:"code"`
				Qty  int    `json:"qty"`
			}
			var in In
			_ = json.NewDecoder(r.Body).Decode(&in)
			code = in.Code
			if in.Qty > 0 {
				qty = in.Qty
			}
		} else {
			_ = r.ParseForm()
			code = r.Form.Get("code")
			if q := r.Form.Get("qty"); q != "" {
				if v, err := strconv.Atoi(q); err == nil && v > 0 {
					qty = v
				}
			}
		}
		b, _ := engine.ScanQty(code, qty)
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		_ = basketView.Render(w, b)
	})

	mux.HandleFunc("/api/pos/tender", func(w http.ResponseWriter, r *http.Request) {
		type In struct {
			Amount int64  `json:"amount"`
			Method string `json:"method"`
		}
		var in In
		_ = json.NewDecoder(r.Body).Decode(&in)
		_, _ = engine.Tender(in.Amount, in.Method)
		b, _ := engine.Scan("")
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		_ = basketView.Render(w, b)
	})

	mux.HandleFunc("/api/settings/save", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		cur := settings.GetAll()
		if v := strings.TrimSpace(r.Form.Get("currency")); v != "" {
			cur.Currency = v
		}
		if v := strings.TrimSpace(r.Form.Get("country")); v != "" {
			cur.Country = v
		}
		if v := strings.TrimSpace(r.Form.Get("region")); v != "" {
			cur.Region = v
		}
		cur.TaxInclusive = r.Form.Get("taxInclusive") == "on"
		if v := r.Form.Get("taxRatePct"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				cur.TaxRatePct = n
			}
		}
		_ = settings.SetAll(cur)
		// apply immediately
		httpx.InitCurrency(cur.Currency)
		// swap tax engine on next basket compute via new service (simple for now)
		resolver := ui.PriceResolverAdapter{Store: btnStore}
		engine = pos.NewServiceWithResolver(pos.Config{TaxInclusive: cur.TaxInclusive}, resolver)
		w.WriteHeader(http.StatusNoContent)
	})

	logger.Printf("Universal Till edge %s listening on %s\n", version, cfg.ListenAddr)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, mux))
}
