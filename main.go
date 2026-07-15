package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/plugins/oauth"
	"github.com/universaltill/universal-till/internal/server"
	"github.com/universaltill/universal-till/internal/settings"
)

func main() {
	// Cancel the context on Ctrl-C (SIGINT), a service/`kill` (SIGTERM), or a
	// closed terminal (SIGHUP). That triggers the server's graceful shutdown
	// (server.Start) so the till always stops cleanly instead of lingering as
	// an orphaned process when its terminal goes away.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	// Load pos.env if it exists (created during installation/setup)
	// For development, you can manually create pos.env or use UT_ENV=dev for pos.env.dev
	envFile := os.Getenv("UT_ENV_FILE")
	if envFile == "" {
		envFile = "pos.env" // Default production config file
	}
	_ = godotenv.Load(envFile)

	logging.Init()
	log := logging.L()

	log.Infof("Universal Till POS starting...")

	// 1) Config
	cfg, err := config.Init()
	if err != nil {
		log.Fatalf("config init failed: %v", err)
	}

	// 2) DB + migrations. A staged restore (Settings → Backups → Restore)
	// applies first, before the DB is opened; the replaced file is kept
	// in data/backups/.
	if applied, err := db.ApplyPendingRestore(cfg.DBPath); err != nil {
		log.Fatalf("apply staged restore failed: %v", err)
	} else if applied {
		log.Infof("staged backup restore applied to %s", cfg.DBPath)
	}
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("db open/migrate failed: %v", err)
	}
	defer database.Close()

	// Joined-shop replica: the snapshot restore above brought the
	// primary's DB; re-apply THIS till's identity (ADR-0011 D2).
	if applied, err := db.ApplyReplicaIdentity(database.DB, cfg.DBPath); err != nil {
		log.Fatalf("apply replica identity failed: %v", err)
	} else if applied {
		log.Infof("replica identity applied — this till is now part of the shop")
	}

	settingsStore := settings.NewStore(database.DB)

	// Bootstrap: create plugin cache directories (T002 - 009-cloud-marketplace)
	if err := bootstrapPluginDirectories(); err != nil {
		log.Fatalf("failed to create plugin cache directories: %v", err)
	}

	// load runtime config from DB
	settingsStore.LoadRuntimeConfig(ctx, cfg)
	err = settingsStore.SaveRuntimeConfig(ctx, cfg)
	if err != nil { /* handle */
	}

	// 3) Plugins (and pass db if needed)
	pluginManager, err := plugins.Init(ctx, cfg, database.DB)
	if err != nil {
		log.Fatalf("plugin init failed: %v", err)
	}

	// 3b) Marketplace: OAuth client and catalog repository (T004, T010 - 009-cloud-marketplace)
	var catalogRepo *marketplace.CatalogRepository
	if cfg.Marketplace.EndpointURL != "" {
		// Create OAuth token client
		tokenClient := oauth.NewTokenClient(&cfg.Marketplace)

		// Create marketplace HTTP client
		marketplaceClient := marketplace.NewClient(&cfg.Marketplace, tokenClient)

		// Create catalog repository
		var err error
		catalogRepo, err = marketplace.NewCatalogRepository(marketplaceClient, "./data/plugins/cache")
		if err != nil {
			log.Fatalf("failed to create catalog repository: %v", err)
		}
		log.Infof("Marketplace catalog repository initialized (endpoint: %s)", cfg.Marketplace.EndpointURL)
	} else {
		log.Warnf("Marketplace not configured (UT_MARKETPLACE_ENDPOINT_URL not set)")
	}

	// 4) Pages: pass db and catalog repo so handlers can use them
	mux := pages.Init(ctx, cfg, pluginManager, database.DB, catalogRepo)

	// 5) Server with background jobs (T011 - 009-cloud-marketplace)
	// TODO: Initialize supervisor and pass it to server.Start for revocation handling
	if err := server.Start(ctx, cfg, mux, catalogRepo, database.DB, nil); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

// bootstrapPluginDirectories creates required plugin cache directories
// as specified in 009-cloud-marketplace feature (T002)
func bootstrapPluginDirectories() error {
	dirs := []string{
		"./data/plugins/cache",
		"./data/plugins/tmp",
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	return nil
}
