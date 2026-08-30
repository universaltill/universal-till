package pages

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
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

	// POST /api/window/input-heartbeat (ut-docs#1329, split from #1228's
	// input-freeze incident: kiosk stops responding to all touch input,
	// backend/app internals stay healthy, only a unitill-desktop restart
	// clears it) forwards a "this kiosk saw real user input just now"
	// signal to WindowCtl, which — on the desktop-shell platforms —
	// carries it on to unitill-desktop's own control channel
	// (HTTPWindowController.RecordInputHeartbeat), recorded there and
	// surfaced via that process's own GET /diagnostics. Diagnosability
	// plumbing only: nothing here acts on a freeze, it only leaves a trail
	// for a human investigating one later.
	//
	// Auth tier, deliberately NOT PIN/manager-gated like
	// POST /api/settings/exit-to-os or checkOrElevate-gated like
	// POST /api/settings/window-mode above: web/public/input-heartbeat.js
	// fires this on every genuine touch/click/key across every
	// base.html-rendered page, throttled to ~once per 5s — a manager PIN
	// prompt or elevation dialog on that cadence would be unusable, and
	// the destructive-action reasoning those two routes gate on (leaving
	// kiosk lockdown, changing the window mode) doesn't apply here at all.
	// Left OUT of auth.exempt() instead: the plain signed-in-session tier
	// every other non-elevated /api/* route already sits behind by
	// default — no PIN, but not open to an anonymous LAN caller either,
	// same "authenticated but not re-authenticated" shape as
	// GET /api/window-mode?control=live's own precedent for a frequent,
	// low-cost, non-destructive poll (that one is pre-login-exempt for a
	// different reason — the shell reads it before any operator exists at
	// all — but is the closest existing "cheap and frequent" example).
	// Every page this ships on (base.html) already carries the session
	// cookie the middleware checks, so this never prompts anything new.
	// heartbeatMu/heartbeatLastAt log receipt at THIS layer, independent of
	// whether WindowCtl.RecordInputHeartbeat below has anywhere to forward
	// to (review of ut-docs#1329, should-fix 5): on the Pi kiosk appliance
	// (KioskSystemdWindowController), Android
	// (AndroidNativeWindowController), and a pure attach-mode desktop
	// shell (ShellPollWindowController with a nil fallback — the common
	// .deb-install topology, ADR-0064), RecordInputHeartbeat is a
	// documented no-op: unitill-desktop's own control server never sees
	// the signal, so ITS "first heartbeat"/"resumed after a gap" log
	// lines (cmd/unitill-desktop/control.go) never fire either. Logging
	// the same two facts unconditionally here covers every topology with
	// no new channel, satisfying the "log line" half of the card's
	// acceptance criteria universally rather than only on the
	// desktop-shell-with-fallback path.
	var heartbeatMu sync.Mutex
	var heartbeatLastAt time.Time
	mux.HandleFunc("POST /api/window/input-heartbeat", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		heartbeatMu.Lock()
		prev := heartbeatLastAt
		heartbeatLastAt = now
		heartbeatMu.Unlock()
		switch {
		case prev.IsZero():
			logging.L().Infof("input heartbeat: first heartbeat received")
		case now.Sub(prev) > 2*time.Minute:
			logging.L().Infof("input heartbeat: resumed after a %s gap", now.Sub(prev).Round(time.Second))
		}

		wc := d.WindowCtl
		if wc == nil {
			wc = common.NoopWindowController{}
		}
		if err := wc.RecordInputHeartbeat(); err != nil {
			// Best-effort telemetry: log for whoever investigates a freeze
			// later, but still answer success — a delivery failure here
			// must never surface as an error to the kiosk page's own JS
			// (which ignores the response either way, see
			// web/public/input-heartbeat.js) or slow it down with a retry.
			// Infof, not Errorf/Warnf (review of ut-docs#1329, blocker 2
			// — same recentBuf-flooding risk as
			// ShellPollWindowController's own forward-failure log).
			logging.L().Infof("input heartbeat forward: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
