package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/universaltill/universal-till/internal/app"
	"github.com/universaltill/universal-till/internal/logging"
)

func main() {
	// Cancel the context on Ctrl-C (SIGINT), a service/`kill` (SIGTERM), or a
	// closed terminal (SIGHUP). That triggers the server's graceful shutdown
	// (server.Start) so the till always stops cleanly instead of lingering as
	// an orphaned process when its terminal goes away.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	// The full boot sequence (config, DB/migrations, plugin host, pages/mux,
	// background jobs, HTTP server) lives in internal/app so the mobile
	// gomobile-bind entry point (mobile/mobile.go, ADR-0023) can drive the
	// exact same server in-process instead of duplicating this.
	if err := app.Run(ctx); err != nil {
		logging.L().Fatalf("server stopped: %v", err)
	}
}
