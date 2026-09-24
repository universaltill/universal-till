package uislot_test

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/uislot"
)

// TestKnownIconNamesMirrorsHttpxIconNames pins ut-docs#1734's cross-package
// mirror: internal/plugins.validatePageEntryIcon validates a plugin's
// declared page-entry icon name against uislot's closed set rather than
// httpx.IconNames() — internal/plugins cannot import internal/httpx (httpx
// already imports internal/plugins for its self-update badge helpers, so
// the reverse import would cycle). This external test package may import
// both. If it goes red, knownIconNames (icon_names.go) has drifted from
// httpx's railIcons keys and must be updated to match, in either direction.
func TestKnownIconNamesMirrorsHttpxIconNames(t *testing.T) {
	want := httpx.IconNames()
	got := uislot.KnownIconNamesForTest()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("uislot's known icon names drifted from httpx.IconNames() — keep them in sync\n uislot: %v\n  httpx: %v", got, want)
	}
	for _, name := range want {
		if !uislot.IsKnownIconName(name) {
			t.Errorf("IsKnownIconName(%q) = false for a drawn httpx icon", name)
		}
	}
}
