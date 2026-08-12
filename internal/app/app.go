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
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/joho/godotenv"
	"github.com/universaltill/universal-till/internal/alerts"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages"
	"github.com/universaltill/universal-till/internal/pages/common"
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
	// Best-effort housekeeping, never fatal: a sweep failure must not block
	// boot (offline-first — startup can't depend on this succeeding).
	if n, err := db.SweepOrphanedJoinSnapshots(cfg.DBPath); err != nil {
		log.Warnf("sweep orphaned join snapshots: %v", err)
	} else if n > 0 {
		log.Infof("swept %d orphaned join-snapshot file(s)", n)
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
	// (cloudsync, and since ut-docs#153 also StartSyncPush/StartSyncPull/
	// StartEODScheduler/StartAutoUpdateScheduler) — so Run can wait for them
	// to actually exit before its deferred database.Close() above runs.
	// Without this, "Run returned" didn't mean "nothing is still writing to
	// the data dir" (found 2026-07-30 via a mobile-shutdown CI flake).
	// deps.AsyncWork (pages.Init's second return value) is joined here too,
	// via its own drainBackgroundServices call below (ut-docs#513): it's a
	// SEPARATE WaitGroup tracking best-effort goroutines fired AFTER an HTTP
	// handler already responded — printReceiptAsync/printKitchenAsync
	// (ut-docs#425) and the invoice-issue print goroutine — which keep
	// touching Db/Settings on their own schedule and are never added to wg
	// itself (they aren't background *services* wg tracks, they're
	// per-request fire-and-forget work). Without draining it too, the same
	// "Run returned but something is still writing to the data dir" class of
	// bug wg's own comment above describes remains open for this second,
	// disconnected goroutine population.
	// The two goroutine classes that used to be exempt are covered too since
	// ut-docs#380: internal/plugins.Supervisor's monitorProcess goroutines are
	// joined by Supervisor.Shutdown itself (bounded by its ctx, called from
	// server.Start's wg-tracked graceful-shutdown goroutine, so waiting on wg
	// waits on it too). The wasm runtime's per-plugin event-channel drainers
	// are joined by pluginManager.Close — but NOT from that same server.Start
	// goroutine (ut-docs#503, a regression from #380/PR#268's original
	// wiring there): that goroutine fires on bgCtx.Done(), the SAME signal
	// that independently triggers every other service on this wg
	// (cloudsync's ticker included), and Close's ResetSubscribers() closes
	// the exact channels EventBus.publish sends on — publish releases its
	// lock before that send, so racing Close against a still-live publisher
	// is a real "send on closed channel" panic (reproduced directly against
	// that exact call path). pluginManager.Close is called below instead,
	// from THIS function's own deferred cleanup, strictly after
	// drainBackgroundServices — by then every wg member, including
	// server.Start's own shutdown goroutine and cloudsync's ticker, has
	// actually exited, not merely been asked to. (If drainBackgroundServices
	// itself times out, a wedged publisher can still be live when Close
	// runs and the panic window reopens on that doubly-degraded path — the
	// underlying fix belongs in EventBus.publish's lock-release-before-send
	// shape, not this sequencing.)
	//
	// bgCtx is independently cancellable from ctx: an early startup error
	// below (plugins.Init, marketplace.NewCatalogRepository, server.Start's
	// own listen failure) returns before the caller ever cancels ctx, and
	// without this, the background goroutines already started (e.g. enroll's
	// registration loop) would never be told to stop — drainBackgroundServices
	// would then always time out waiting on goroutines nothing signalled.
	// stopBg() is deferred so it (and the drain, and pluginManager.Close) run
	// on every return path, including early-return ones — pluginManager is
	// declared here (not with := at its assignment below) so this closure
	// can reference it before it exists yet; Close is nil-safe on both
	// *Manager and its *WasmRuntime field, so an early return before
	// plugins.Init assigns it is safe (Close is a METHOD with its own nil
	// receiver check, unlike a bare field access — verified by reading
	// Manager.Close/WasmRuntime.Close, not assumed).
	var wg sync.WaitGroup
	var pluginManager *plugins.Manager
	// deps is pages.Init's second return value, declared here (not := at its
	// assignment below) for the same reason as pluginManager above: this
	// closure references it before it exists yet. Init runs late in Run
	// (after enroll/plugins/marketplace setup) — an early return before that
	// point leaves deps nil, so the drain below is guarded rather than
	// unconditionally taking &deps.AsyncWork against a nil *common.Deps.
	var deps *common.Deps
	bgCtx, stopBg := context.WithCancel(ctx)
	defer func() {
		stopBg()
		drainBackgroundServices(&wg, log, backgroundDrainTimeout, "background services")
		if deps != nil {
			drainBackgroundServices(&deps.AsyncWork, log, backgroundDrainTimeout, "async print/kitchen/invoice work")
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), wasmCloseTimeout)
		defer cancel()
		pluginManager.Close(closeCtx)
	}()

	enroll.Init(bgCtx, cfg, settingsStore, &wg)

	pluginManager, err = plugins.Init(ctx, cfg, database.DB)
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

	// LAN till discovery (ADR-0033 part 1, ut-docs#264): advertise this
	// till over mDNS while — and only while — it's a primary. The role
	// check duplicates Deps.SyncPrimaryURL's one-line lookup rather than
	// waiting for pages.Init's *common.Deps to exist (Init runs after this
	// and needs mux routes registered before returning); both read the
	// exact same "sync.primary_url" setting. RoleCheckFromSettings (not an
	// inline closure here) so the rule itself has direct test coverage —
	// see internal/discovery's TestRoleCheckFromSettings_* tests.
	discoverySettings := data.NewSettingsRepo(database.DB)
	discoveryAdvertiser := discovery.NewAdvertiser(discoverySettings, discovery.RoleCheckFromSettings(discoverySettings), listenPort(cfg.ListenAddr))
	discoveryAdvertiser.Start(bgCtx, &wg)

	var mux http.Handler
	mux, deps = pages.Init(ctx, bgCtx, cfg, pluginManager, database.DB, catalogRepo, &wg)

	supervisor := plugins.NewSupervisor(database.DB)
	if err := supervisor.AutoStartPlugins(ctx); err != nil {
		log.Warnf("plugin auto-start failed: %v", err)
	}

	return server.Start(bgCtx, cfg, mux, catalogRepo, database.DB, supervisor, &wg)
}

