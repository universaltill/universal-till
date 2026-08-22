package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerWindowState exposes the till's persisted display preferences
// (window mode + launch-on-startup) over an unauthenticated, read-only
// endpoint (ut-docs#611): the desktop shell (unitill-desktop) reads this
// once at launch, before any operator has signed in, to decide which
// native window mode to apply (fullscreen/kiosk hide OS chrome, maximized,
// or the existing fixed-size normal window) and whether to reconcile its
// own XDG autostart entry. No more sensitive than /healthz — a pair of UI
// display preferences, not shop/financial data — so it's listed alongside
// it in auth.exempt().
//
// Both preferences are applied shell-side, not here: unitill-pos (this
// process) and unitill-desktop (the shell that owns the window and, on a
// .deb install, is the only one of the two actually running as the
// interactive desktop user) are separate OS processes — see
// settings_page.go's launch-on-startup handler comment and
// cmd/unitill-desktop/autostart_linux.go for why that split matters.
func registerWindowState(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /api/window-mode", func(w http.ResponseWriter, r *http.Request) {
		st := d.CurrentState()
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"window_mode":       common.ClampWindowMode(st.WindowMode),
				"launch_on_startup": st.LaunchOnStartup,
			},
			"error": nil,
		})
	})
}
