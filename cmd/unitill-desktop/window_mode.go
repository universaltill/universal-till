// Pure window-mode logic (ut-docs#611) — deliberately free of any cgo/OS
// build tag so `go test ./...` (which never sets `-tags desktop`, see
// stub.go's own comment) actually exercises it. The per-OS glue that turns
// this into real GTK/WebView2/Cocoa calls (window_mode_linux.go and
// friends) stays a thin, hard-to-get-wrong wrapper around it.
package main

import (
	"encoding/json"
	"net/http"
	"time"
)

// windowModeFlags describes what a persisted display.window_mode value
// (fullscreen|kiosk|maximized|normal) asks the native window to do.
// Fullscreen and kiosk map identically for the desktop shell — both mean
// "hide OS chrome," per #549's own framing ("Fullscreen/Kiosk (no OS
// chrome/menu bar)"); the Pi kiosk path (a completely different,
// non-windowed mechanism) is out of scope for this card, see ut-docs#883.
type windowModeFlags struct {
	Fullscreen bool
	Decorated  bool
	Maximize   bool
}

// flagsForWindowMode never fails — an unrecognized mode (a future value
// this build predates, or a fetch that came back malformed upstream)
// degrades to "normal" rather than risking a window that can't be escaped.
func flagsForWindowMode(mode string) windowModeFlags {
	switch mode {
	case "fullscreen", "kiosk":
		return windowModeFlags{Fullscreen: true, Decorated: false}
	case "maximized":
		return windowModeFlags{Decorated: true, Maximize: true}
	default: // "normal" and anything unrecognized
		return windowModeFlags{Decorated: true}
	}
}

// shellPrefs is the desktop shell's own read of the till's persisted
// display preferences (ut-docs#611) — both applied shell-side, at this
// process's own launch, because both need something only this process
// has: window-mode needs the actual native window (owned by this process,
// not the unitill-pos server it spawns as a child); launch-on-startup
// needs the actual interactive-user identity this process runs as (on a
// .deb install, unitill-pos runs as the unprivileged system user `pos`,
// which has no autostart directory a login session ever reads — the
// review finding that moved this out of the server entirely).
type shellPrefs struct {
	WindowMode      string
	LaunchOnStartup bool
}

// defaultShellPrefs is what a fetch failure degrades to — never blocks the
// window from opening, never enables autostart nobody asked for.
var defaultShellPrefs = shellPrefs{WindowMode: "normal", LaunchOnStartup: false}

// fetchShellPrefs asks the just-started server (GET /api/window-mode,
// unauthenticated by design — ut-docs#611) for both persisted preferences
// in one round trip. Falls back to defaultShellPrefs on any failure
// (unreachable server, non-200 status, malformed body, an unrecognized
// mode) — a settings-fetch problem must never block the window from
// opening or crash the shell.
func fetchShellPrefs(baseURL string) shellPrefs {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/api/window-mode")
	if err != nil {
		return defaultShellPrefs
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return defaultShellPrefs
	}

	var body struct {
		Data struct {
			WindowMode      string `json:"window_mode"`
			LaunchOnStartup bool   `json:"launch_on_startup"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return defaultShellPrefs
	}

	mode := body.Data.WindowMode
	switch mode {
	case "fullscreen", "kiosk", "maximized", "normal":
	default:
		mode = "normal"
	}
	return shellPrefs{WindowMode: mode, LaunchOnStartup: body.Data.LaunchOnStartup}
}
