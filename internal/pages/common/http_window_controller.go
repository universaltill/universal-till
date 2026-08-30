package common

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// EnvDesktopControlAddr/EnvDesktopControlToken are the env vars
// unitill-desktop (the macOS/Windows/Linux desktop shell) sets on the
// unitill-pos child it spawns (ut-docs#882) — the address and bearer token
// for its own loopback-only control listener, mirroring the existing
// UT_LISTEN_ADDR pattern (cmd/unitill-desktop/desktop.go) for the reverse
// direction. There is no shared package between the two binaries to hold
// one constant; cmd/unitill-desktop/control.go's own envDesktopControlAddr/
// envDesktopControlToken must be kept byte-identical to these values —
// cmd/unitill-desktop/control_test.go asserts it.
const (
	EnvDesktopControlAddr  = "UT_DESKTOP_CONTROL_ADDR"
	EnvDesktopControlToken = "UT_DESKTOP_CONTROL_TOKEN"
)

// controlTokenHeader must match cmd/unitill-desktop/control.go's own
// controlTokenHeader constant — same cross-binary-string-contract caveat
// as the env var names above.
const controlTokenHeader = "X-UT-Control-Token"

// httpWindowControllerTimeout bounds one control-channel call: long enough
// for a Dispatch-queued native call to actually run (the UI thread is
// normally idle, not busy), short enough that a wedged or crashed shell
// can't hang the Settings HTTP handler indefinitely.
const httpWindowControllerTimeout = 3 * time.Second

// httpWindowControllerErrBodyLimit bounds how much of a non-2xx response
// body gets folded into the returned error — enough to carry a real
// message (e.g. errNoOps's "desktop shell window not ready"), never enough
// for a misbehaving/compromised shell to bloat an operator-facing error.
const httpWindowControllerErrBodyLimit = 256

// HTTPWindowController is the WindowController that talks to a running
// unitill-desktop shell over its loopback control channel (ut-docs#882) —
// the live, no-relaunch counterpart to #611's next-launch-only apply. Used
// only when EnvDesktopControlAddr is present, which unitill-desktop alone
// sets; a plain browser session, a headless server, or a shell built before
// this card all leave it unset and keep NoopWindowController (see
// NewHTTPWindowControllerFromEnv).
//
// The bearer token is a second, independent layer alongside the PIN check
// that stays entirely inside the callers of this type (settings_page.go) —
// it proves this process is the one unitill-desktop actually spawned, not
// some other loopback listener; it is not itself an authorization decision.
type HTTPWindowController struct {
	addr   string
	token  string
	client *http.Client
}

// NewHTTPWindowControllerFromEnv returns (controller, true) when
// EnvDesktopControlAddr is set, else (nil, false) — pages.Init falls back
// to NoopWindowController in the false case, the same convention every
// other WindowController selection here already follows. A set address
// with an empty/missing token still returns a controller (the token is
// simply wrong on every call, which the shell's control server already
// turns into a clear 403 — no separate "shell predates the token" case to
// special-case here).
func NewHTTPWindowControllerFromEnv() (WindowController, bool) {
	addr := strings.TrimSpace(os.Getenv(EnvDesktopControlAddr))
	if addr == "" {
		return nil, false
	}
	return HTTPWindowController{
		addr:   addr,
		token:  strings.TrimSpace(os.Getenv(EnvDesktopControlToken)),
		client: &http.Client{Timeout: httpWindowControllerTimeout},
	}, true
}

// ExitToOS asks the shell to leave kiosk/fullscreen now.
func (c HTTPWindowController) ExitToOS() error { return c.post("/exit-to-os", nil) }

// ApplyMode asks the shell to apply mode to its native window now, for a
// live (not next-launch) Settings toggle.
func (c HTTPWindowController) ApplyMode(mode string) error {
	return c.post("/apply-mode", url.Values{"mode": {mode}})
}

// InputHeartbeat relays a kiosk page's own input-liveness signal to the
// shell's control server (ut-docs#1329), which records it for the
// on-demand snapshot (cmd/unitill-desktop/control.go's GET /snapshot).
// Same falls-back-safely shape as ExitToOS/ApplyMode's post() — the caller
// (registerKioskHeartbeat) never surfaces this as an operator-facing error,
// it's a passive diagnostic signal, not an action.
func (c HTTPWindowController) InputHeartbeat() error {
	return c.post("/input-heartbeat", nil)
}

// post calls one control-channel endpoint. Every failure path here is the
// "falls back safely" scenario ut-docs#882's acceptance criteria requires:
// unreachable (the shell exited, or predates this card and never started
// the listener), 503 (the shell is up but has no live handler wired for
// this platform yet — see cmd/unitill-desktop/webkit_darwin.go), 403 (a
// missing/wrong token — should not happen against our own shell, but is
// never mistaken for success if it does), or any other non-2xx — all
// surfaced to the caller (settings_page.go's existing handlers) as a clear
// error, matching how they already report a WindowCtl failure to the
// operator.
func (c HTTPWindowController) post(path string, form url.Values) error {
	req, err := http.NewRequest(http.MethodPost, "http://"+c.addr+path, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("desktop shell control channel request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(controlTokenHeader, c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("desktop shell control channel unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, httpWindowControllerErrBodyLimit))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			return fmt.Errorf("desktop shell control channel: %s", resp.Status)
		}
		return fmt.Errorf("desktop shell control channel: %s: %s", resp.Status, msg)
	}
	return nil
}
