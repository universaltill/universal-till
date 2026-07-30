package server

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	dbpkg "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// BackgroundJobs manages periodic marketplace tasks
type BackgroundJobs struct {
	catalogRepo         *marketplace.CatalogRepository
	revocationChecker   *plugins.RevocationChecker
	telemetryClient     *plugins.TelemetryClient
	catalogSyncInterval time.Duration
	telemetryInterval   time.Duration
	revocationInterval  time.Duration
	retryBaseDelay      time.Duration // first catalog-sync retry backoff step
	logger              *log.Logger
	cfg                 *config.Config
}

// NewBackgroundJobs creates a background job scheduler
func NewBackgroundJobs(catalogRepo *marketplace.CatalogRepository, db *sql.DB, supervisor *plugins.Supervisor, cfg *config.Config, logger *log.Logger) *BackgroundJobs {
	var revocationChecker *plugins.RevocationChecker
	if cfg.Marketplace.EndpointURL != "" {
		revocationChecker = plugins.NewRevocationChecker(db, cfg.Marketplace.EndpointURL, supervisor)
	}

	telemetryClient := plugins.NewTelemetryClient(db, cfg.Marketplace.EndpointURL,
		marketplace.DeviceIDFromConfig(&cfg.Marketplace), cfg.Marketplace.ClientID, cfg.Marketplace.StoreID)

	return &BackgroundJobs{
		catalogRepo:         catalogRepo,
		revocationChecker:   revocationChecker,
		telemetryClient:     telemetryClient,
		catalogSyncInterval: 15 * time.Minute,
		telemetryInterval:   5 * time.Minute,
		revocationInterval:  30 * time.Minute,
		retryBaseDelay:      1 * time.Second,
		logger:              logger,
		cfg:                 cfg,
	}
}

// Start begins all background jobs
func (bj *BackgroundJobs) Start(ctx context.Context) {
	// Catalog sync job (T011) - LOCAL-FIRST: Only sync when cache is stale
	// User must explicitly refresh via UI to fetch from marketplace
	go func() {
		ticker := time.NewTicker(bj.catalogSyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Only sync if cache exists and is stale (not first fetch)
				if bj.catalogRepo != nil {
					_, isStale, err := bj.catalogRepo.Get()
					if err == nil && isStale {
						bj.logger.Println("[Scheduler] cache is stale, syncing catalog")
						bj.syncCatalog(ctx)
					}
				}
			}
		}
	}()

	// Telemetry reporting job (stub for T024)
	go func() {
		ticker := time.NewTicker(bj.telemetryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := bj.telemetryClient.ReportNow(ctx); err != nil {
					bj.logger.Printf("[Scheduler] telemetry report failed: %v", err)
				}
			}
		}
	}()

	// Revocation check job (T030)
	go func() {
		ticker := time.NewTicker(bj.revocationInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if bj.revocationChecker != nil {
					bj.logger.Println("[Scheduler] checking for revoked plugins")
					count, err := bj.revocationChecker.SyncRevocations(ctx)
					if err != nil {
						bj.logger.Printf("[Scheduler] revocation sync failed: %v", err)
					} else if count > 0 {
						bj.logger.Printf("[Scheduler] disabled %d revoked plugins", count)
					}
				}
			}
		}
	}()
}

// syncCatalog fetches the latest catalog from marketplace with exponential backoff
func (bj *BackgroundJobs) syncCatalog(ctx context.Context) {
	if bj.catalogRepo == nil {
		return
	}

	// The catalog is arch-filtered server-side; request the architecture this
	// till actually runs on — same value every interactive path sends
	// (plugins_store_page, cloudsync_wire, plugin_api). A hardcoded arch here
	// made an arm64 till's scheduler overwrite the cache with an
	// amd64-filtered catalog.
	deviceArch := deviceArchOf()
	locale := bj.cfg.DefaultLocale
	if locale == "" {
		locale = "en-US" // fallback to US English
	}

	maxRetries := 3
	baseDelay := bj.retryBaseDelay
	if baseDelay <= 0 {
		baseDelay = 1 * time.Second
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		_, err := bj.catalogRepo.Fetch(ctx, locale, deviceArch)
		if err == nil {
			bj.logger.Printf("[Scheduler] catalog sync successful")
			return
		}

		// Exponential backoff; bail out promptly on shutdown instead of
		// sleeping through it.
		if attempt < maxRetries-1 {
			delay := baseDelay * time.Duration(1<<uint(attempt))
			bj.logger.Printf("[Scheduler] catalog sync failed (attempt %d/%d): %v, retrying in %v",
				attempt+1, maxRetries, err, delay)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		} else {
			bj.logger.Printf("[Scheduler] catalog sync failed after %d attempts: %v", maxRetries, err)
		}
	}
}

