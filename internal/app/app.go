// Package app is the till's full boot sequence — config, DB/migrations,
// plugin host, marketplace enrolment, pages/mux, background jobs, and the
// HTTP server — factored out of main.go so it can be driven by more than
// one entry point. cmd/unitill-desktop already proves the shared-core
// pattern for desktop shells (spawn the same server binary, point a native
// WebView at localhost); mobile/mobile.go (ADR-0023, spec: the Android/iOS
// gomobile-bind shell) needs the identical boot sequence run IN-PROCESS
// instead, since a mobile app can't spawn a sibling binary — this package
// is what makes that possible without duplicating main.go's ~150 lines.
package app

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/joho/godotenv"
	"github.com/universaltill/universal-till/internal/alerts"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/plugins/oauth"
	"github.com/universaltill/universal-till/internal/server"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/updates"
)

// Run boots the till and blocks serving HTTP until ctx is cancelled (or a
// fatal startup error occurs). Every env var config.Init and its callees
// read (UT_DATA_DIR, UT_LISTEN_ADDR, UT_AUTH, UT_OPEN_BROWSER, ...) must be
// set by the caller BEFORE calling Run — this function reads process
// environment/config exactly once, the same way main() always has.
//
// Fatal startup errors return here instead of calling log.Fatalf (unlike
// the original main() body this was extracted from) — a mobile host process
// must never be os.Exit'd out from under its own app, and a CLI caller can
// still choose to log.Fatalf on the returned error itself.
func Run(ctx context.Context) error {
	envFile := os.Getenv("UT_ENV_FILE")
	if envFile == "" {
		envFile = "pos.env"
	}
	_ = godotenv.Load(envFile)

	logging.Init()
	log := logging.L()

	log.Infof("Universal Till POS starting...")

	cfg, err := config.Init()
	if err != nil {
		return err
	}

	paths.MigrateLegacyData(cfg.DBPath)

	if applied, err := db.ApplyPendingRestore(cfg.DBPath); err != nil {
		return err
	} else if applied {
		log.Infof("staged backup restore applied to %s", cfg.DBPath)
	}
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer database.Close()

	if applied, err := db.ApplyReplicaIdentity(database.DB, cfg.DBPath); err != nil {
		return err
	} else if applied {
		log.Infof("replica identity applied — this till is now part of the shop")
	}

	settingsStore := settings.NewStore(database.DB)

	if err := bootstrapPluginDirectories(); err != nil {
		return err
	}

	settingsStore.LoadRuntimeConfig(ctx, cfg)
	_ = settingsStore.SaveRuntimeConfig(ctx, cfg)

	// wg tracks the background goroutines this boot sequence starts —
	// directly (enroll/updates/alerts), via server.Start, or via pages.Init
	// (cloudsync, since ut-docs#8) — so Run can wait for them to actually
	// exit before its deferred database.Close() above runs. Without this,
	// "Run returned" didn't mean "nothing is still writing to the data dir"
	// (found 2026-07-30 via a mobile-shutdown CI flake). NOT yet covered —
	// the drain is NOT complete: pages.Init's StartSyncPush/StartSyncPull/
	// StartEODScheduler loops still run unjoined on ctx and do DB work
	// (ut-docs#153); internal/plugins.Supervisor's monitorProcess goroutines
	// (native plugin processes — separate, larger fix) and the wasm runtime's
	// per-plugin event-channel drainer (internal/plugins/wasm_runtime.go —
	// its channel is only closed on the next Sync/reload, never on shutdown,
	// so tracking it here would make every drain time out) are also logged
	// on that same card.
	//
	// bgCtx is independently cancellable from ctx: an early startup error
	// below (plugins.Init, marketplace.NewCatalogRepository, server.Start's
	// own listen failure) returns before the caller ever cancels ctx, and
	// without this, the background goroutines already started (e.g. enroll's
	// registration loop) would never be told to stop — drainBackgroundServices
	// would then always time out waiting on goroutines nothing signalled.
	// stopBg() is deferred so it (and the drain) run on every return path.
	var wg sync.WaitGroup
	bgCtx, stopBg := context.WithCancel(ctx)
	defer func() {
		stopBg()
		drainBackgroundServices(&wg, log, backgroundDrainTimeout)
	}()

	enroll.Init(bgCtx, cfg, settingsStore, &wg)

	pluginManager, err := plugins.Init(ctx, cfg, database.DB)
	if err != nil {
		return err
	}

	var catalogRepo *marketplace.CatalogRepository
	if cfg.Marketplace.EndpointURL != "" {
		tokenClient := oauth.NewTokenClient(&cfg.Marketplace)
		marketplaceClient := marketplace.NewClient(&cfg.Marketplace, tokenClient)
		catalogRepo, err = marketplace.NewCatalogRepository(marketplaceClient, paths.Plugins("cache"))
		if err != nil {
			return err
		}
		log.Infof("Marketplace catalog repository initialized (endpoint: %s)", cfg.Marketplace.EndpointURL)
	} else {
		log.Warnf("Marketplace not configured (UT_MARKETPLACE_ENDPOINT_URL not set)")
	}

	updates.Start(bgCtx, &wg)
	alerts.Start(bgCtx, cfg, database.DB, &wg)

	mux := pages.Init(ctx, bgCtx, cfg, pluginManager, database.DB, catalogRepo, &wg)

	supervisor := plugins.NewSupervisor(database.DB)
	if err := supervisor.AutoStartPlugins(ctx); err != nil {
		log.Warnf("plugin auto-start failed: %v", err)
	}

	return server.Start(bgCtx, cfg, mux, catalogRepo, database.DB, supervisor, &wg)
}

// backgroundDrainTimeout bounds how long Run waits for background goroutines
// to exit before giving up and closing the database anyway.
const backgroundDrainTimeout = 10 * time.Second

// drainBackgroundServices blocks until every goroutine registered on wg has
// exited, so the caller's subsequent database.Close() never races a
// straggler. Bounded: a service that ignores ctx cancellation must not hang
// shutdown forever — after timeout elapses this logs loudly and returns
// anyway, closing the database regardless (a wedged service is a bug to fix,
// not a reason to never shut down). timeout is a parameter (rather than using
// the backgroundDrainTimeout constant directly) so tests can exercise the
// timeout branch without a real 10-second wait. On timeout the internal
// wg.Wait() goroutine above is intentionally leaked — harmless (it only
// blocks on Wait, touching nothing) — and exits whenever the wedged service
// eventually does.
func drainBackgroundServices(wg *sync.WaitGroup, log *logging.Logger, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Errorf("shutdown: background services still running %s after cancel — closing database anyway", timeout)
	}
}

// bootstrapPluginDirectories creates required plugin cache directories
// as specified in 009-cloud-marketplace feature (T002)
func bootstrapPluginDirectories() error {
	dirs := []string{
		paths.Plugins("cache"),
		paths.Plugins("tmp"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	return nil
}
