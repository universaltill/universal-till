package pages

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// maxParkedShellPolls caps how many GET /api/window-mode?control=live
// requests may be PARKED in a long poll at once (review of ut-docs#1039,
// finding 6). There is exactly one real shell per till, so the cap only
// needs headroom for a reconnecting shell whose previous poll hasn't timed
// out yet plus a stray local curl; over the cap the handler answers
// immediately with the current state instead of parking — degraded to the
// pre-ADR-0064 instant answer, never an error. Without a cap, each parked
// poll pins a goroutine + connection for up to ShellPollMaxWait on a
// server that sets no WriteTimeout — a cheap memory exhaustion on a Pi.
const maxParkedShellPolls = 4

// clampShellPollWait turns the raw ?wait= seconds into the duration the
// handler may actually hold the request: never negative, never more than
// ShellPollMaxWait (extracted so the clamp is numerically testable —
// review of ut-docs#1039, finding 10).
func clampShellPollWait(waitSec int) time.Duration {
	wait := time.Duration(waitSec) * time.Second
	if wait < 0 {
		return 0
	}
	if wait > common.ShellPollMaxWait {
		return common.ShellPollMaxWait
	}
	return wait
}

// isLoopbackAddr reports whether a request's RemoteAddr is a loopback
// address. The desktop shell always talks to its own till over loopback,
// so the live control channel loses nothing by refusing LAN callers — and
// gains that no LAN host can park long polls, keep Attached() true on a
// shell-less till, or spoof/suppress applied= acknowledgements (review of
// ut-docs#1039, finding 6; the repo's security-first rule).
func isLoopbackAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

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
//	  counts, and only from a loopback address — the shell is always
//	  same-host, so a non-loopback control=live is treated as a plain
//	  client (served the downgraded mode, immediately).
//	- since is the last revision the client saw; with wait > 0 the request
//	  holds until the revision moves, the wait elapses, or the client
//	  disconnects. since=0 (or any stale value) returns immediately, so a
//	  change between two polls can never be missed. At most
//	  maxParkedShellPolls requests park at once; past that the handler
//	  answers immediately.
//	- applied is the mode the shell last actually applied — its heartbeat
//	  and its acknowledgement in one, carried on the next poll rather than
//	  as a separate write endpoint (no new write surface at all). The
//	  first valid applied= after a server start may be ADOPTED as the live
//	  mode (ShellChannel.AdoptIfUntouched): the attached shell is the
//	  authority on what the window currently is, so a server restart
//	  cannot slam a window the operator just escaped back into kiosk.
//
// THE FAIL-CLOSED GUARANTEE (ADR-0064 Decision 3, tightened by the review
// of ut-docs#1039, blocker 2): a chrome-hiding mode is served ONLY when
// BOTH halves of one conjunction hold —
//
//	(1) the client holds a live control poll (control=live, loopback), AND
//	(2) the shell channel really is the exit path (Shell.IsExitPath():
//	    Deps.WindowCtl is the ShellPollWindowController consuming this
//	    channel, marked by its constructor and nothing else).
//
// (1) alone is NOT enough: on a box wired to the Pi kiosk controller
// (UT_KIOSK=1, or a leftover unitill-kiosk.service unit file — which the
// documented disable path leaves on disk, on desktop images and non-Pi
// Debian boxes too), nothing consumes this channel and exit-to-os drives
// systemd instead, so a control=live GTK shell served "kiosk" there would
// fullscreen+undecorate with no working exit — the original ut-docs#1039
// trap, rebuilt. Requiring both halves makes "the window is locked down"
// and "the exit works" genuinely one fact instead of two correlated ones.
// Everything else (an old shell binary, a browser, curl, any LAN client)
// is served "normal" whenever the live mode hides OS chrome.
//
// Auth: stays in auth.exempt() — the shell reads this before any operator
// has signed in, and it exposes two display preferences, no more sensitive
// than /healthz. A hostile LOOPBACK reader learns the window mode and can
// send a false applied= acknowledgement, which can only make the
// operator's exit-to-os success message arrive early — it cannot stop the
// real shell applying, because the live mode is idempotent state that is
// re-read, not a command that is consumed (ADR-0064 Decision 2) — and
// AdoptIfUntouched refuses to escalate into a chrome-hiding mode, so it
// cannot be used to lock the real shell down either.
//
// Long-poll safety: internal/server/server.go's http.Server sets no
// WriteTimeout (only the control listener in the shell sets
// ReadHeaderTimeout), so holding a request for up to ShellPollMaxWait
// (25s) is safe. If a WriteTimeout is ever added there, it must exceed
// ShellPollMaxWait or every shell poll will be severed mid-hold.
func registerWindowState(mux *http.ServeMux, d *common.Deps) {
	// One counted semaphore per registration: how many polls are parked
	// right now. Buffered-channel semaphore; try-acquire, never block.
	parkSlots := make(chan struct{}, maxParkedShellPolls)

	mux.HandleFunc("GET /api/window-mode", func(w http.ResponseWriter, r *http.Request) {
		st := d.CurrentState()
		q := r.URL.Query()
		live := q.Get("control") == "live" && isLoopbackAddr(r.RemoteAddr)

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
		case live && sh.IsExitPath():
			// Adoption first (one-shot per boot, see the doc comment),
			// then heartbeat + acknowledgement, so the exit-to-os
			// handler's WaitApplied sees the ack even while this request
			// then parks in a long poll.
			sh.AdoptIfUntouched(q.Get("applied"))
			sh.NoteSeen(q.Get("applied"))
			since, _ := strconv.ParseUint(q.Get("since"), 10, 64)
			waitSec, _ := strconv.Atoi(q.Get("wait"))
			wait := clampShellPollWait(waitSec)
			if wait > 0 {
				select {
				case parkSlots <- struct{}{}:
					// r.Context() so a client disconnect releases the
					// handler goroutine immediately instead of leaking it
					// for the wait.
					mode, rev = sh.Wait(r.Context(), since, wait)
					<-parkSlots
				default:
					// Park capacity exhausted — answer immediately.
					mode, rev = sh.Snapshot()
				}
			} else {
				mode, rev = sh.Snapshot()
			}
		default:
			mode, rev = sh.Snapshot()
			if common.IsChromeHiding(mode) {
				// The fail-closed downgrade — see the doc comment above.
				// Reached by plain clients AND by a control=live client
				// whose channel is not the exit path (the Pi kiosk
				// controller topology) or not loopback.
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
