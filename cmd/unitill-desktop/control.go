// Cross-process control channel (ut-docs#882) — deliberately free of any
// cgo/OS build tag, like window_mode.go, so `go test ./...` (which never
// passes `-tags desktop`, see stub.go) actually exercises it. Only the
// per-OS wiring that turns windowOps into real native calls
// (webview_fallback.go / webkit_darwin.go) needs the desktop tag.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// envDesktopControlAddr/envDesktopControlToken are the env vars this shell
// sets on the unitill-pos child it spawns — the address and bearer token
// for the loopback listener below — mirroring desktop.go's existing
// UT_LISTEN_ADDR pattern for the reverse direction. Must match
// common.EnvDesktopControlAddr/EnvDesktopControlToken on the unitill-pos
// side (internal/pages/common/http_window_controller.go); there is no
// shared package between the two binaries to hold one constant, so the
// string itself is the contract — control_test.go asserts both pairs stay
// byte-identical.
const (
	envDesktopControlAddr  = "UT_DESKTOP_CONTROL_ADDR"
	envDesktopControlToken = "UT_DESKTOP_CONTROL_TOKEN"
)

// controlServerReadHeaderTimeout bounds header parsing on the loopback
// listener — cheap insurance against a slow-header client tying up a
// connection (gosec G112); traffic here is exactly one same-host client
// making one small POST, so this never bites in normal operation.
const controlServerReadHeaderTimeout = 5 * time.Second

// windowOps are the real per-OS actions the control listener dispatches to
// once the native window exists. Both fields are set exactly once, by the
// UI-thread-owning showWindow (ut-docs#882) — nil before that (window
// creation still in progress) and nil forever on a platform that doesn't
// wire a live channel yet (macOS this cycle, see webkit_darwin.go and
// ut-docs#609) — callers get errNoOps, never a panic or a hang.
type windowOps struct {
	ExitToOS  func() error
	ApplyMode func(mode string) error
}

// errNoOps is what an unwired or not-yet-ready control channel returns —
// the exact "falls back safely" scenario ut-docs#882's acceptance criteria
// names, surfaced to the operator as a clear error rather than a panic or a
// silent no-op.
var errNoOps = errors.New("desktop shell window not ready")

// validWindowModes mirrors settings_page.go's own POST /api/settings/
// window-mode allowlist. The control channel re-validates rather than
// trusting unitill-pos to have already done so — this is a network-facing
// handler (loopback, but still a process boundary with its own request
// parsing), and CLAUDE.md's "validate all external input" applies to any
// boundary, not only the outermost one. An invalid/missing mode used to
// silently degrade to "normal" (flagsForWindowMode's own safe default) —
// silently un-fullscreening the till on a typo or a missing field is
// exactly the kind of surprising side effect a 400 avoids.
var validWindowModes = map[string]bool{
	"fullscreen": true,
	"kiosk":      true,
	"maximized":  true,
	"normal":     true,
}

// controlServer is the loopback-only, bearer-token-authenticated HTTP
// listener unitill-desktop starts before spawning its unitill-pos child —
// the channel that lets the PIN-gated POST /api/settings/exit-to-os
// handler (which runs inside unitill-pos, a separate OS process with no
// shared memory with the window) take effect immediately instead of
// waiting for next launch. Bound to 127.0.0.1:0 only, an OS-assigned free
// port — this is a local escape hatch, never a network control surface.
//
// The token (below) is a second, independent layer, not a replacement for
// unitill-pos's own PIN gate: the PIN check stays entirely inside that
// existing handler, deciding WHETHER the action is authorized; the token
// only proves the caller IS the unitill-pos process this shell spawned,
// not some other loopback listener (any other same-host process, or the
// shell's own WebView content, scanning ports and POSTing here directly to
// reach the native window without ever going through the PIN check at
// all — the exact bypass an unauthenticated channel would otherwise be).
type controlServer struct {
	ln        net.Listener
	srv       *http.Server
	token     string
	startedAt time.Time

	mu          sync.RWMutex
	ops         *windowOps
	lastInputAt time.Time // zero until the first /input-heartbeat (ut-docs#1329)
	mode        string    // last mode /apply-mode actually applied; "" until the first call
}

// newControlServer binds the loopback listener, mints a random bearer
// token, and starts serving in the background. Returns an error if the
// bind itself fails (never expected on 127.0.0.1:0 in practice) — the
// caller treats that the same as "no shell listening": log and continue
// without a live channel, never block the window from opening.
func newControlServer() (*controlServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("control listener: %w", err)
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("control token: %w", err)
	}
	cs := &controlServer{ln: ln, token: hex.EncodeToString(tokenBytes), startedAt: time.Now()}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /exit-to-os", cs.withAuth(cs.handleExitToOS))
	mux.HandleFunc("POST /apply-mode", cs.withAuth(cs.handleApplyMode))
	// ut-docs#1329 (split from #1228's Pi5-1 input-freeze incident):
	// input-heartbeat records that the kiosk page's own JS just saw real
	// user input; snapshot is the on-demand diagnostic dump a human reads
	// after the fact (e.g. over SSH, the same way Pi5-1 was diagnosed
	// live) — no self-recovery action, that's the sibling watchdog card.
	mux.HandleFunc("POST /input-heartbeat", cs.withAuth(cs.handleInputHeartbeat))
	mux.HandleFunc("GET /snapshot", cs.withAuth(cs.handleSnapshot))
	cs.srv = &http.Server{Handler: mux, ReadHeaderTimeout: controlServerReadHeaderTimeout}
	go func() { _ = cs.srv.Serve(ln) }()
	return cs, nil
}

