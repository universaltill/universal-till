package pages

import (
	"net/http"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// kioskHeartbeatMinInterval bounds how often a heartbeat POST actually
// reaches Deps.WindowCtl (review of ut-docs#1329). Unlike every other
// auth-exempt route in this repo, this one carries no PIN/token/bearer of
// its own and — unlike the shell's OWN control server, which is
// loopback-only — is reachable from anywhere on the shop LAN. Without a
// server-side floor, a single misbehaving or hostile LAN client could fire
// unbounded concurrent POSTs, each fanning out to an up-to-3s outbound
// call (common.HTTPWindowController's own httpWindowControllerTimeout)
// against the shell's control listener — a real resource-exhaustion shape
// against the same channel exit-to-os depends on. The kiosk page's own
// 15s client-side throttle (web/public/kiosk-heartbeat.js) is trivially
// bypassed by anything that isn't that exact script, so this floor is the
// real bound, not a duplicate of it. Well under the client's own interval
// so a legitimate heartbeat is never itself throttled.
const kioskHeartbeatMinInterval = 2 * time.Second

// registerKioskHeartbeat exposes an unauthenticated endpoint (ut-docs#1329,
// split from #1228's Pi5-1 input-freeze incident) that relays a kiosk
// page's own input-liveness signal to whatever live desktop-shell control
// channel Deps.WindowCtl grants (Deps.WindowCtl.InputHeartbeat), so the
// shell's on-demand snapshot (cmd/unitill-desktop's GET /snapshot) has
// something to report the next time a till strands to all input.
//
// KNOWN GAP (review of ut-docs#1329, round 2 — not fixed in this change,
// documented rather than silently shipped as if complete): this only
// reaches the shell in SPAWN mode (an old shell binary that hands its
// unitill-pos child an HTTPWindowController via env vars). The ADR-0064
// ATTACH-mode default — the real topology on a .deb/Pi install, unitill-pos
// running as its own systemd service, `ShellPollWindowController` with a
// nil fallback — has NO push channel at all ("all traffic shell → server,
// never the reverse", see ShellPollWindowController's own doc comment) and
// no way for this process to even discover the shell's control-server
// address/token (those env vars are set only for a spawned child). So on
// Pi5-1's actual configuration, InputHeartbeat is presently a no-op and
// GET /snapshot on the shell's control server stays permanently at the -1
// "never happened" sentinel. Closing this needs the heartbeat signal
// threaded through the EXISTING bidirectional long-poll instead (GET
// /api/window-mode?control=live already carries shell→server traffic every
// few seconds; add a last-input-age field there and have the shell cache
// it locally for its own /snapshot to report) — not a new discovery/secret
// mechanism between the two processes. Left as a follow-up rather than
// attempted here: it touches window_state_api.go's long-poll response and
// the shell's own poll client (shell_poll.go/webview_fallback.go), real
// architecture surface beyond this card's original scope.
//
// Auth: added to auth.exempt() alongside GET /api/window-mode — a freeze
// can happen before an operator ever signs in (the login screen is exactly
// where a stuck till most often sits), so gating this behind a session
// would silence the one signal that matters most exactly when the till
// needs it most. Safe to leave open: the handler takes no action beyond a
// throttled, fire-and-forget nudge into the same loopback-token-authed
// channel ExitToOS/ApplyMode already use — no new trust boundary, same
// reasoning HTTPWindowController's own doc comment gives for that
// channel's second, independent auth layer — and kioskHeartbeatMinInterval
// above bounds the abuse surface an unauthenticated LAN-reachable route
// would otherwise open.
func registerKioskHeartbeat(mux *http.ServeMux, d *common.Deps) {
	var mu sync.Mutex
	var lastForwarded time.Time

	mux.HandleFunc("POST /api/kiosk/input-heartbeat", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		throttled := time.Since(lastForwarded) < kioskHeartbeatMinInterval
		if !throttled {
			lastForwarded = time.Now()
		}
		mu.Unlock()

		// Always 204 regardless of throttling — this is fire-and-forget
		// from the caller's perspective (a throttled or no-live-channel
		// heartbeat is never surfaced as an error, same as the relay
		// failure case below).
		if !throttled {
			wc := d.WindowCtl
			if wc == nil {
				wc = common.NoopWindowController{}
			}
			// Best-effort: no live shell channel is the ordinary case (a
			// plain browser session, or a shell not yet attached), never
			// surfaced to the page as an error. Logged at Debug only —
			// this fires on a throttled timer from every kiosk page, too
			// frequent to warrant anything louder for the ordinary
			// "nothing to relay to" case.
			if err := wc.InputHeartbeat(); err != nil {
				logging.L().Debugf("kiosk input heartbeat: %v", err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
