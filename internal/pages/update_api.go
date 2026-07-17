package pages

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/selfupdate"
	"github.com/universaltill/universal-till/internal/updates"
)

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
		if !selfupdate.Supported() {
			respond(http.StatusBadRequest, false, selfupdate.ErrUnsupported.Error())
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
			fmt.Fprintf(w, `<span>⬆ %s v%s — <a href="https://www.universaltill.com/download" rel="noopener">%s</a></span>`,
				html.EscapeString(httpx.T(locale, "status.update_available")),
				html.EscapeString(st.Latest),
				html.EscapeString(httpx.T(locale, "settings.update.download")))
		}
	})
}
