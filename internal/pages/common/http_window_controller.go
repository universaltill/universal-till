package common

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// EnvDesktopControlAddr is the env var unitill-desktop (the macOS/Windows/
// Linux desktop shell) sets on the unitill-pos child it spawns (ut-docs#882)
// — the address of its own loopback-only control listener, mirroring the
// existing UT_LISTEN_ADDR pattern (cmd/unitill-desktop/desktop.go) for the
// reverse direction. There is no shared package between the two binaries to
// hold one constant; cmd/unitill-desktop/control.go's own
// envDesktopControlAddr must be kept byte-identical to this value.
const EnvDesktopControlAddr = "UT_DESKTOP_CONTROL_ADDR"

// httpWindowControllerTimeout bounds one control-channel call: long enough
// for a Dispatch-queued native call to actually run (the UI thread is
// normally idle, not busy), short enough that a wedged or crashed shell
// can't hang the Settings HTTP handler indefinitely.
const httpWindowControllerTimeout = 3 * time.Second

// HTTPWindowController is the WindowController that talks to a running
// unitill-desktop shell over its loopback control channel (ut-docs#882) —
// the live, no-relaunch counterpart to #611's next-launch-only apply. Used
// only when EnvDesktopControlAddr is present, which unitill-desktop alone
// sets; a plain browser session, a headless server, or a shell built before
// this card all leave it unset and keep NoopWindowController (see
// NewHTTPWindowControllerFromEnv).
type HTTPWindowController struct {
	addr   string
	client *http.Client
}

// NewHTTPWindowControllerFromEnv returns (controller, true) when
// EnvDesktopControlAddr is set, else (nil, false) — pages.Init falls back
// to NoopWindowController in the false case, the same convention every
// other WindowController selection here already follows.
func NewHTTPWindowControllerFromEnv() (WindowController, bool) {
	addr := strings.TrimSpace(os.Getenv(EnvDesktopControlAddr))
	if addr == "" {
		return nil, false
	}
	return HTTPWindowController{
		addr:   addr,
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

// post calls one control-channel endpoint. Every failure path here is the
// "falls back safely" scenario ut-docs#882's acceptance criteria requires:
// unreachable (the shell exited, or predates this card and never started
// the listener), 503 (the shell is up but has no live handler wired for
// this platform yet — see cmd/unitill-desktop/webkit_darwin.go), or any
// other non-2xx — all surfaced to the caller (settings_page.go's existing
// handlers) as a clear error, matching how they already report a WindowCtl
// failure to the operator.
func (c HTTPWindowController) post(path string, form url.Values) error {
	resp, err := c.client.PostForm("http://"+c.addr+path, form)
	if err != nil {
		return fmt.Errorf("desktop shell control channel unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("desktop shell control channel: %s", resp.Status)
	}
	return nil
}
