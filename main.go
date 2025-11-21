package main

import (
	"context"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/server"
	"github.com/universaltill/universal-till/internal/settings"
)

func main() {
	ctx := context.Background()

	logging.Init()
	log := logging.L()

	log.Infof("Universal Till POS starting...")

	// 1) Config
	cfg, err := config.Init()
	if err != nil {
		log.Fatalf("config init failed: %v", err)
	}

	// 2) DB + migrations
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("db open/migrate failed: %v", err)
	}
	defer database.Close()

	settingsStore := settings.NewStore(database.DB)

	// load runtime config from DB
	settingsStore.LoadRuntimeConfig(ctx, cfg)
	err = settingsStore.SaveRuntimeConfig(ctx, cfg)
	if err != nil { /* handle */
	}

	// 3) Plugins (and pass db if needed)
	pluginManager, err := plugins.Init(ctx, cfg /*, database*/)
	if err != nil {
		log.Fatalf("plugin init failed: %v", err)
	}

	// 4) Pages: pass db so handlers can use it
	mux := pages.Init(ctx, cfg, pluginManager, database.DB)

	// 5) Server
	if err := server.Start(ctx, cfg, mux); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
