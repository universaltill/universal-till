package recovery

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/web/locales"
)

// Result tells internal/app.Run what to do once Serve returns.
type Result int

const (
	// Shutdown means the caller's ctx was cancelled while recovery mode was
	// serving (SIGTERM/Ctrl-C etc.) — Run should return cleanly, same as a
	// normal shutdown, without attempting to reboot.
	Shutdown Result = iota
	// Retry means the operator clicked Retry — Run should re-attempt the
	// boot sequence that failed, in the same process.
	Retry
)

const shutdownTimeout = 5 * time.Second

// Serve binds cfg.ListenAddr (the SAME address every shell already points
// its WebView/webview at — ADR-0075) and serves the recovery page until
// either ctx is cancelled or the operator triggers a retry. It never falls
// back to a different port the way internal/server.Start's normal listener
// can (listenWithFallback) — recovery mode's whole point is that every
// shell's already-known address keeps working, so failing loudly on a bind
// conflict here is correct, not a regression.
func Serve(ctx context.Context, cfg *config.Config, failure Failure) (Result, error) {
	log := logging.L()

	// A fresh translator scoped to recovery mode's own lifetime, seeded
	// from cfg.Locales.Locale (env-derived, resolved by config.Init before
	// any of this ran) rather than the DB-persisted default locale
	// (internal/pages/init.go's state.Locale) — the database is exactly
	// what may be broken here. Per-request ?lang=/cookie overrides still
	// work: httpx.ResolveLocale/T/IsRTL are all DB-independent already.
	i18n, err := config.NewI18nFS(locales.FS, cfg.Locales.Locale)
	if err != nil {
		return Shutdown, fmt.Errorf("recovery mode: load locales: %w", err)
	}
	httpx.InitI18n(i18n, cfg.Locales.Locale)

	var safeModeDB *db.DB
	safeModeUsable := false
	if failure.Kind == KindMigration {
		if ro, roErr := db.OpenReadOnly(cfg.DBPath); roErr == nil {
			safeModeDB = ro
			defer safeModeDB.Close()

			// httpx.ActiveCurrency() silently defaults to GBP when never
			// initialized (its own atomic.Value load-with-default) — never a
			// crash, but a real correctness gap for any shop NOT on GBP: the
			// safe-mode sales list would show real figures under the wrong
			// currency symbol. pages.Init's normal InitCurrency call never
			// runs in recovery mode (it needs Deps this boot never reaches),
			// so read the shop's configured currency directly — a plain read,
			// safe against the read-only connection, no LoadState/SaveState
			// write-back needed for this narrow a purpose.
			if code, found, sErr := settings.NewStore(safeModeDB.DB).Get(ctx, "store.currency"); sErr == nil && found && code != "" {
				httpx.InitCurrency(code)
			} else if sErr != nil {
				log.Infof("recovery mode: could not read the shop's configured currency, safe mode will show amounts in the default currency: %v", sErr)
			}

			// A readable file isn't the same as a queryable one (independent
			// review, ut-docs#1436): ListSalesJournal joins tills/reads
			// sales.till_id, migrations 014/015 of 78 — a failure anywhere
			// before those leaves a perfectly openable database this query
			// still can't run against. Only advertise safe mode once the
			// real query has actually been proven to work.
			safeModeUsable = probeSafeMode(safeModeDB.DB)
			if !safeModeUsable {
				log.Infof("recovery mode: database opened read-only but the sales-journal query failed against its current schema — safe mode not offered")
			}
		} else {
			log.Infof("recovery mode: safe-mode read-only access unavailable: %v", roErr)
		}
	}

	retry := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler())
	mux.HandleFunc("GET /{$}", pageHandler(failure, safeModeUsable))
	mux.HandleFunc("POST /api/recovery/retry", retryHandler(retry))
	if safeModeUsable {
		registerSafeModeRoutes(mux, safeModeDB.DB)
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return Shutdown, fmt.Errorf("recovery mode: listen on %s: %w", cfg.ListenAddr, err)
	}
	srv := &http.Server{Handler: mux}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	log.Warnf("boot failed (%s), serving recovery screen on %s — ref %s: %s", failure.Kind, cfg.ListenAddr, failure.RefCode, failure.Detail)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return Shutdown, nil
	case <-retry:
		log.Infof("recovery mode: retry requested, re-attempting boot")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return Retry, nil
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			return Shutdown, fmt.Errorf("recovery mode: serve: %w", err)
		}
		return Shutdown, nil
	}
}
