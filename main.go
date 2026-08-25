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

	// One-shot subcommand, not the server: `unitill-pos
	// provision-desktop-kiosk-defaults` seeds the desktop-OS kiosk-overlay
	// window settings at install time (ut-docs#1040) and exits — invoked by
	// packaging/scripts/postinstall.sh as the pos service user, never by the
	// systemd unit. Kept off the elevation-gated /api/settings HTTP path on
	// purpose: an unattended first-install script has no admin PIN, and the
	// repository layer is the sanctioned non-HTTP write path.
	if len(os.Args) > 1 && os.Args[1] == "provision-desktop-kiosk-defaults" {
		if err := app.ProvisionDesktopKioskDefaults(ctx, os.Args[2:]); err != nil {
			logging.L().Fatalf("provision-desktop-kiosk-defaults: %v", err)
		}
		return
	}

	// The full boot sequence (config, DB/migrations, plugin host, pages/mux,
	// background jobs, HTTP server) lives in internal/app so the mobile
	// gomobile-bind entry point (mobile/mobile.go, ADR-0023) can drive the
	// exact same server in-process instead of duplicating this.
	if err := app.Run(ctx); err != nil {
		logging.L().Fatalf("server stopped: %v", err)
	}
}
