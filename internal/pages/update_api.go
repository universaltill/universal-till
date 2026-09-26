package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"html"
	"net/http"
	"os"
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
	keyAutoUpdateTime        = "update.auto_time"                    // local "HH:MM"
	keyAutoUpdateLastAttempt = data.AutoUpdateLastAttemptSettingsKey // "YYYY-MM-DD", per-till (never synced)
)

// Seams so tests can fake the scheduler's decisions without hitting the real
// GitHub API or calling the real selfupdate.Apply (which would try to re-exec
// the test binary) — same hermetic-test convention as selfupdate's own seams.
var (
	autoUpdateCurrent   = updates.Current
	autoUpdateCheckNow  = updates.CheckNow
	autoUpdateSupported = selfupdate.Supported
	// androidInstallCheckNow: the freshness re-check for the Android install
	// endpoint (ut-docs#1545). A seam so a test can exercise both answers
	// without reaching the GitHub API.
	androidInstallCheckNow = updates.CheckNow
	autoUpdateApply        = func(ctx context.Context, idle func() bool) error {
		return selfupdate.ApplyVersionWhenIdle(ctx, "", idle)
	}
	// updateStatusFn backs GET /api/update/status (ut-docs#2759).
	updateStatusFn = selfupdate.CurrentStatus
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

// Unattended updates are ON by default, nightly (product owner, ut-docs#2726):
// a till nobody switched on never moved, so the fleet drifted across versions.
// An unset update.auto_enabled means on; an explicit "false" is the shop's
// choice and is kept. The default slot is a quiet local hour, and every till
// adds its own stable 0–30 min offset so a fleet does not hit the releases
// API (and restart) all at the same minute.
const (
	autoUpdateDefaultTime = "03:00"
	autoUpdateMaxJitter   = 30 * time.Minute
)

// autoUpdateJitter is a seam so tick tests can pin the per-till offset.
var autoUpdateJitter = defaultAutoUpdateJitter

// autoUpdateSchedule resolves the effective (enabled, HH:MM) from the stored
// settings. A replica (sync.primary_url set) reads OFF while the setting is
// unset: a replica that updates to latest while its main till cannot (a
// Windows main till, an unwritable .deb) would run ahead of it indefinitely.
// Since ut-docs#2738 a replica never runs the nightly path at all — it
// follows its main till's exact version (followTick) — so this only
// decides what the Settings page shows there.
func autoUpdateSchedule(get func(string) string) (enabled bool, hhmm string) {
	hhmm = strings.TrimSpace(get(keyAutoUpdateTime))
	if hhmm == "" {
		hhmm = autoUpdateDefaultTime
	}
	// Only the two values the save handler writes mean anything; any other
	// (hand-edited) value fails safe to off.
	switch strings.TrimSpace(get(keyAutoUpdateEnabled)) {
	case "true":
		return true, hhmm
	case "":
		// Same role rule as discovery.RoleCheckFromSettings / Deps.SyncPrimaryURL:
		// an empty sync.primary_url is a main or standalone till.
		return strings.TrimSpace(get("sync.primary_url")) == "", hhmm
	default:
		return false, hhmm
	}
}

// autoUpdateJitterFor maps a per-till seed onto a stable whole-minute offset
// in [0, autoUpdateMaxJitter).
func autoUpdateJitterFor(seed string) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	return time.Duration(h.Sum32()%uint32(autoUpdateMaxJitter/time.Minute)) * time.Minute
}

// defaultAutoUpdateJitter seeds the offset from a value that really is this
// till's own: sync.till_id first (set per replica at join, and sync.* never
// replicates), then marketplace.device_id (a main/standalone till has no
// till id; on a replica the device id is overwritten by the main till's on
// the next admin sync, so it can't come first), then the hostname on a till
// that never registered. Computed, not stored: the update.* settings
// replicate shop-wide, so a stored offset would be copied onto every till.
func defaultAutoUpdateJitter(ctx context.Context, d *common.Deps) time.Duration {
	seed := ""
	if d.Settings != nil {
		for _, k := range []string{"sync.till_id", "marketplace.device_id"} {
			if v, _, _ := d.Settings.Get(ctx, k); strings.TrimSpace(v) != "" {
				seed = k + "=" + strings.TrimSpace(v)
				break
			}
		}
	}
	if seed == "" {
		host, _ := os.Hostname()
		seed = "host=" + host
	}
	return autoUpdateJitterFor(seed)
}

