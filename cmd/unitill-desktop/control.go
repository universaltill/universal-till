// Cross-process control channel (ut-docs#882) — deliberately free of any
// cgo/OS build tag, like window_mode.go, so `go test ./...` (which never
// passes `-tags desktop`, see stub.go) actually exercises it. Only the
// per-OS wiring that turns windowOps into real native calls
// (webview_fallback.go / webkit_darwin.go) needs the desktop tag.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// envDesktopControlAddr is the env var this shell sets on the unitill-pos
// child it spawns — the address of the loopback listener below — mirroring
// desktop.go's existing UT_LISTEN_ADDR pattern for the reverse direction.
// Must match common.EnvDesktopControlAddr on the unitill-pos side
// (internal/pages/common/http_window_controller.go); there is no shared
// package between the two binaries to hold one constant, so the string
// itself is the contract — the go:generate-free, cross-binary equivalent of
// what UT_LISTEN_ADDR/UT_OPEN_BROWSER already do.
const envDesktopControlAddr = "UT_DESKTOP_CONTROL_ADDR"

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

// controlServer is the loopback-only HTTP listener unitill-desktop starts
// before spawning its unitill-pos child — the channel that lets the
// PIN-gated POST /api/settings/exit-to-os handler (which runs inside
// unitill-pos, a separate OS process with no shared memory with the window)
// take effect immediately instead of waiting for next launch. Bound to
// 127.0.0.1:0 only, an OS-assigned free port — this is a local escape
// hatch, never a network control surface, and adds no new authorization
// path: the PIN check stays entirely inside unitill-pos's existing handler,
// this channel only carries the already-authorized command.
type controlServer struct {
	ln  net.Listener
	srv *http.Server

	mu  sync.RWMutex
	ops *windowOps
}

// newControlServer binds the loopback listener and starts serving in the
// background. Returns an error if the bind itself fails (never expected on
// 127.0.0.1:0 in practice) — the caller treats that the same as "no shell
// listening": log and continue without a live channel, never block the
// window from opening.
func newControlServer() (*controlServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("control listener: %w", err)
	}
	cs := &controlServer{ln: ln}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /exit-to-os", cs.handleExitToOS)
	mux.HandleFunc("POST /apply-mode", cs.handleApplyMode)
	cs.srv = &http.Server{Handler: mux}
	go func() { _ = cs.srv.Serve(ln) }()
	return cs, nil
}

// Addr is the listener's own loopback address ("127.0.0.1:<port>"), passed
// to the unitill-pos child via envDesktopControlAddr.
func (cs *controlServer) Addr() string { return cs.ln.Addr().String() }

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
	if err := ops.ApplyMode(r.Form.Get("mode")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Close shuts the listener down; best-effort, called as the shell exits.
func (cs *controlServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return cs.srv.Shutdown(ctx)
}