func Start(ctx context.Context, cfg *config.Config, handler http.Handler, catalogRepo *marketplace.CatalogRepository, db *sql.DB, supervisor *plugins.Supervisor) error {
	// Start background jobs if catalog repository is configured
	if catalogRepo != nil {
		logger := log.New(log.Writer(), "[BackgroundJobs] ", log.LstdFlags)
		jobs := NewBackgroundJobs(catalogRepo, db, supervisor, cfg, logger)
		go jobs.Start(ctx)
	}

	// Daily local DB backup (docs: architecture/local-backup.md) — runs
	// regardless of marketplace config. Checks hourly; snapshots when the
	// newest backup is older than 24h; first check shortly after boot so a
	// till powered off nightly still gets one.
	if db != nil {
		go func() {
			run := func() { runDailyBackup(db, cfg.DBPath) }
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Minute):
				run()
			}
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					run()
				}
			}
		}()
	}

	// Related-items rebuild (docs: architecture/ai-integration.md §h):
	// market-basket stats from the shop's own sales history feed the
	// "customers also buy" strip. Runs at startup (tills reboot daily) and
	// every 24h for always-on installs. Local SQL only — no network.
	if db != nil {
		go func() {
			repo := data.NewRelatedItemsRepo(db)
			rebuild := func() {
				jobCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				if n, err := repo.Rebuild(jobCtx); err != nil {
					log.Printf("[RelatedItems] rebuild failed: %v", err)
				} else {
					log.Printf("[RelatedItems] rebuilt %d co-occurrence pairs", n)
				}
			}
			rebuild()
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					rebuild()
				}
			}
		}()
	}

	srv := &http.Server{
		Handler: handler,
	}

	// Bind the configured port, or the next free one if it's already taken (a
	// second till on the same machine, or another app on :8080) so a busy port
	// never blocks startup. cfg.ListenAddr is updated to what we actually bound
	// so the browser-open below points at the right place.
	ln, actualAddr, err := listenWithFallback(cfg.ListenAddr)
	if err != nil {
		return err
	}
	if actualAddr != cfg.ListenAddr {
		log.Printf("port %s was busy — listening on %s instead", cfg.ListenAddr, actualAddr)
	}
	cfg.ListenAddr = actualAddr

	// Graceful shutdown when context is cancelled
	go func() {
		<-ctx.Done()
		log.Printf("shutting down HTTP server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		if supervisor != nil {
			_ = supervisor.Shutdown(shutdownCtx)
		}
	}()

	log.Printf("listening on %s", cfg.ListenAddr)

	// Convenience: open the setup/sale page in the operator's browser once the
	// server accepts connections. Skipped on kiosk tills (they launch their own
	// browser) and when UT_OPEN_BROWSER is set falsy.
	if shouldOpenBrowser() {
		go openSetupPage(cfg.ListenAddr)
	}

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// deviceArchOf reports the os/arch pair this till runs on. A package var so
// tests can pin a sentinel value: on linux/amd64 CI the real runtime value is
// indistinguishable from the historical hardcoded "linux/amd64" default this
// code once had, so a pass-through test needs a sentinel to catch a
// re-hardcoding on every platform.
var deviceArchOf = func() string {
	return fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
}

// runDailyBackup snapshots the local DB unless a backup newer than 24h
// already exists, then prunes old snapshots to the newest 14.
func runDailyBackup(db *sql.DB, dbPath string) {
	list, err := dbpkg.ListBackups(dbPath)
	if err == nil && len(list) > 0 && time.Since(list[0].ModTime) < 24*time.Hour {
		return
	}
	path, err := dbpkg.Snapshot(db, dbPath)
	if err != nil {
		log.Printf("[Backup] snapshot failed: %v", err)
		return
	}
	log.Printf("[Backup] daily snapshot: %s", path)
	_ = dbpkg.PruneBackups(dbPath, 14)
}

// listenWithFallback binds addr, or the next free port when addr's port is
// already in use, so a busy port doesn't stop the till from starting. It tries
// the configured port, then the next 20, then lets the OS pick any free port.
// Returns the listener and the address actually bound. If the configured port
// binds cleanly the behaviour is unchanged.
func listenWithFallback(addr string) (net.Listener, string, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, ln.Addr().String(), nil
	}
	host, portStr, serr := net.SplitHostPort(addr)
	base, aerr := strconv.Atoi(portStr)
	if serr != nil || aerr != nil {
		return nil, "", err // unparseable addr — surface the original bind error
	}
	for p := base + 1; p <= base+20; p++ {
		cand := net.JoinHostPort(host, strconv.Itoa(p))
		if l, e := net.Listen("tcp", cand); e == nil {
			return l, l.Addr().String(), nil
		}
	}
	// Last resort: port 0 → the OS hands us any free port.
	if l, e := net.Listen("tcp", net.JoinHostPort(host, "0")); e == nil {
		return l, l.Addr().String(), nil
	}
	return nil, "", err // nothing free — report the original failure
}

// shouldOpenBrowser decides whether to auto-open the browser. UT_OPEN_BROWSER
// wins when set; otherwise default on, except on kiosk tills.
func shouldOpenBrowser() bool {
	if v := os.Getenv("UT_OPEN_BROWSER"); v != "" {
		on, err := strconv.ParseBool(v)
		return err == nil && on
	}
	if k, _ := strconv.ParseBool(os.Getenv("UT_KIOSK")); k {
		return false
	}
	return true
}

// openSetupPage waits for the listener to accept, then opens the local URL in
// the default browser. Entirely best-effort — any failure is silently ignored
// (headless boxes, no browser, SSH sessions).
func openSetupPage(listenAddr string) {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	// Wait up to ~5s for the server to start accepting connections.
	addr := net.JoinHostPort(host, port)
	for range 50 {
		conn, derr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if derr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	startBrowser(fmt.Sprintf("http://%s:%s", host, port))
}

// startBrowser launches the OS default browser at url. A package var so tests
// can stub it — the real body spawns an actual browser process, which is
// untestable exec glue (documented, not faked).
var startBrowser = func(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
