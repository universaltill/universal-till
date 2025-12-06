package server

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// BackgroundJobs manages periodic marketplace tasks
type BackgroundJobs struct {
	catalogRepo         *marketplace.CatalogRepository
	catalogSyncInterval time.Duration
	telemetryInterval   time.Duration
	revocationInterval  time.Duration
	logger              *log.Logger
	cfg                 *config.Config
}

// NewBackgroundJobs creates a background job scheduler
func NewBackgroundJobs(catalogRepo *marketplace.CatalogRepository, cfg *config.Config, logger *log.Logger) *BackgroundJobs {
	return &BackgroundJobs{
		catalogRepo:         catalogRepo,
		catalogSyncInterval: 15 * time.Minute,
		telemetryInterval:   5 * time.Minute,
		revocationInterval:  30 * time.Minute,
		logger:              logger,
		cfg:                 cfg,
	}
}

// Start begins all background jobs
func (bj *BackgroundJobs) Start(ctx context.Context) {
	// Catalog sync job (T011)
	go func() {
		// Run immediately on startup
		bj.syncCatalog(ctx)

		ticker := time.NewTicker(bj.catalogSyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				bj.syncCatalog(ctx)
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

	// Revocation check job (stub for T030)
	go func() {
		ticker := time.NewTicker(bj.revocationInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				bj.logger.Println("[Scheduler] revocation check job triggered (stub)")
				// TODO: Implement revocation sync in T030
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

func Start(ctx context.Context, cfg *config.Config, handler http.Handler, catalogRepo *marketplace.CatalogRepository) error {
	// Start background jobs if catalog repository is configured
	if catalogRepo != nil {
		logger := log.New(log.Writer(), "[BackgroundJobs] ", log.LstdFlags)
		jobs := NewBackgroundJobs(catalogRepo, cfg, logger)
		go jobs.Start(ctx)
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
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
