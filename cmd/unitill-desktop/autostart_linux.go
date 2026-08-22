//go:build desktop && linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// autostartDesktopFileName is the XDG autostart entry's filename — a fixed
// name (not per-till/per-user) since this file lives under the interactive
// user's own $XDG_CONFIG_HOME/autostart, one till per desktop account.
const autostartDesktopFileName = "unitill.desktop"

// reconcileAutostart writes or removes the XDG autostart entry for THIS
// process's own executable (ut-docs#611 review fix, Major findings M2/M3)
// — resolved via os.Executable() from inside unitill-desktop itself, the
// shell process that (a) actually owns a window and belongs in a login
// session's autostart, and (b) — critically, on a .deb install — is the
// process actually running as the interactive desktop user. The original
// version of this logic lived in internal/pages/common (the unitill-pos
// server) and got both of those wrong: it named unitill-pos's own
// executable (the headless server, not the shell with a window) in an
// entry written into unitill-pos's own home directory, which on a .deb
// install is the unprivileged system user `pos`'s (/opt/unitill), a
// directory no login session ever reads.
//
// Called once per shell launch, unconditionally reconciling to whatever
// the server's currently persisted preference says (see fetchShellPrefs in
// window_mode.go) — not only on a state transition, so an install that
// already had launch_on_startup=true persisted from #608's scaffold picks
// up a real entry on its very next launch, and an entry deleted by the
// user out-of-band comes back if the preference still says on. Same
// next-launch semantics window-mode itself already uses, and for the same
// reason (#549 explicitly allows either).
func reconcileAutostart(enabled bool) error {
	path, err := autostartEntryPath()
	if err != nil {
		return err
	}
	if !enabled {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove autostart entry: %w", err)
		}
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create autostart dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(autostartEntryContents(exe)), 0o644); err != nil {
		return fmt.Errorf("write autostart entry: %w", err)
	}
	return nil
}

// autostartEntryPath resolves ~/.config/autostart/unitill.desktop, honoring
// $XDG_CONFIG_HOME (os.UserConfigDir's own resolution).
func autostartEntryPath() (string, error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(cfgDir, "autostart", autostartDesktopFileName), nil
}
