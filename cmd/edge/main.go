package main

import (
    "log"
    "net/http"
    "os"

    "github.com/universaltill/universal-till/internal/common"
    "github.com/universaltill/universal-till/internal/httpx"
    "github.com/universaltill/universal-till/internal/pos"
)

var version = "0.1.0"

func main() {
    cfg := common.ConfigFromEnv()
    logger := log.New(os.Stdout, "[edge] ", log.LstdFlags)

    engine := pos.NewService(pos.Config{TaxInclusive: false})

    mux := httpx.NewMux()
    mux.Handle("/public/", http.StripPrefix("/public/", http.FileServer(http.Dir("web/public"))))
    mux.HandleFunc("/", httpx.Render("ui/pages/index.html", map[string]any{
        "title": "Universal Till",
    }))

    mux.HandleFunc("/api/pos/scan", httpx.JSON(func(in struct{ Code string }) (any, error) {
        return engine.Scan(in.Code)
    }))
    mux.HandleFunc("/api/pos/tender", httpx.JSON(func(in struct {
        Amount int64  `json:"amount"`
        Method string `json:"method"`
    }) (any, error) {
        return engine.Tender(in.Amount, in.Method)
    }))

    addr := cfg.ListenAddr
    logger.Printf("Universal Till edge %s listening on %s\n", version, addr)
    log.Fatal(http.ListenAndServe(addr, mux))
}
