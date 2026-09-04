package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
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
	// autoUpdateBuildVersion is buildinfo.Version, but the manual Update-now
	// button's own handler (`POST /api/update/apply`, above) does NOT use
	// this seam or the guard built on it -- an explicit user action stays
	// available even on a dev build; only the unattended scheduler defers.
	autoUpdateBuildVersion = func() string { return buildinfo.Version }
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
	// A "dev" build is a developer/hotfix build (ldflags -X never stamped a
	// real version) — unattended self-replacement of it is never the right
	// default, whatever the reason it ended up unstamped (ut-docs#369). The
	// manual "Update now" button is a separate handler and stays available.
	if autoUpdateBuildVersion() == "dev" {
		return
	}
	// Both engines (ut-docs#449): the kiosk basket is a separate instance
	// from the cashier's, so an unattended update mid-kiosk-order must be
	// blocked too, not just a cashier's mid-sale basket. d.KioskEngine is
	// nil in some test harnesses that never wire a kiosk engine.
	if d.Engine.Basket().ItemCount() > 0 ||
		(d.KioskEngine != nil && d.KioskEngine.Basket().ItemCount() > 0) {
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
// ut-docs#79). Same 30s-ticker shape as StartEODScheduler. wg registers the
// loop with app.Run's shutdown drain (ut-docs#153) — the caller must pass
// bgCtx (not ctx), same requirement as StartCloudSync. Joining this one
// matters more than most: autoUpdateTick can call selfupdate.Apply, which
// renames the binary/web assets, so an unjoined shutdown mid-swap has a
// narrow window to leave the install half-applied.
func StartAutoUpdateScheduler(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
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
	// ut-docs#1246: Android can never self-swap — the Go core ships as a
	// native library inside the APK and only the package installer may
	// replace an app's own code (an OS guarantee, not a gap here). But the
	// native shell CAN drive that installer, so this is not the dead end the
	// generic branch below describes: offer a button that calls the shell's
	// own bridge. The bridge takes NO url — it resolves the release APK
	// itself — so this markup cannot steer what gets installed (see
	// MainActivity.KioskBridge.installUpdate). A plain <a href> would not
	// work either way: shouldOverrideUrlLoading confines the WebView to the
	// till's own loopback origin, so an off-origin download link is refused
	// before it starts.
	if selfupdate.InstallBridgeAvailable(goos) {
		// ut-docs#1534: an ABSOLUTE path, not a bare "#android-update"
		// fragment. This same line renders into the status bar on every
		// page, where a same-page fragment silently navigates nowhere — the
		// operator taps "Download" and the till does nothing. Same-origin,
		// so shouldOverrideUrlLoading still lets the WebView follow it.
		return fmt.Sprintf(`<span>⬆ %s v%s — <a href="/settings#android-update" data-testid="android-update-install">%s</a></span>`,
			html.EscapeString(httpx.T(locale, "status.update_available")),
			html.EscapeString(latest),
			html.EscapeString(httpx.T(locale, "settings.update.download")))
	}
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

// setupUnavailableHTML is updateUnavailableHTML for the FIRST-BOOT WIZARD,
// where /settings does not yet exist as a destination: there is no manager
// account to authorise with, and internal/auth/middleware.go bounces the
// route to /login, which throws away the Alpine wizard's step state
// (ut-docs#1534 review, finding 3 — a regression introduced by making the
// Android link absolute: as a bare "#android-update" fragment it had been an
// inert no-op here, so the harm was invisible).
//
// Android therefore gets the plain statement of fact and no link at all on
// this screen. The update is real and the operator will be able to install it
// from Settings the moment setup finishes; sending them there mid-wizard just
// loses their progress. Every other platform is unchanged — a website link on
// Windows/macOS is as actionable at first boot as anywhere else.
func setupUnavailableHTML(locale, latest, goos string) string {
	if selfupdate.InstallBridgeAvailable(goos) {
		return fmt.Sprintf(`<span>⬆ %s v%s</span>`,
			html.EscapeString(httpx.T(locale, "status.update_available")),
			html.EscapeString(latest))
	}
	return updateUnavailableHTML(locale, latest, goos)
}

// registerUpdateAPI exposes the manager-gated in-app updater. It downloads the
// latest release, verifies its checksum, swaps the binary + web assets, and
// re-execs for any install whose tree is writable by the running user
// (archive installs, and .deb installs whose postinstall chowns the tree to
// the service user — ut-docs#151); Windows always uses its native installer,
// and a non-writable install falls back to a plain reinstall.
// respondUpdateApply writes the { "data": …, "error": null|"…" } envelope
// universal-till/CLAUDE.md mandates (ut-docs#387) for POST /api/update/apply.
// Package-level (not a closure inside the handler) so a test can call it
// directly against a real ResponseRecorder instead of a copy of its body —
// a copied closure would pass even if this one drifted (ut-docs#387 review).
func respondUpdateApply(w http.ResponseWriter, status int, ok bool, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if ok {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"message": msg}, "error": nil})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": msg})
}

