package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
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

type menuItem struct {
	Href  string
	Label string
}

func buildMenu(installed map[string]bool, menuPlugins map[string]common.MenuPlugin, recs map[string]common.PluginRecord) []menuItem {
	items := []menuItem{
		{Href: "/", Label: "Home"},
		{Href: "/designer", Label: "Designer"},
		{Href: "/settings", Label: "Settings"},
		{Href: "/plugins", Label: "Plugins"},
	}
	for _, p := range menuPlugins {
		if p.Route != "" && p.Label != "" {
			items = append(items, menuItem{Href: p.Route, Label: p.Label})
		}
	}
	for _, r := range recs {
		if r.Route != "" && r.Label != "" {
			items = append(items, menuItem{Href: r.Route, Label: r.Label})
		}
	}
	return items
}

// plugin helpers
func pluginDir(id string) string { return filepath.Join("data", "plugins", id) }

func pluginDownloaded(id string) bool {
	st, err := os.Stat(filepath.Join(pluginDir(id), "index.html"))
	return err == nil && !st.IsDir()
}

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
		cur := settings.GetAll()
		data := map[string]any{
			"title":     "Universal Till",
			"samples":   cfg.SamplesDir != "",
			"theme":     settings.GetTheme(),
			"menuItems": buildMenu(cur.InstalledPlugins, cur.MenuPlugins, cur.PluginRecords),
		}
		httpx.Render("ui/pages/index.html", data)(w, r)
	})
	mux.HandleFunc("/designer", func(w http.ResponseWriter, r *http.Request) {
		cur := settings.GetAll()
		data := map[string]any{
			"title":     "Designer",
			"theme":     settings.GetTheme(),
			"menuItems": buildMenu(cur.InstalledPlugins, cur.MenuPlugins, cur.PluginRecords),
		}
		httpx.Render("ui/pages/designer.html", data)(w, r)
	})
	mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		cur := settings.GetAll()
		data := map[string]any{
			"title":     "Settings",
			"theme":     settings.GetTheme(),
			"settings":  cur,
			"menuItems": buildMenu(cur.InstalledPlugins, cur.MenuPlugins, cur.PluginRecords),
		}
		httpx.Render("ui/pages/settings.html", data)(w, r)
	})
	mux.HandleFunc("/plugins", func(w http.ResponseWriter, r *http.Request) {
		cur := settings.GetAll()
		// Build installed and downloaded id lists
		installed := []string{}
		if cur.InstalledPlugins != nil {
			for id, ok := range cur.InstalledPlugins {
				if ok {
					installed = append(installed, id)
				}
			}
		}
		downloaded := []string{}
		for id := range cur.PluginRecords {
			if pluginDownloaded(id) {
				downloaded = append(downloaded, id)
			}
		}
		data := map[string]any{
			"title":         "Plugins",
			"theme":         settings.GetTheme(),
			"menuItems":     buildMenu(cur.InstalledPlugins, cur.MenuPlugins, cur.PluginRecords),
			"installedIDs":  installed,
			"downloadedIDs": downloaded,
		}
		httpx.Render("ui/pages/plugins.html", data)(w, r)
	})
	mux.HandleFunc("/faq", func(w http.ResponseWriter, r *http.Request) {
		cur := settings.GetAll()
		if cur.InstalledPlugins == nil || !cur.InstalledPlugins["com.unitill.plugins.faq"] {
			http.NotFound(w, r)
			return
		}
		data := map[string]any{
			"title":     "FAQ",
			"theme":     settings.GetTheme(),
			"menuItems": buildMenu(cur.InstalledPlugins, cur.MenuPlugins, cur.PluginRecords),
		}
		httpx.Render("ui/pages/faq.html", data)(w, r)
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

	// plugin state: downloaded/installed
	mux.HandleFunc("/api/plugins/state", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		cur := settings.GetAll()
		installed := cur.InstalledPlugins != nil && cur.InstalledPlugins[id]
		downloaded := pluginDownloaded(id)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"installed":%v,"downloaded":%v}`, installed, downloaded)))
	})

	// download bundle to local folder
	mux.HandleFunc("/api/plugins/download", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("id"))
		bundleURL := strings.TrimSpace(r.Form.Get("bundleUrl"))
		if id == "" || bundleURL == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		dir := pluginDir(id)
		_ = os.MkdirAll(dir, 0o755)
		// local copy or remote fetch
		if strings.HasPrefix(bundleURL, "/") {
			src := bundleURL
			if strings.HasPrefix(src, "/public/") {
				src = filepath.Join("web", src[1:])
			}
			in, err := os.Open(src)
			if err == nil {
				defer in.Close()
				out, err := os.Create(filepath.Join(dir, "index.html"))
				if err == nil {
					io.Copy(out, in)
					out.Close()
				}
			}
		} else if strings.HasPrefix(bundleURL, "http://") || strings.HasPrefix(bundleURL, "https://") {
			resp, err := http.Get(bundleURL)
			if err == nil {
				defer resp.Body.Close()
				out, err := os.Create(filepath.Join(dir, "index.html"))
				if err == nil {
					io.Copy(out, resp.Body)
					out.Close()
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// install: require downloaded, then register
	mux.HandleFunc("/api/plugins/install", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("id"))
		route := strings.TrimSpace(r.Form.Get("route"))
		label := strings.TrimSpace(r.Form.Get("label"))
		if id == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !pluginDownloaded(id) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		cur := settings.GetAll()
		if cur.InstalledPlugins == nil {
			cur.InstalledPlugins = map[string]bool{}
		}
		cur.InstalledPlugins[id] = true
		if cur.PluginRecords == nil {
			cur.PluginRecords = map[string]common.PluginRecord{}
		}
		if route == "" {
			route = "/plug/" + id
		}
		if label == "" {
			label = id
		}
		cur.PluginRecords[id] = common.PluginRecord{Route: route, Label: label, Path: pluginDir(id)}
		_ = settings.SetAll(cur)
		w.WriteHeader(http.StatusNoContent)
	})

	// uninstall: deregister (keep files)
	mux.HandleFunc("/api/plugins/uninstall", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("id"))
		if id == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		cur := settings.GetAll()
		if cur.InstalledPlugins != nil {
			delete(cur.InstalledPlugins, id)
		}
		if cur.PluginRecords != nil {
			delete(cur.PluginRecords, id)
		}
		_ = settings.SetAll(cur)
		w.WriteHeader(http.StatusNoContent)
	})

	// delete: remove files (and unregister if present)
	mux.HandleFunc("/api/plugins/delete", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("id"))
		if id == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		cur := settings.GetAll()
		if cur.InstalledPlugins != nil {
			delete(cur.InstalledPlugins, id)
		}
		if cur.PluginRecords != nil {
			delete(cur.PluginRecords, id)
		}
		_ = settings.SetAll(cur)
		_ = os.RemoveAll(pluginDir(id))
		w.WriteHeader(http.StatusNoContent)
	})

	// Serve local plugin pages
	mux.HandleFunc("/plug/", func(w http.ResponseWriter, r *http.Request) {
		cur := settings.GetAll()
		pid := strings.TrimPrefix(r.URL.Path, "/plug/")
		rec := cur.PluginRecords[pid]
		if rec.Path == "" {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(filepath.Join(rec.Path, "index.html"))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		data := map[string]any{
			"title":      rec.Label,
			"theme":      settings.GetTheme(),
			"menuItems":  buildMenu(cur.InstalledPlugins, cur.MenuPlugins, cur.PluginRecords),
			"pluginHTML": template.HTML(string(b)),
		}
		httpx.Render("ui/pages/plugin_embed.html", data)(w, r)
	})

	// Dynamic proxy for external menu plugins: /ext/<pluginId>
	mux.HandleFunc("/ext/", func(w http.ResponseWriter, r *http.Request) {
		cur := settings.GetAll()
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/ext/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}
		pid := parts[0]
		mp := cur.MenuPlugins[pid]
		if mp.URL == "" {
			http.NotFound(w, r)
			return
		}
		resp, err := http.Get(mp.URL)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.Copy(w, resp.Body)
	})

	logger.Printf("Universal Till edge %s listening on %s\n", version, cfg.ListenAddr)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, mux))
}
