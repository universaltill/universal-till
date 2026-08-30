package pages

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"runtime"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/selfupdate"
	"github.com/universaltill/universal-till/internal/updates"
)

// ut-docs#1165: the setup wizard's first screen (step 1) checks whether a
// newer release exists and, if so, offers to update BEFORE setup continues
// — accept applies in place and returns to the wizard on the new version,
// decline just continues on the current one. Never automatic. Reuses the
// EXACT existing mechanisms (internal/updates.CheckNow, internal/selfupdate,
// update_api.go's updateUnavailableHTML) rather than a second copy of any of
// them — see those files' own doc comments for the offline-first/10s-timeout
// contract this relies on.
//
// POST /api/update/check and POST /api/update/apply (update_api.go) can't be
// reused directly: both are gated by canPerform(d, r, "plugin_management"),
// and no admin session exists yet during first-boot setup. These two mirror
// them exactly but auth-exempt and NeedsFirstBoot-gated instead — the same
// tier as setupLanguageInstallHandler (setup_language_catalog.go) and
// setupTaxPluginInstallHandler (setup_tax_catalog.go).

// Test seams, same hermetic-test convention as update_api.go's autoUpdate*
// seams: production code never reassigns these; tests do, so the full
// check/apply flow can run without ever hitting the real GitHub API or
// calling the real selfupdate.Apply (which would try to re-exec the test
// binary).
var (
	setupUpdateCheckNow  = updates.CheckNow
	setupUpdateSupported = selfupdate.Supported
	setupUpdateApply     = selfupdate.Apply
	setupUpdateEnabled   = updates.Enabled
)

// setupUpdateBannerHTML is step 1's "a newer version exists — update before
// continuing?" prompt, with an apply control the operator must explicitly
// click (never auto-applied) — targets the SAME container GET /setup wired
// the check into (#setup-update-check), so a click swaps the banner for the
// apply response in place rather than navigating away from step 1.
func setupUpdateBannerHTML(locale, latest string) string {
	// hx-params="none": see the matching comment on #setup-update-check's own
	// container in web/ui/pages/setup.html (review finding B1, ut-docs#1165)
	// — this button lives inside the same wizard <form> and must never
	// serialize a later step's PIN fields to this unauthenticated endpoint.
	return fmt.Sprintf(`<div class="setup-update-banner"><p>%s (v%s)</p><button type="button" class="btn primary" hx-post="/api/setup/update-apply" hx-target="#setup-update-check" hx-swap="innerHTML" hx-params="none" hx-confirm="%s">%s</button></div>`,
		html.EscapeString(httpx.T(locale, "setup.update.prompt")),
		html.EscapeString(latest),
		html.EscapeString(httpx.T(locale, "settings.update.apply_confirm")),
		html.EscapeString(httpx.T(locale, "status.update_now")))
}

// setupUpdateAlreadyCurrentHTML mirrors POST /api/update/check's "up to
// date" span (update_api.go) — reused wording, not a near-duplicate.
func setupUpdateAlreadyCurrentHTML(locale string) string {
	return fmt.Sprintf(`<span>✓ %s (v%s)</span>`,
		html.EscapeString(httpx.T(locale, "settings.update.up_to_date")),
		html.EscapeString(buildinfo.Version))
}

// setupUpdateRestartingHTML is the apply response on success: tells the
// operator the till is restarting, and polls /healthz (same technique as the
// status-bar chip's own update button, web/ui/layouts/base.html) so step 1
// reloads itself once the new binary is back up — "return to the wizard on
// the new version" per the card, with no manual refresh needed. Bounded the
// same way base.html's poller is (giving up after ~3 minutes), and — like
// base.html — offers a manual "tap to reload" recovery on timeout instead of
// leaving the message stuck forever with no way out (review finding N1,
// ut-docs#1165): base.html's own comment on this exact pattern explains why
// a permanent dead end previously read as "the update doesn't work," and
// that reasoning applies at least as much here — a kiosk till may have no
// other refresh affordance at all. Unlike base.html's copy of this text
// (marked `i18n:ignore`, pre-existing debt tracked under ut-docs#205), this
// is new code, so the recovery text goes through a proper locale key like
// everything else in this file, JSON-encoded for safe embedding in the
// inline script (the established pattern elsewhere in this package, e.g.
// index_page.go/invoice_page.go).
func setupUpdateRestartingHTML(locale string) string {
	timeoutJS, _ := json.Marshal(httpx.T(locale, "setup.update.restart_timeout"))
	return fmt.Sprintf(`<span id="setup-update-restart-msg">%s</span><script>(function(){var el=document.getElementById('setup-update-restart-msg');var tries=0;var iv=setInterval(function(){tries++;fetch('/healthz',{cache:'no-store'}).then(function(r){if(r.ok){clearInterval(iv);location.reload();}}).catch(function(){});if(tries>90){clearInterval(iv);el.textContent=%s;el.style.cursor='pointer';el.onclick=function(){location.reload();};}},2000);})();</script>`,
		html.EscapeString(httpx.T(locale, "setup.update.restarting")),
		string(timeoutJS))
}