// Addr is the listener's own loopback address ("127.0.0.1:<port>"), passed
// to the unitill-pos child via envDesktopControlAddr.
func (cs *controlServer) Addr() string { return cs.ln.Addr().String() }

// Token is the bearer token the caller must present in the X-UT-Control-Token
// header, passed to the unitill-pos child via envDesktopControlToken.
func (cs *controlServer) Token() string { return cs.token }

// controlTokenHeader carries the bearer token (see controlServer's own doc
// comment for why this exists alongside the PIN check, not instead of it).
const controlTokenHeader = "X-UT-Control-Token"

// withAuth rejects any request that doesn't present the exact token this
// server minted, and any request carrying an Origin header — the real
// HTTPWindowController client never sends one (it isn't a browser), so an
// Origin header's presence at all marks the request as coming from a
// browsing context (e.g. content rendered in the shell's own WebView, or a
// browser tab reaching the loopback port directly), which this channel
// must never accept regardless of the token check outcome.
func (cs *controlServer) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		got := r.Header.Get(controlTokenHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(cs.token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// SetMode records the window mode showWindow actually just applied — the
// snapshot's window_mode field (ut-docs#1329). Two kinds of callers, both
// wired by showWindow itself: (1) the launch-time apply and the ADR-0064
// polled-shell callback (webview_fallback.go), which call applyWindowMode
// directly and never touch this control server otherwise — without this
// call cs.mode would sit at its zero value "" for the entire life of the
// steady-state common case (no live Settings toggle since launch), which
// is exactly the kind of silently-wrong state ADR-0064 exists to prevent
// elsewhere in this file; (2) handleApplyMode below, for the spawn-mode
// fallback's own POST /apply-mode. Safe to call from any goroutine, same
// convention as SetOps.
func (cs *controlServer) SetMode(mode string) {
	cs.mu.Lock()
	cs.mode = mode
	cs.mu.Unlock()
}

// SetOps wires the real native-window actions once the OS-specific
// showWindow has created its window. Safe to call from any goroutine; a
// concurrent request sees either the old value or the new one, never a
// half-set struct.
func (cs *controlServer) SetOps(ops *windowOps) {
	cs.mu.Lock()
	cs.ops = ops
	cs.mu.Unlock()
}

func (cs *controlServer) currentOps() *windowOps {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.ops
}

func (cs *controlServer) handleExitToOS(w http.ResponseWriter, _ *http.Request) {
	ops := cs.currentOps()
	if ops == nil || ops.ExitToOS == nil {
		http.Error(w, errNoOps.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := ops.ExitToOS(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (cs *controlServer) handleApplyMode(w http.ResponseWriter, r *http.Request) {
	ops := cs.currentOps()
	if ops == nil || ops.ApplyMode == nil {
		http.Error(w, errNoOps.Error(), http.StatusServiceUnavailable)
		return
	}
	_ = r.ParseForm()
	mode := r.Form.Get("mode")
	if !validWindowModes[mode] {
		http.Error(w, "mode must be one of fullscreen, kiosk, maximized, normal", http.StatusBadRequest)
		return
	}
	if err := ops.ApplyMode(mode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cs.SetMode(mode)
	w.WriteHeader(http.StatusNoContent)
}

// handleInputHeartbeat records that the kiosk page's own JS just observed
// real user input (ut-docs#1329) — no window/native call, just a
// timestamp, so unlike the two handlers above this needs no windowOps at
// all and works even before the native window exists.
func (cs *controlServer) handleInputHeartbeat(w http.ResponseWriter, _ *http.Request) {
	cs.mu.Lock()
	cs.lastInputAt = time.Now()
	cs.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// snapshot is the on-demand diagnostic dump ut-docs#1329 asks for: enough
// for a human to reconstruct state after an input-freeze report without
// another anecdote. lastInputAgeMs is -1 when no heartbeat has ever
// arrived (never reached the JSON-int trap of a fabricated 0, which would
// read as "input a moment ago" on a till that has never sent one).
//
// KNOWN GAP (review of ut-docs#1329, round 2): on the ADR-0064 attach-mode
// default (the real .deb/Pi topology), lastInputAgeMs stays permanently at
// -1 — nothing on that path ever reaches POST /input-heartbeat at all. See
// internal/pages/kiosk_heartbeat_api.go's own "KNOWN GAP" comment for the
// recommended fix (route the signal through the existing long-poll
// instead). This endpoint is fully live and correct today only for a
// spawn-mode shell.
type snapshot struct {
	LastInputAgeMs int64  `json:"last_input_age_ms"`
	WindowMode     string `json:"window_mode"`
	ProcessUptimeS int64  `json:"process_uptime_s"`
	ControlAddr    string `json:"control_addr"`
}

func (cs *controlServer) handleSnapshot(w http.ResponseWriter, _ *http.Request) {
	cs.mu.RLock()
	lastInputAt, mode := cs.lastInputAt, cs.mode
	cs.mu.RUnlock()

	lastInputAgeMs := int64(-1)
	if !lastInputAt.IsZero() {
		lastInputAgeMs = time.Since(lastInputAt).Milliseconds()
	}
	snap := snapshot{
		LastInputAgeMs: lastInputAgeMs,
		WindowMode:     mode,
		ProcessUptimeS: int64(time.Since(cs.startedAt).Seconds()),
		ControlAddr:    cs.Addr(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

// Close shuts the listener down; best-effort, called as the shell exits.
// Safe to call more than once (net/http.Server.Shutdown is idempotent) —
// showWindow (webview_fallback.go / webkit_darwin.go) defers this itself,
// ordered ahead of destroying the native window, so it always runs at
// least once regardless of what main does afterward.
func (cs *controlServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return cs.srv.Shutdown(ctx)
}
