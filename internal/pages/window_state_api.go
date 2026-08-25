package pages

import (
	"net/http"
	"strconv"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerWindowState exposes the till's display preferences (window mode +
// launch-on-startup) over an unauthenticated endpoint (ut-docs#611),
// reshaped by ADR-0064 (ut-docs#1039) into the desktop shell's live
// control poll as well:
//
//	GET /api/window-mode?control=live&since=<rev>&applied=<mode>&wait=<seconds>
//
//	- control=live advertises that this client will really apply what it
//	  is told AND can leave it again (only unitill-desktop builds whose
//	  platform has a real applyWindowMode send it — see
//	  cmd/unitill-desktop/shell_poll.go). Only the exact value "live"
//	  counts.
//	- since is the last revision the client saw; with wait > 0 the request
//	  holds until the revision moves, the wait elapses, or the client
//	  disconnects. since=0 (or any stale value) returns immediately, so a
//	  change between two polls can never be missed.
//	- applied is the mode the shell last actually applied — its heartbeat
//	  and its acknowledgement in one, carried on the next poll rather than
//	  as a separate write endpoint (no new write surface at all).
//
// Any client NOT holding a live control poll (an old shell binary, a
// browser, curl, any other local process) is served "normal" whenever the
// live mode hides OS chrome. THIS SINGLE DOWNGRADE IS THE FAIL-CLOSED
// GUARANTEE (ADR-0064 Decision 3): "the window is locked down" and "the
// exit works" become the same fact over the same channel, so a client that
// cannot leave kiosk/fullscreen is structurally never told to enter it —
// the ut-docs#1039 trap (undecorated fullscreen with a dead exit) cannot
// be reassembled by a forgotten check.
//
// Auth: stays in auth.exempt() — the shell reads this before any operator
// has signed in, and it exposes two display preferences, no more sensitive
// than /healthz. A hostile local reader learns the window mode and can
// send a false applied= acknowledgement, which can only make the
// operator's exit-to-os success message arrive early — it cannot stop the
// real shell applying, because the live mode is idempotent state that is
// re-read, not a command that is consumed (ADR-0064 Decision 2).
//
// Long-poll safety: internal/server/server.go's http.Server sets no
// WriteTimeout (only the control listener in the shell sets
// ReadHeaderTimeout), so holding a request for up to ShellPollMaxWait
// (25s) is safe. If a WriteTimeout is ever added there, it must exceed
// ShellPollMaxWait or every shell poll will be severed mid-hold.
func registerWindowState(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /api/window-mode", func(w http.ResponseWriter, r *http.Request) {
		st := d.CurrentState()
		q := r.URL.Query()
		live := q.Get("control") == "live"

		var mode string
		var rev uint64
		switch sh := d.Shell; {
		case sh == nil:
			// Bare-Deps tests only — pages.Init always wires Shell. Serve
			// the persisted preference with the same fail-closed downgrade.
			mode, rev = common.ClampWindowMode(st.WindowMode), 1
			if !live && common.IsChromeHiding(mode) {
				mode = common.DefaultWindowMode
			}
		case live:
			// Heartbeat + acknowledgement first, so the exit-to-os
			// handler's WaitApplied sees the ack even while this request
			// then parks in a long poll.
			sh.NoteSeen(q.Get("applied"))
			since, _ := strconv.ParseUint(q.Get("since"), 10, 64)
			waitSec, _ := strconv.Atoi(q.Get("wait"))
			wait := time.Duration(waitSec) * time.Second
			if wait < 0 {
				wait = 0
			}
			if wait > common.ShellPollMaxWait {
				wait = common.ShellPollMaxWait
			}
			if wait > 0 {
				// r.Context() so a client disconnect releases the handler
				// goroutine immediately instead of leaking it for the wait.
				mode, rev = sh.Wait(r.Context(), since, wait)
			} else {
				mode, rev = sh.Snapshot()
			}
		default:
			mode, rev = sh.Snapshot()
			if common.IsChromeHiding(mode) {
				// The fail-closed downgrade — see the doc comment above.
				mode = common.DefaultWindowMode
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"window_mode":       mode,
				"launch_on_startup": st.LaunchOnStartup,
				"rev":               rev,
			},
			"error": nil,
		})
	})
}
