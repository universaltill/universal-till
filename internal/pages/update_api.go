package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/selfupdate"
	"github.com/universaltill/universal-till/internal/updates"
)

// Unattended auto-update (ut-docs#79). The hard parts (release check, verify
// + swap + re-exec) already exist below/in selfupdate; this adds a settings
// toggle + time-of-day schedule so Apply can run without a human clicking
// "Update now" — same enable+HH:MM shape as the EOD scheduler (eod_api.go).
const (
	keyAutoUpdateEnabled     = "update.auto_enabled"
	keyAutoUpdateTime        = "update.auto_time"         // local "HH:MM"
	keyAutoUpdateLastAttempt = "update.auto_last_attempt" // "YYYY-MM-DD"
)

// Seams so tests can fake the scheduler's decisions without hitting the real
// GitHub API or calling the real selfupdate.Apply (which would try to re-exec
// the test binary) — same hermetic-test convention as selfupdate's own seams.
var (
	autoUpdateCurrent   = updates.Current
	autoUpdateCheckNow  = updates.CheckNow
	autoUpdateSupported = selfupdate.Supported
	autoUpdateApply     = selfupdate.Apply
)

// autoUpdateWindow bounds how late a catch-up can still fire. eodDue's
// unbounded "any time >= hhmm today" is fine for a Z-report (generating one
// late is harmless), but wrong here: an unbounded check means a till switched
// off overnight and booted at opening time would auto-update — and restart
// itself — minutes into trading, exactly what scheduling a time-of-day exists
// to avoid (ut-docs#79 review, BLOCKING-1). Missing the window entirely means
// waiting for tomorrow's, not "whenever the till next happens to be on."
const autoUpdateWindow = 30 * time.Minute

// autoUpdateDue is the pure schedule decision: enabled, a valid HH:MM whose
// window [hhmm, hhmm+autoUpdateWindow) now falls in, and not already
// attempted today (success or failure — at most once per day, mirrors
// eodDue's alreadyDone gate).
func autoUpdateDue(now time.Time, enabled bool, hhmm string, lastAttempt string) bool {
	if !enabled || !eodTimeRe.MatchString(hhmm) {
		return false
	}
	if lastAttempt == now.Format("2006-01-02") {
		return false
	}
	sched, err := time.Parse("15:04", hhmm)
	if err != nil {
		return false
	}
	clock, err := time.Parse("15:04", now.Format("15:04"))
	if err != nil {
		return false
	}
	elapsed := clock.Sub(sched)
	return elapsed >= 0 && elapsed < autoUpdateWindow
}

// autoUpdateTick runs one scheduler decision for the given wall-clock time.
// It reads cached updates.Current() first (no network) so a disabled/not-
// yet-available day is a free no-op on every 30s tick; only once due AND
// cached-available does it check the basket (an unattended restart mid-sale
// silently destroys the in-memory basket — ut-docs#79 review, BLOCKING-2 —
// so it defers rather than fires, without spending today's window), then
// record the day's attempt and re-check freshness (updates.CheckNow) before
// actually calling Apply — the same staleness guard the manual "Update now"
// button already uses (Current() can be up to 24h stale by design). Apply
// itself refuses a second concurrent caller (selfupdate.applyMu), so this
// never races the manual button.
func autoUpdateTick(ctx context.Context, d *common.Deps, now time.Time) {
	get := func(key string) string {
		v, _, _ := d.Settings.Get(ctx, key)
		return strings.TrimSpace(v)
	}
	enabled := get(keyAutoUpdateEnabled) == "true"
	hhmm := get(keyAutoUpdateTime)
	lastAttempt := get(keyAutoUpdateLastAttempt)
	if !autoUpdateDue(now, enabled, hhmm, lastAttempt) {
		return
	}
	if !autoUpdateCurrent().Available || !autoUpdateSupported() {
		return
	}
	if d.Engine.Basket().ItemCount() > 0 {
		return
	}
	// Mark the attempt BEFORE calling Apply so a failure (or a stale-cache
	// miss below) never retries twice in one day — repeated large downloads
	// on failure would be wasteful/aggressive.
	_ = d.Settings.Set(ctx, keyAutoUpdateLastAttempt, now.Format("2006-01-02"))
	st := autoUpdateCheckNow(ctx)
	if !st.Available {
		return
	}
	if err := autoUpdateApply(ctx); err != nil {
		logging.L().Errorf("auto-update: %v", err)
	}
}

// StartAutoUpdateScheduler runs the background unattended-update loop (docs:
// ut-docs#79). Same 30s-ticker shape as StartEODScheduler.
func StartAutoUpdateScheduler(ctx context.Context, d *common.Deps) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				autoUpdateTick(ctx, d, time.Now())
			}
		}
	}()
}

