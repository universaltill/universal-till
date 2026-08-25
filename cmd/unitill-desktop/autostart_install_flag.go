// Hidden --install-autostart invocation (ut-docs#1040). Untagged, like
// autostart.go, so `go test ./...` (which never sets -tags desktop, see
// stub.go) exercises the dispatch logic; the tag-gated main() in desktop.go
// is what acts on it.
package main

// installAutostartFlag is deliberately undocumented in --help-style output
// (there is none): it exists for packaging/scripts/postinstall.sh, which —
// on a fresh install on a desktop-OS Pi — runs `unitill-desktop
// --install-autostart` once as the detected login user, so the XDG
// autostart entry is written by the exact same reconcileAutostart /
// autostartEntryContents code that owns the entry at every normal launch.
// The entry's content/format therefore has exactly one author, in Go —
// never a second bash-heredoc copy that could drift.
const installAutostartFlag = "--install-autostart"

// installAutostartRequested reports whether args (os.Args[1:]) ask for the
// install-autostart-and-exit invocation. Exact match only — a future flag
// merely sharing the prefix must not turn a window launch into an
// install-and-exit.
func installAutostartRequested(args []string) bool {
	for _, a := range args {
		if a == installAutostartFlag {
			return true
		}
	}
	return false
}
