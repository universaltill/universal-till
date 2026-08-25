package app

// Install-time provisioning for the desktop-OS kiosk overlay (ut-docs#1040).
//
// A fresh .deb install on a Raspberry Pi running a DESKTOP OS (the case
// packaging/scripts/postinstall.sh's is_pi_appliance deliberately bails on)
// should end with the till coming up fullscreen ON TOP of that desktop:
// unitill-desktop in kiosk window mode, autostarted with the login session,
// PIN-gated exit back to the desktop. All the app-level plumbing for that
// already exists (#611/#883/#1039) and keys off two persisted settings —
// display.window_mode and display.launch_on_startup — which this file seeds
// at install time, via the repository layer (never raw SQL from bash:
// guard-data-access.sh), plus a system-actor audit entry so the decision is
// visible to the owner on /audit rather than only in installer stdout.
//
// Invoked as `unitill-pos provision-desktop-kiosk-defaults [flags]` (see
// main.go's subcommand dispatch) by postinstall.sh, running as the pos
// service user with the same UT_DATA_DIR/env-file wiring as
// unitill-pos.service, so it opens the exact DB the running service uses.

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// keyDesktopKioskOverlayProvisioned is the settings-table completion marker
// that makes provisioning idempotent — same pattern as the headless path's
// /var/lib/unitill/kiosk-setup-done file, but kept in the DB because that is
// what the seeded defaults themselves live in (and what a DB restore/reset
// carries or clears along with them). Its value is the RFC3339 time of the
// one run that actually provisioned. Once present, a re-run is a complete
// no-op: it must never clobber a window mode the owner has since changed,
// and never write a duplicate audit row.
const keyDesktopKioskOverlayProvisioned = "install.desktop_kiosk_overlay_provisioned"

// auditActionDesktopKioskOverlayProvisioned is the audit_log action the
// provisioning run records (entity_type "settings", actor "system" — the
// same system-actor convention eod_api.go's report_archive_pruned entry
// already uses for actions no session user performed). Shows up on /audit
// like any other settings mutation.
const auditActionDesktopKioskOverlayProvisioned = "desktop_kiosk_overlay_provisioned"

// ProvisionDesktopKioskDefaults is the `provision-desktop-kiosk-defaults`
// subcommand body: resolves config the same way Run does (env file, then
// config.Init), opens the real DB (running migrations if it's first), and
// delegates to provisionDesktopKioskDefaults. args are the subcommand's own
// flags (everything after the subcommand name).
func ProvisionDesktopKioskDefaults(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("provision-desktop-kiosk-defaults", flag.ContinueOnError)
	trigger := fs.String("trigger", "manual",
		"what invoked this provisioning run (recorded in the audit entry)")
	autostartStaged := fs.Bool("autostart-staged", false,
		"whether the caller managed to stage the desktop session's autostart entry (recorded in the audit entry)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Same env-file convention as Run: UT_ENV_FILE, defaulting to ./pos.env.
	// godotenv.Load never overrides variables already set in the process
	// environment, so postinstall's explicit UT_DATA_DIR wins — mirroring
	// unitill-pos.service, which pins UT_DATA_DIR via Environment= too.
	envFile := os.Getenv("UT_ENV_FILE")
	if envFile == "" {
		envFile = "pos.env"
	}
	_ = godotenv.Load(envFile)

	cfg, err := config.Init()
	if err != nil {
		return err
	}
	// db.Open runs pending migrations itself, so this works both before the
	// service has ever created the DB and against a live one (the DSN's
	// busy_timeout + _txlock=immediate serialize against the service's own
	// writes, same as any second connection).
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer database.Close()

	did, err := provisionDesktopKioskDefaults(ctx, database.DB, *trigger, *autostartStaged)
	if err != nil {
		return err
	}
	if did {
		fmt.Println("desktop-kiosk-overlay defaults provisioned: window_mode=kiosk, launch_on_startup=true (recorded in the audit trail)")
	} else {
		fmt.Println("desktop-kiosk-overlay defaults already provisioned — nothing to do")
	}
	return nil
}

// provisionDesktopKioskDefaults idempotently seeds the two window settings
// and records the audit entry. Returns whether this run actually
// provisioned (false = the completion marker was already present).
//
// Ordering, chosen so every partial-failure retry converges: the two
// defaults land first (atomically, one SetMany transaction), then the audit
// entry, then the completion marker last. A failure before the marker
// leaves the next run to redo the earlier steps — re-seeding identical
// values is harmless at install time (nothing else has run yet), and the
// audit row only ever duplicates in the narrow marker-write-failed window,
// which is preferable to the opposite ordering's silent no-audit-ever.
func provisionDesktopKioskDefaults(ctx context.Context, sqlDB *sql.DB, trigger string, autostartStaged bool) (bool, error) {
	settingsRepo := data.NewSettingsRepo(sqlDB)

	if _, done, err := settingsRepo.Get(ctx, keyDesktopKioskOverlayProvisioned); err != nil {
		return false, err
	} else if done {
		return false, nil
	}

	if err := settingsRepo.SetMany(ctx, map[string]string{
		common.KeyWindowMode:      "kiosk",
		common.KeyLaunchOnStartup: "true",
	}); err != nil {
		return false, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	posRepo := data.NewPOSRepo(sqlDB)
	if err := posRepo.InsertAudit(ctx, nil, "system", "settings", common.KeyWindowMode,
		auditActionDesktopKioskOverlayProvisioned, map[string]any{
			"trigger":           trigger,
			"window_mode":       "kiosk",
			"launch_on_startup": true,
			"autostart_staged":  autostartStaged,
		}, now, ""); err != nil {
		return false, err
	}

	if err := settingsRepo.Set(ctx, keyDesktopKioskOverlayProvisioned, now); err != nil {
		return false, err
	}
	return true, nil
}
