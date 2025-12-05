package server

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

// BackgroundJobs manages periodic marketplace tasks
type BackgroundJobs struct {
	catalogSyncInterval time.Duration
	telemetryInterval   time.Duration
	revocationInterval  time.Duration
	logger              *log.Logger
}

// NewBackgroundJobs creates a background job scheduler
func NewBackgroundJobs(logger *log.Logger) *BackgroundJobs {
	return &BackgroundJobs{
		catalogSyncInterval: 15 * time.Minute,
		telemetryInterval:   5 * time.Minute,
		revocationInterval:  30 * time.Minute,
		logger:              logger,
	}
}

// Start begins all background jobs
func (bj *BackgroundJobs) Start(ctx context.Context) {
	// Catalog sync job (stub for T011)
	go func() {
		ticker := time.NewTicker(bj.catalogSyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				bj.logger.Println("[Scheduler] catalog sync job triggered (stub)")
				// TODO: Implement catalog sync in T011
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

func Start(ctx context.Context, cfg *config.Config, handler http.Handler) error {
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