// updateUnavailableHTML renders the status line for "a newer version exists but
// in-app apply can't run on this install". On Windows and macOS a download
// link is actionable — both are windowed desktop OSes with a browser, so a
// user who can't self-update (Windows: no in-app updater at all; macOS: an
// Intel Mac, ut-docs#18 — no Intel .dmg is ever published) can still get the
// new version themselves. On a unix kiosk a website link is a dead end —
// fullscreen with no way out and no installer to run — so it states the
// situation plainly with no link (board ut-docs#147). A correctly provisioned
// kiosk never reaches here: selfupdate.Supported() is true for a
// service-writable install, so the inline Apply button is shown instead.
func updateUnavailableHTML(locale, latest, goos string) string {
	if selfupdate.DownloadLinkActionable(goos) {
		// target="_blank": a plain same-window navigation is a dead end in
		// the WebView2 desktop shell (cmd/unitill-desktop/webview_fallback.go
		// has no NewWindowRequested handler) — ut-docs#159.
		return fmt.Sprintf(`<span>⬆ %s v%s — <a href="https://www.universaltill.com/download" rel="noopener" target="_blank">%s</a></span>`,
			html.EscapeString(httpx.T(locale, "status.update_available")),
			html.EscapeString(latest),
			html.EscapeString(httpx.T(locale, "settings.update.download")))
	}
	return fmt.Sprintf(`<span>⬆ %s v%s — %s</span>`,
		html.EscapeString(httpx.T(locale, "status.update_available")),
		html.EscapeString(latest),
		html.EscapeString(httpx.T(locale, "settings.update.unavailable_here")))
}

// registerUpdateAPI exposes the manager-gated in-app updater. It downloads the
// latest release, verifies its checksum, swaps the binary + web assets, and
// re-execs for any install whose tree is writable by the running user
// (archive installs, and .deb installs whose postinstall chowns the tree to
// the service user — ut-docs#151); Windows always uses its native installer,
// and a non-writable install falls back to a plain reinstall.
func registerUpdateAPI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/update/apply", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager only", http.StatusForbidden)
			return
		}
		respond := func(status int, ok bool, msg string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": ok, "message": msg})
		}
		respondCurrent := func() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true, "already_current": true,
				"message": "already up to date (v" + buildinfo.Version + ")",
			})
		}
		if !selfupdate.Supported() {
			respond(http.StatusBadRequest, false, selfupdate.ErrUnsupported.Error())
			return
		}
		// Re-check freshness before applying, don't trust the button's
		// data-latest (baked from the status bar's cached updates.Current()
		// at the PAGE'S last render/boot, up to 24h stale by design — see
		// updates.Start's daily ticker). Without this, a till that's already
		// on the latest version (e.g. just self-updated, or a new release
		// landed after this page loaded but the running build is still
		// current) can be told to "update" to a version that isn't actually
		// newer — confirmed as a real user-visible bug 2026-07-28: the
		// status bar showed "Update now v0.2.40" while v0.2.41 was already
		// running. selfupdate.Apply has no equality guard of its own, so
		// this would silently re-download+reinstall the same build instead
		// of failing loudly, at best a wasted download, at worst whatever
		// state applyMacApp's helper hits redoing a swap it just did.
		st := updates.CheckNow(r.Context())
		if !st.Available {
			respondCurrent()
			return
		}
		// Apply stages the swap and schedules the re-exec; the response flushes
		// before the process restarts.
		if err := selfupdate.Apply(r.Context()); err != nil {
			respond(http.StatusBadGateway, false, err.Error())
			return
		}
		respond(http.StatusOK, true, "update installed — restarting")
	})

	// Manual "Check for updates" (Settings): one synchronous poll of the
	// releases API, answered as a swappable HTML snippet.
	mux.HandleFunc("POST /api/update/check", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager only", http.StatusForbidden)
			return
		}
		st := updates.CheckNow(r.Context())
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case st.Latest == "":
			fmt.Fprintf(w, `<span>✗ %s</span>`, html.EscapeString(httpx.T(locale, "settings.update.check_failed")))
		case !st.Available:
			fmt.Fprintf(w, `<span>✓ %s (v%s)</span>`,
				html.EscapeString(httpx.T(locale, "settings.update.up_to_date")),
				html.EscapeString(buildinfo.Version))
		case selfupdate.Supported():
			// The status-bar update button also appears on the next page
			// load; this inline one applies immediately.
			fmt.Fprintf(w, `<span>⬆ v%s — </span><button class="btn primary" hx-post="/api/update/apply" hx-swap="none" hx-confirm="%s">%s</button>`,
				html.EscapeString(st.Latest),
				html.EscapeString(httpx.T(locale, "settings.update.apply_confirm")),
				html.EscapeString(httpx.T(locale, "status.update_now")))
		default:
			fmt.Fprint(w, updateUnavailableHTML(locale, st.Latest, runtime.GOOS))
		}
	})

	// Auto-update schedule settings (manager): enable + local time. Mirrors
	// POST /api/settings/eod's shape exactly (eod_api.go).
	mux.HandleFunc("POST /api/settings/update-schedule", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager only", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		hhmm := strings.TrimSpace(r.Form.Get("time"))
		enabled := r.Form.Get("enabled") == "on" || r.Form.Get("enabled") == "1"
		if enabled && !eodTimeRe.MatchString(hhmm) {
			http.Error(w, "time must be HH:MM", http.StatusBadRequest)
			return
		}
		_ = d.Settings.Set(r.Context(), keyAutoUpdateEnabled, fmt.Sprintf("%t", enabled))
		_ = d.Settings.Set(r.Context(), keyAutoUpdateTime, hhmm)
		w.WriteHeader(http.StatusNoContent)
	})
}
