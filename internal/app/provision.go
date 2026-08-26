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

// provisionOpenRetryAttempts/provisionOpenRetryDelay bound the db.Open
// retry (ut-docs#1094): a genuinely fresh install is exactly the case
// unitill-pos.service's own first-ever migration can still be mid-flight
// when this subcommand's own db.Open races it (see the call site's
// comment). 8 attempts x 750ms = up to ~5.25s of waiting, comfortably
// under postinstall.sh's own patience and this ticket's own reproduction
// (the service's migration is a handful of CREATE TABLE statements — well
// under a second in practice), while still converging rather than
// spinning forever against a genuinely broken database.
const (
	provisionOpenRetryAttempts = 8
	provisionOpenRetryDelay    = 750 * time.Millisecond
)

// openWithRetry calls db.Open up to attempts times, sleeping delay between
// tries, and returns the first success or the LAST error if every attempt
// fails — so a caller sees the real, final failure reason, not the first
// transient one.
func openWithRetry(path string, attempts int, delay time.Duration) (*db.DB, error) {
	var database *db.DB
	err := retryOnError(attempts, delay, func() error {
		var openErr error
		database, openErr = db.Open(path)
		return openErr
	})
	if err != nil {
		return nil, err
	}
	return database, nil
}

// retryOnError calls fn up to attempts times (attempts < 1 is treated as
// 1 — always try at least once), sleeping delay between failed attempts
// (never after the last one), and returns nil on the first success or fn's
// LAST error if every attempt fails. Generic and side-effect-free around
// fn itself, so the retry/backoff behavior is testable without a real
// database or real time (a fake fn + delay=0 in tests).
func retryOnError(attempts int, delay time.Duration, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if lastErr = fn(); lastErr == nil {
			return nil
		}
		if i < attempts-1 {
			time.Sleep(delay)
		}
	}
	return lastErr
}

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
	//
	// Deliberately does NOT take db.AcquireDataDirLock (ut-docs#1097), and
	// must not: packaging/scripts/postinstall.sh restarts unitill-pos.service
	// well BEFORE it invokes this subcommand, so on a real .deb install the
	// running service already holds that lock — acquiring it here would make
	// every desktop-kiosk-overlay install fail. That is not a hole in #1097's
	// guarantee: the lock exists to stop a second long-lived SERVER from
	// owning the data, and this is a short, single-transaction settings write
	// through the repository layer, exactly the "any second connection" case
	// SQLite's own locking already serializes. If this ever grows into
	// something that rewrites files in the data directory rather than rows in
	// the database, that reasoning stops holding and the sequencing in
	// postinstall.sh has to change with it.
	//
	// RETRIED (ut-docs#1094): on a genuinely fresh install, this process and
	// unitill-pos.service's own startup both race to migrate the SAME
	// brand-new database — postinstall.sh restarts the service and invokes
	// this subcommand back to back, and migrate() (internal/db/db.go) reads
	// its "current version" once, unprotected, before applying each pending
	// migration. _txlock=immediate + busy_timeout correctly serialize the two
	// connections' BEGIN statements, but only that: if this process's own
	// migrate() read a stale "current" a moment before the service's own
	// migration committed, it still re-attempts a migration the service just
	// applied — a genuine SQL error (most of this repo's migrations are
	// CREATE TABLE IF NOT EXISTS, so in practice this is more likely an
	// ALTER TABLE ... ADD COLUMN "duplicate column" or the
	// INSERT INTO schema_migrations primary-key conflict, not literally
	// "table already exists" — the mechanism, a stale version read causing
	// a redundant apply, is what matters, not which specific statement
	// trips over it), not a lock/timeout one, so busy_timeout cannot paper
	// over it. A bare retry of the whole db.Open (which re-reads the
	// current version fresh) converges once the service's own migration has
	// actually landed — confirmed as the real failure by a first
	// fresh-hardware install (ut-docs#1094).
	//
	// RESIDUAL RISK, not fixed here (independent review): the race is
	// symmetric — migrate()'s unprotected version read can just as well
	// make unitill-pos.service itself lose to a racing second connection,
	// which would fail the SERVICE's own startup (a dead till on a fresh
	// install, strictly worse than this subcommand failing). The real fix
	// is reading the current version and applying inside one BEGIN
	// IMMEDIATE in migrate() itself — out of scope for this change (every
	// db.Open call in the app goes through migrate(), so that fix needs its
	// own careful review); tracked on the issue instead of attempted here.
	database, err := openWithRetry(cfg.DBPath, provisionOpenRetryAttempts, provisionOpenRetryDelay)
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

	// NOT re-read-back-verified after this write (independent review of
	// ut-docs#1094 rejected an earlier draft that did): SetMany and any
	// subsequent Get in this same function go through the same *sql.DB
	// after a commit, so SQLite's own consistency guarantees make such a
	// check dead code — it can never actually catch anything here. The
	// symptom this ticket's failure 3 reports (window_mode reverting to
	// "normal" well after a successful provisioning run) happens at a
	// different layer and a later time — internal/pages/common.SaveState
	// rewrites the WHOLE settings map from whatever RuntimeState a
	// subsequent wizard/Settings save was built from, unconditionally
	// including window_mode — which a same-function read-back cannot
	// observe and, if it ever spuriously fired, would make worse: an error
	// here propagates out of ProvisionDesktopKioskDefaults, causing
	// postinstall.sh to skip systemctl try-restart (the step that makes
	// seeding take effect at all), the audit row, AND the marker. Left
	// genuinely open — see the ticket for the SaveState finding.
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
