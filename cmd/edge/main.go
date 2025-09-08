package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"

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

	// Buttons: store + renderer
	btnStore := ui.NewButtonStore(dataDir)
	btnRenderer, err := ui.NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", "buttons.html"),
	)
	if err != nil {
		logger.Fatalf("failed to load templates: %v", err)
	}
	btnHTTP := &ui.ButtonsHTTP{Store: btnStore, View: btnRenderer}

	// Basket fragment renderer
	basketView, err := ui.NewBasketView()
	if err != nil {
		logger.Fatalf("failed to init basket view: %v", err)
	}

	// POS engine
	engine := pos.NewService(pos.Config{TaxInclusive: false})

	mux := httpx.NewMux()

	// Static (CSS/JS)
	mux.Handle("/public/", http.StripPrefix("/public/", http.FileServer(http.Dir("web/public"))))

	// Page
	mux.HandleFunc("/", httpx.Render("ui/pages/index.html", map[string]any{
		"title": "Universal Till",
	}))

	// UI fragments (GET)
	mux.HandleFunc("/ui/buttons", btnHTTP.List)
	mux.HandleFunc("/ui/basket", func(w http.ResponseWriter, r *http.Request) {
		b, _ := engine.Scan("") // no-op to get current basket (or replace with getter)
		_ = basketView.Render(w, b)
	})

	// Buttons admin (POST)
	mux.HandleFunc("/api/buttons/add", btnHTTP.Add)
	mux.HandleFunc("/api/buttons/remove", btnHTTP.Remove)

	// POS actions return basket HTML (so htmx can swap it)
	mux.HandleFunc("/api/pos/scan", func(w http.ResponseWriter, r *http.Request) {
		type In struct{ Code string }
		var in In
		_ = json.NewDecoder(r.Body).Decode(&in)
		b, _ := engine.Scan(in.Code)
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
		b, _ := engine.Scan("") // basket cleared -> render empty basket
		_ = basketView.Render(w, b)
	})

	logger.Printf("Universal Till edge %s listening on %s\n", version, cfg.ListenAddr)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, mux))
}