// setupUpdateApplyFailedHTML: an explicit, operator-initiated action failing
// is never silent (unlike the background check) — say so plainly, and that
// setup itself is unaffected.
func setupUpdateApplyFailedHTML(locale string) string {
	return fmt.Sprintf(`<span>%s</span>`, html.EscapeString(httpx.T(locale, "setup.update.apply_failed")))
}

// setupUpdateCheckHandler is POST /api/setup/update-check: one synchronous
// release check (bounded by updates.CheckNow's own 10s timeout), answered as
// a swappable HTML snippet — empty when there's nothing to say (no update,
// or the check failed/timed out; offline-first: never an error, never a
// delay to the wizard painting first). Auth-exempt on the same first-boot-
// only window as POST /api/setup/language — NeedsFirstBoot is the gate.
func setupUpdateCheckHandler(d *common.Deps, svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		firstBoot, err := svc.NeedsFirstBoot(r.Context())
		if err != nil || !firstBoot {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		// UT_UPDATE_CHECK=0 (air-gapped/opted-out installs, internal/updates'
		// own Start docs): unlike Settings' manual "Check for updates" button
		// (an explicit user action, exempt by established precedent), this
		// check fires automatically on step 1's load — so it must honor the
		// same opt-out rather than making an outbound call regardless
		// (review finding N2, ut-docs#1165).
		if !setupUpdateEnabled() {
			return
		}
		st := setupUpdateCheckNow(r.Context())
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if st.Latest == "" || !st.Available {
			// st.Latest == "": the check failed or timed out — nothing was
			// ever confirmed, so say nothing (never an error on this screen).
			// !st.Available: genuinely up to date — nothing to prompt either.
			return
		}
		if setupUpdateSupported() {
			fmt.Fprint(w, setupUpdateBannerHTML(locale, st.Latest))
			return
		}
		fmt.Fprint(w, updateUnavailableHTML(locale, st.Latest, runtime.GOOS))
	}
}

// setupUpdateApplyHandler is POST /api/setup/update-apply: the wizard's
// explicit "yes, update before continuing" action. Never reached without an
// operator clicking the banner's button first — same consent posture as the
// Settings page's own "Update now". Re-checks freshness before applying
// (same staleness guard registerUpdateAPI's POST /api/update/apply already
// uses — Current()/the cached status behind the banner can be stale) so a
// till that's already on the latest version is never told to re-download.
// Auth-exempt on the same first-boot-only window as
// setupUpdateCheckHandler above.
func setupUpdateApplyHandler(d *common.Deps, svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		firstBoot, err := svc.NeedsFirstBoot(r.Context())
		if err != nil || !firstBoot {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		st := setupUpdateCheckNow(r.Context())
		if !st.Available {
			fmt.Fprint(w, setupUpdateAlreadyCurrentHTML(locale))
			return
		}
		if !setupUpdateSupported() {
			fmt.Fprint(w, updateUnavailableHTML(locale, st.Latest, runtime.GOOS))
			return
		}
		if err := setupUpdateApply(r.Context()); err != nil {
			logging.L().Warnf("setup wizard: update apply failed: %v", err)
			fmt.Fprint(w, setupUpdateApplyFailedHTML(locale))
			return
		}
		fmt.Fprint(w, setupUpdateRestartingHTML(locale))
	}
}
