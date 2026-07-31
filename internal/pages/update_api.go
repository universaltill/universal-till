package pages

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"runtime"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/selfupdate"
	"github.com/universaltill/universal-till/internal/updates"
)

// updateUnavailableHTML renders the status line for "a newer version exists but
// in-app apply can't run on this install". On Windows a download link is
// actionable (the user runs the installer); on a unix kiosk a website link is a
// dead end — fullscreen with no way out and no installer to run — so it states
// the situation plainly with no link (board ut-docs#147). A correctly
// provisioned kiosk never reaches here: selfupdate.Supported() is true for a
// service-writable install, so the inline Apply button is shown instead.
func updateUnavailableHTML(locale, latest, goos string) string {
	if goos == "windows" {
		return fmt.Sprintf(`<span>⬆ %s v%s — <a href="https://www.universaltill.com/download" rel="noopener">%s</a></span>`,
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
// re-execs (archive installs only; the .deb/Windows use their native updaters).
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
}
