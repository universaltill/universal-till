package main

import (
	"strings"
	"testing"
)

// autostartEntryContents is the pure .desktop-file builder (ut-docs#611
// review fix, M2/M4) — deliberately free of any cgo/OS build tag, like
// window_mode.go, so `go test ./...` actually exercises it. The per-OS
// glue that resolves the real exec path and writes the file
// (autostart_linux.go) stays a thin, hard-to-get-wrong wrapper around it,
// and — critically — the exec path is injected here rather than resolved
// via os.Executable() inside the assertion, so this test would actually
// fail if production code named the wrong binary (the exact bug M2 found:
// the original diff wrote Exec=<unitill-pos>, not <unitill-desktop>, and
// its own test couldn't catch it because it computed its expectation the
// same wrong way).
func TestAutostartEntryContents(t *testing.T) {
	got := autostartEntryContents("/opt/unitill/bin/unitill-desktop")
	for _, want := range []string{
		"[Desktop Entry]\n",
		"Type=Application\n",
		"Name=Universal Till\n",
		"Comment=Offline-first point of sale\n",
		"Exec=/opt/unitill/bin/unitill-desktop\n",
		"Icon=universal-till\n",
		"Terminal=false\n",
		"Categories=Office;Finance;\n",
		"StartupNotify=false\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("autostartEntryContents missing %q in:\n%s", want, got)
		}
	}
}

// A path containing a space or a literal '%' must not silently fail to
// launch — the Desktop Entry spec requires quoting reserved characters and
// doubling a literal '%'.
func TestAutostartEntryContents_QuotesAndEscapesExec(t *testing.T) {
	got := autostartEntryContents("/home/jean dupont/unitill-desktop")
	if !strings.Contains(got, `Exec="/home/jean dupont/unitill-desktop"`+"\n") {
		t.Errorf("Exec with a space must be quoted, got:\n%s", got)
	}

	got = autostartEntryContents("/opt/100%unitill/unitill-desktop")
	if !strings.Contains(got, `Exec=/opt/100%%unitill/unitill-desktop`+"\n") {
		t.Errorf("a literal %% in the path must be doubled per the Desktop Entry spec, got:\n%s", got)
	}
}
