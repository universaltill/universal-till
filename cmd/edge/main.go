package main

import (
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

	// Create data dir for storing buttons.json if not present
	dataDir := "./data"
	_ = os.MkdirAll(dataDir, 0o755)

	// Initialize button store + renderer
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

	// Initialize POS service
	engine := pos.NewService(pos.Config{TaxInclusive: false})

	mux := httpx.NewMux()

	// Static files (CSS, JS)
	mux.Handle("/public/", http.StripPrefix("/public/", http.FileServer(http.Dir("web/public"))))

	// Root page
	mux.HandleFunc("/", httpx.Render("ui/pages/index.html", map[string]any{
		"title": "Universal Till",
	}))

	// Buttons management endpoints
	mux.HandleFunc("/ui/buttons", btnHTTP.List)
	mux.HandleFunc("/api/buttons/add", btnHTTP.Add)
	mux.HandleFunc("/api/buttons/remove", btnHTTP.Remove)

	// POS API endpoints
	mux.HandleFunc("/api/pos/scan", httpx.JSON(func(in struct{ Code string }) (any, error) {
		return engine.Scan(in.Code)
	}))
	mux.HandleFunc("/api/pos/tender", httpx.JSON(func(in struct {
		Amount int64  `json:"amount"`
		Method string `json:"method"`
	}) (any, error) {
		return engine.Tender(in.Amount, in.Method)
	}))

	logger.Printf("Universal Till edge %s listening on %s\n", version, cfg.ListenAddr)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, mux))
}