// autoUpdateSlot reports whether now falls in the window that opens at
// hhmm+offset, and the date that slot belongs to. Minutes-of-day arithmetic
// modulo 24h, so a window that crosses midnight (23:50, or 23:45 plus an
// offset) still opens and closes where it should; the slot date is the date
// the window OPENED on, so the half after midnight is not mistaken for a new
// day's slot.
func autoUpdateSlot(now time.Time, hhmm string, offset time.Duration) (inWindow bool, slotDate string) {
	sched, err := time.Parse("15:04", hhmm)
	if err != nil {
		return false, ""
	}
	const day = 24 * 60
	start := (sched.Hour()*60 + sched.Minute() + int(offset/time.Minute)) % day
	cur := now.Hour()*60 + now.Minute()
	elapsed := (cur - start + day) % day
	if time.Duration(elapsed)*time.Minute >= autoUpdateWindow {
		return false, ""
	}
	return true, now.Add(-time.Duration(elapsed) * time.Minute).Format("2006-01-02")
}

// autoUpdateDue is the pure schedule decision: enabled, a valid HH:MM whose
// window [hhmm+offset, hhmm+offset+autoUpdateWindow) now falls in, and that
// slot not already attempted (success or failure — at most once per day,
// mirrors eodDue's alreadyDone gate).
func autoUpdateDue(now time.Time, enabled bool, hhmm string, offset time.Duration, lastAttempt string) bool {
	if !enabled || !eodTimeRe.MatchString(hhmm) {
		return false
	}
	in, slotDate := autoUpdateSlot(now, hhmm, offset)
	return in && lastAttempt != slotDate
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
	// A replica follows its main till's exact version instead (ut-docs#2738):
	// the nightly "latest" could run it ahead of its main till.
	if get("sync.primary_url") != "" {
		followTick(ctx, d)
		return
	}
	enabled, hhmm := autoUpdateSchedule(get)
	lastAttempt := get(keyAutoUpdateLastAttempt)
	offset := autoUpdateJitter(ctx, d)
	if !autoUpdateDue(now, enabled, hhmm, offset, lastAttempt) {
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
	// blocked too, not just a cashier's mid-sale basket. Since ADR-0103
	// (ut-docs#2261) a table-QR guest's basket counts too. autoUpdateBusy
	// (update_follow.go) is shared with the replica follow (ut-docs#2738).
	if autoUpdateBusy(d) {
		return
	}
	// Mark the attempt BEFORE calling Apply so a failure (or a stale-cache
	// miss below) never retries twice in one day — repeated large downloads
	// on failure would be wasteful/aggressive.
	_, slotDate := autoUpdateSlot(now, hhmm, offset)
	_ = d.Settings.Set(ctx, keyAutoUpdateLastAttempt, slotDate)
	st := autoUpdateCheckNow(ctx)
	if !st.Available {
		return
	}
	if err := autoUpdateApply(ctx, autoUpdateIdle(d)); err != nil {
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

// androidUpdateSessionAuthorizes reports whether POST
// /api/update/android-install would accept this caller on their session alone,
// with no manager PIN (ut-docs#1537). The ONE owner of that decision — the
// handler calls it too, so the Settings page cannot render a PIN-less button
// the endpoint will then refuse, or hide a PIN field it is about to demand.
//
// Fails closed on every uncertainty: no settings store, an unreadable
// display.mode, self-order mode, or a caller without plugin_management.
func androidUpdateSessionAuthorizes(d *common.Deps, r *http.Request) bool {
	if !canPerform(d, r, "plugin_management") {
		return false
	}
	if d.Settings == nil {
		return false
	}
	mode, _, err := d.Settings.Get(r.Context(), "display.mode")
	if err != nil {
		return false
	}
	return mode != "self_order"
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

// respondUpdateApplyInstalled is the success answer for POST
// /api/update/apply: it names the version being installed so the status bar
// reloads only once exactly that version answers (ut-docs#2759).
func respondUpdateApplyInstalled(w http.ResponseWriter, version string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":  map[string]any{"message": "update installed — restarting", "version": version},
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
		respondUpdateApplyInstalled(w, st.Latest)
	})

	// ut-docs#2759: which version is running and whether an applied update
	// is still waiting for a restart. The status bar polls this after
	// "Update now" and reloads only once the new version answers — /healthz
	// is answered by the old process too. Same gate as apply.
	mux.HandleFunc("GET /api/update/status", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "plugin_management") {
			http.Error(w, "manager only", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": updateStatusFn(), "error": nil})
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
		// ut-docs#1545: re-check freshness BEFORE authorising the actual
		// install. The desktop endpoint above has done this since
		// 2026-07-28, when the status bar was caught offering "Update now
		// v0.2.40" on a till already running v0.2.41; the Android endpoint
		// never learned it. The native bridge compares no versions — it just
		// fetches releases/latest — so without this an operator already on
		// the newest build spends a ~140MB download and is handed an
		// installer for the version they are running. Reported from the
		// pilot tablet: "even if there is no new version ... after 10~15
		// seconds it shows the download window."
		//
		// Ordered carefully (ut-docs#1545 review, second concurrent cycle
		// sweeping this PR, finding 6): the freshness check is an OUTBOUND
		// network call to the releases API, so it must not be reachable by
		// anyone who could not install anyway — a cashier tapping repeatedly
		// would otherwise burn the shop's unauthenticated GitHub rate budget
		// and starve the daily background check that keeps every till
		// current. But it must still come before AuthorizeManager, so a
		// correct manager PIN is never spent on a no-op.
		//
		// So: cheap local permission test first, network second, PIN last.
		_ = r.ParseForm()
		pin := strings.TrimSpace(r.Form.Get("manager_pin"))
		if pin == "" && !androidUpdateSessionAuthorizes(d, r) {
			respondUpdateApply(w, http.StatusForbidden, false, "manager PIN required")
			return
		}
		if st := androidInstallCheckNow(r.Context()); !st.Available {
			respondUpdateApplyCurrent(w)
			return
		}
		if pin == "" {
			// ut-docs#1537: no PIN offered. A signed-in manager on an
			// ORDINARY till may authorise from their session alone — the same
			// gate the desktop status-bar chip already uses (POST
			// /api/update/apply → canPerform), and for the same reason: they
			// have already proved who they are, and since ut-docs#1508 the app
			// does not pin itself at all outside self-order mode, so there is
			// no kiosk lock for this to release.
			//
			// In SELF-ORDER mode the pin is real and the session is not
			// trustworthy evidence: entering self-order never logs the till
			// out (ut-docs#1253), so the kiosk's own browser can still be
			// carrying a live manager cookie. Authorising from it would let a
			// customer standing at the machine tap the update chip and walk
			// the till out of its kiosk. There, the PIN stays mandatory.
			// display.mode is a server-side setting, so this decision is made
			// here rather than trusting the page to report whether it is
			// pinned.
			//
			// Already established above (the pre-check that gates the
			// network call); re-asked here so this branch stays readable on
			// its own and so the decision keeps exactly one owner.
			if androidUpdateSessionAuthorizes(d, r) {
				// settingsActorID, not a bare FromContext: with UT_AUTH
				// disabled canPerform returns true with NO user in context, so
				// a bare read writes actor_id NULL for a privileged action.
				// Same fallback every other manager-gated handler here uses.
				//
				// The payload distinguishes this from the PIN path, which
				// writes the same action name — an audit trail that cannot
				// tell "a manager typed a PIN" from "a manager's cookie was
				// live" is not much of an audit trail (review finding 4).
				now := time.Now().UTC().Format(time.RFC3339)
				_ = data.NewPOSRepo(d.Db).InsertAudit(r.Context(), nil, settingsActorID(r), "update", "android", "update_authorized", map[string]any{"via": "session"}, now, "")
				respondUpdateApply(w, http.StatusOK, true, "authorized")
				return
			}
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
		_ = data.NewPOSRepo(d.Db).InsertAudit(r.Context(), nil, approver.ID, "update", "android", "update_authorized", map[string]any{"via": "pin"}, now, "")
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
		// On a main/standalone till, "on" is stored as the default (unset),
		// not "true" (ut-docs#2726 review): the setting replicates shop-wide,
		// and an explicit "true" would switch every replica on too — with no
		// replica version cap yet (ut-docs#2732), a replica must stay off.
		// "Off" is still stored explicitly and applies to the whole shop.
		val := fmt.Sprintf("%t", enabled)
		if enabled {
			if primary, _, _ := d.Settings.Get(r.Context(), "sync.primary_url"); strings.TrimSpace(primary) == "" {
				val = ""
			}
		}
		_ = d.Settings.Set(r.Context(), keyAutoUpdateEnabled, val)
		_ = d.Settings.Set(r.Context(), keyAutoUpdateTime, hhmm)
		w.WriteHeader(http.StatusNoContent)
	})
}
