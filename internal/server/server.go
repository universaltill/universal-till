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
	catalogSyncInterval time.Duration
	telemetryInterval   time.Duration
	revocationInterval  time.Duration
	logger              *log.Logger
	cfg                 *config.Config
}

// NewBackgroundJobs creates a background job scheduler
func NewBackgroundJobs(catalogRepo *marketplace.CatalogRepository, db *sql.DB, supervisor *plugins.Supervisor, cfg *config.Config, logger *log.Logger) *BackgroundJobs {
	var revocationChecker *plugins.RevocationChecker
	if cfg.Marketplace.EndpointURL != "" {
		revocationChecker = plugins.NewRevocationChecker(db, cfg.Marketplace.EndpointURL, supervisor)
	}

	return &BackgroundJobs{
		catalogRepo:         catalogRepo,
		revocationChecker:   revocationChecker,
		catalogSyncInterval: 15 * time.Minute,
		telemetryInterval:   5 * time.Minute,
		revocationInterval:  30 * time.Minute,
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
				bj.logger.Println("[Scheduler] telemetry job triggered (stub)")
				// TODO: Implement telemetry reporting in T024
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

	// Detect device architecture
	deviceArch := "linux/amd64" // Default, could be runtime.GOOS + "/" + runtime.GOARCH
	locale := bj.cfg.DefaultLocale
	if locale == "" {
		locale = "en-US" // fallback to US English
	}

	maxRetries := 3
	baseDelay := 1 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		_, err := bj.catalogRepo.Fetch(ctx, locale, deviceArch)
		if err == nil {
			bj.logger.Printf("[Scheduler] catalog sync successful")
			return
		}

		// Exponential backoff
		if attempt < maxRetries-1 {
			delay := baseDelay * time.Duration(1<<uint(attempt))
			bj.logger.Printf("[Scheduler] catalog sync failed (attempt %d/%d): %v, retrying in %v",
				attempt+1, maxRetries, err, delay)
			time.Sleep(delay)
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
			run := func() {
				list, err := dbpkg.ListBackups(cfg.DBPath)
				if err == nil && len(list) > 0 && time.Since(list[0].ModTime) < 24*time.Hour {
					return
				}
				path, err := dbpkg.Snapshot(db, cfg.DBPath)
				if err != nil {
					log.Printf("[Backup] snapshot failed: %v", err)
					return
				}
				log.Printf("[Backup] daily snapshot: %s", path)
				_ = dbpkg.PruneBackups(cfg.DBPath, 14)
			}
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
		Addr:    cfg.ListenAddr,
		Handler: handler,
	}

	// Graceful shutdown when context is cancelled
	go func() {
		<-ctx.Done()
		log.Printf("shutting down HTTP server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("listening on %s", cfg.ListenAddr)

	// Convenience: open the setup/sale page in the operator's browser once the
	// server accepts connections. Skipped on kiosk tills (they launch their own
	// browser) and when UT_OPEN_BROWSER is set falsy.
	if shouldOpenBrowser() {
		go openSetupPage(cfg.ListenAddr)
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
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
	url := fmt.Sprintf("http://%s:%s", host, port)
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