// respondUpdateApplyCurrent is respondUpdateApply's "already up to date"
// case — see respondUpdateApply's own doc comment for why this is
// package-level rather than a handler-local closure.
func respondUpdateApplyCurrent(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": map[string]any{
			"already_current": true,
			"message":         "already up to date (v" + buildinfo.Version + ")",
		},
		"error": nil,
	})
}

func registerUpdateAPI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/update/apply", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "plugin_management") {
			http.Error(w, "manager only", http.StatusForbidden)
			return
		}
		respond := func(status int, ok bool, msg string) { respondUpdateApply(w, status, ok, msg) }
		respondCurrent := func() { respondUpdateApplyCurrent(w) }
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
	// ut-docs#1246: authorises the Android in-app update behind a MANAGER PIN.
	//
	// The install itself is performed by the native shell
	// (MainActivity.KioskBridge.installUpdate), which must drop the kiosk pin
	// to let the package installer appear — Android silently refuses to start
	// a non-allowlisted activity from a pinned app. Dropping the pin is
	// exactly the capability exit-to-os guards, and the update chip lives in
	// base.html on EVERY page including the sale screen, so without this gate
	// any cashier could tap "Update" and walk the till straight out of kiosk
	// mode. Same shape and same reasoning as POST /api/settings/exit-to-os,
	// including rejecting a blank PIN BEFORE AuthorizeManager so it cannot
	// burn the device-wide failed-attempt budget (5 failures = 30s lockout)
	// that keypad login shares.
	//
	// Authorisation only: this returns nothing the page can install with, and
	// the bridge takes no URL. A caller who forges a success response gains
	// no ability to install anything of their choosing.
	mux.HandleFunc("POST /api/update/android-install", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		pin := strings.TrimSpace(r.Form.Get("manager_pin"))
		if pin == "" {
			respondUpdateApply(w, http.StatusForbidden, false, "manager PIN required")
			return
		}
		if d.AuthSvc == nil {
			// Fail closed, same convention as canPerform elsewhere in this
			// package: no auth service wired means no way to prove manager.
			respondUpdateApply(w, http.StatusForbidden, false, "manager PIN required")
			return
		}
		approver, err := d.AuthSvc.AuthorizeManager(r.Context(), pin)
		if err != nil {
			status := http.StatusForbidden
			if errors.Is(err, auth.ErrLockedOut) {
				status = http.StatusTooManyRequests
			}
			respondUpdateApply(w, status, false, "manager PIN required")
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		_ = data.NewPOSRepo(d.Db).InsertAudit(r.Context(), nil, approver.ID, "update", "android", "update_authorized", nil, now, "")
		respondUpdateApply(w, http.StatusOK, true, "authorized")
	})

	mux.HandleFunc("POST /api/update/check", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "plugin_management") {
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
		if !canPerform(d, r, "plugin_management") {
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
