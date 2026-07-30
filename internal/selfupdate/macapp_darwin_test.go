//go:build darwin

package selfupdate

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// Only applyMacApp's early, purely-local branches are unit-tested (release has
// no .dmg; .dmg download fails). Everything past the download — hdiutil mount,
// ditto, codesign, the detached replace-and-relaunch helper — is real macOS
// glue that must never run in a test: the helper script pkills real
// unitill-pos/unitill-desktop processes and replaces /Applications bundles.
// That remainder is deliberately uncovered, not faked.

// Apply routes a darwin .app-bundle executable into applyMacApp instead of the
// archive-swap path — proven by hitting applyMacApp's distinctive error.
func TestApplyRoutesAppBundleToMacPath(t *testing.T) {
	oldExec, oldVer := osExecutable, buildinfo.Version
	osExecutable = func() (string, error) {
		return "/Applications/Universal Till.app/Contents/MacOS/unitill-pos", nil
	}
	buildinfo.Version = "0.1.0"
	t.Cleanup(func() { osExecutable, buildinfo.Version = oldExec, oldVer })
	newReleaseServer(t, "v0.2.0", map[string][]byte{})
	err := Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no macOS .dmg") {
		t.Fatalf("err = %v, want applyMacApp's no-dmg error", err)
	}
}

func TestApplyMacAppNoDmgInRelease(t *testing.T) {
	oldVer := buildinfo.Version
	buildinfo.Version = "0.1.0"
	t.Cleanup(func() { buildinfo.Version = oldVer })
	newReleaseServer(t, "v0.2.0", map[string][]byte{"unitill-pos_0.2.0_linux_amd64.tar.gz": []byte("x")})
	err := applyMacApp(context.Background(), "/Applications/Universal Till.app")
	if err == nil || !strings.Contains(err.Error(), "no macOS .dmg") {
		t.Fatalf("err = %v, want no-dmg-in-release", err)
	}
}

func TestApplyMacAppDmgDownloadFails(t *testing.T) {
	oldVer := buildinfo.Version
	buildinfo.Version = "0.1.0"
	t.Cleanup(func() { buildinfo.Version = oldVer })
	// A nil asset body is listed in /latest but 404s on download.
	newReleaseServer(t, "v0.2.0", map[string][]byte{"unitill-pos-0.2.0-macOS-arm64.dmg": nil})
	err := applyMacApp(context.Background(), "/Applications/Universal Till.app")
	if err == nil || !strings.Contains(err.Error(), "download .dmg") {
		t.Fatalf("err = %v, want download failure", err)
	}
}