// backgroundDrainTimeout bounds how long Run waits for background goroutines
// to exit before giving up and closing the database anyway.
const backgroundDrainTimeout = 10 * time.Second

// wasmCloseTimeout bounds pluginManager.Close's wait for the wasm runtime's
// event-channel drainer goroutines (ut-docs#380/#503). Close runs strictly
// AFTER drainBackgroundServices (see the long comment above for why), so
// this is additive on top of backgroundDrainTimeout, not contained inside
// it — a wedged drainer logs its own specific error rather than hanging
// shutdown forever, but does add its own bound's worth of time in that case.
const wasmCloseTimeout = 5 * time.Second

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
//
// label identifies which WaitGroup this call is draining in the timeout log
// line — Run.go joins two independent WaitGroups here (the background-
// services wg and Deps.AsyncWork, ut-docs#513), and a bare "background
// services still running" message on the second call would misname exactly
// the thing an operator needs to know when triaging a stuck shutdown.
func drainBackgroundServices(wg *sync.WaitGroup, log *logging.Logger, timeout time.Duration, label string) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Errorf("shutdown: %s still running %s after cancel — closing database anyway", label, timeout)
	}
}

// listenPort extracts the numeric port from cfg.ListenAddr (":8080" or
// "0.0.0.0:8080") for the mDNS advertiser's SRV record — best-effort only,
// falling back to config.Init's own default (8080) on any parse failure.
// Nothing built by ut-docs#264 actually consumes this port yet (selecting a
// discovered primary to join is #185's job); the TXT record's name/id/v
// fields are what matters for this card.
func listenPort(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 8080
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return 8080
	}
	return port
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
