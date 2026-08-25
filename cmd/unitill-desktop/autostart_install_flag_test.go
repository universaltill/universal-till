package main

// Tests for the hidden --install-autostart invocation (ut-docs#1040): the
// .deb postinstall, on a desktop-OS Pi, runs `unitill-desktop
// --install-autostart` once as the login user so the XDG autostart entry is
// written by the exact same Go code (reconcileAutostart /
// autostartEntryContents) that owns it at every normal launch — never a
// bash heredoc duplicating the entry format. Untagged (like autostart.go)
// so plain `go test ./...` exercises it without the desktop build tag.

import "testing"

func TestInstallAutostartRequested(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"no args", nil, false},
		{"empty", []string{}, false},
		{"the flag", []string{"--install-autostart"}, true},
		{"flag among others", []string{"--verbose", "--install-autostart"}, true},
		{"unrelated flag", []string{"--verbose"}, false},
		// Must be exact: a future flag sharing the prefix must not trigger
		// an install-and-exit.
		{"prefix only", []string{"--install-autostart-entry"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := installAutostartRequested(c.args); got != c.want {
				t.Errorf("installAutostartRequested(%v) = %v, want %v", c.args, got, c.want)
			}
		})
	}
}
