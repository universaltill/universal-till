package pages

import (
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// F11 window toggle (ut-docs#3735). In the desktop shell, F11 flips the
// LIVE window between fullscreen and normal over the ADR-0064 channel.
//
//   - Manager only: canPerform(..., "settings") — no PIN elevation prompt on
//     a keypress, so a cashier simply gets 403.
//   - Never touches kiosk: entering it needs the Window-mode selector and
//     leaving it stays behind the Exit-to-OS manager PIN.
//   - Live-only, like ExitToOS: display.window_mode (the launch preference)
//     is not rewritten, so there is no state/cloud-sync churn on a keypress.
//   - Only acts when a live shell is attached on loopback; otherwise a
//     harmless no-op and the page's JS never swallows the browser's F11.

// windowToggleOffered reports whether the page rendered for r should carry
// <body data-window-toggle> (so window-toggle.js intercepts F11): the
// request is from the till itself, a live shell that is the exit path is
// attached, and the operator may change settings. base.html is never used
// by /self-order, but the path is excluded here too.
func windowToggleOffered(d *common.Deps, r *http.Request) bool {
	if r == nil || strings.HasPrefix(r.URL.Path, "/self-order") {
		return false
	}
	if !windowToggleShellReady(d, r) {
		return false
	}
	return canPerform(d, r, "settings")
}

// windowToggleShellReady: loopback caller + attached, exit-path shell.
func windowToggleShellReady(d *common.Deps, r *http.Request) bool {
	return isLoopbackAddr(r.RemoteAddr) &&
		d.Shell != nil && d.Shell.IsExitPath() && d.Shell.Attached(common.ShellAttachedWindow)
}

// windowToggleTarget maps the live mode to what F11 should switch to; ""
// means leave it alone (kiosk, or an unknown value).
func windowToggleTarget(live string) string {
	switch live {
	case "normal", "maximized":
		return "fullscreen"
	case "fullscreen":
		return "normal"
	}
	return ""
}

func registerWindowToggle(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)
	reply := func(w http.ResponseWriter, mode string, applied bool, reason string) {
		writeJSON(w, http.StatusOK, map[string]any{
			"data":  map[string]any{"window_mode": mode, "applied": applied, "reason": reason},
			"error": nil,
		})
	}
	mux.HandleFunc("POST /api/settings/window-mode/toggle", func(w http.ResponseWriter, r *http.Request) {
		deny := func(code, msg string) {
			writeJSON(w, http.StatusForbidden, map[string]any{"data": nil, "error": map[string]any{"code": code, "message": msg}})
		}
		if !sameOriginRequest(r) {
			deny("cross_origin", "cross-origin request refused")
			return
		}
		if !canPerform(d, r, "settings") {
			deny("forbidden", "manager or admin required")
			return
		}
		if !windowToggleShellReady(d, r) {
			mode := ""
			if d.Shell != nil {
				mode, _ = d.Shell.Snapshot()
			}
			reply(w, mode, false, "no_shell")
			return
		}
		live, _ := d.Shell.Snapshot()
		target := windowToggleTarget(live)
		if target == "" {
			reply(w, live, false, "kiosk_locked")
			return
		}
		// Compare-and-set under the channel lock: a kiosk saved between the
		// read above and this write is never overwritten. The shell is
		// attached on the exit path (windowToggleShellReady), so the poll
		// controller's ApplyMode would only do SetMode — the spawn-mode
		// fallback is not in play — and the CAS is the whole write.
		if !d.Shell.CompareAndSetMode(live, target) {
			cur, _ := d.Shell.Snapshot()
			reply(w, cur, false, "kiosk_locked")
			return
		}
		settingsAudit(r, posRepo, elevationCheck{ActorID: settingsActorID(r)}, "settings", common.KeyWindowMode, "window_mode_toggled", map[string]any{"mode": target, "via": "f11"})
		reply(w, target, true, "")
	})
}
