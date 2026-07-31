//go:build darwin

package selfupdate

import (
	"context"
	"os"
	"path/filepath"
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

// End-to-end proof of Supported()'s Intel gating (ut-docs#18): unlike
// TestMacAppBundleSupported (the pure helper, unit-tested cross-platform in
// selfupdate_test.go), this exercises the real runtime.GOOS=="darwin" wiring
// inside Supported() itself — only possible on an actual darwin build/test
// run, same constraint as this file's other tests. An Intel Mac must never
// see "Update now": clicking it would only ever fail at applyMacApp's own
// Intel refusal below.
func TestSupportedFalseOnIntelMacAppBundle(t *testing.T) {
	oldExec, oldArch := osExecutable, goarch
	osExecutable = func() (string, error) {
		return "/Applications/Universal Till.app/Contents/MacOS/unitill-pos", nil
	}
	goarch = "amd64"
	t.Cleanup(func() { osExecutable, goarch = oldExec, oldArch })
	if Supported() {
		t.Error("Supported() = true for an Intel Mac .app bundle, want false — no Intel .dmg is ever published")
	}
}

// The arm64 counterpart: a real Apple Silicon Mac must still see "Update now".
func TestSupportedTrueOnArm64MacAppBundle(t *testing.T) {
	oldExec, oldArch := osExecutable, goarch
	osExecutable = func() (string, error) {
		return "/Applications/Universal Till.app/Contents/MacOS/unitill-pos", nil
	}
	goarch = "arm64"
	t.Cleanup(func() { osExecutable, goarch = oldExec, oldArch })
	if !Supported() {
		t.Error("Supported() = false for an arm64 Mac .app bundle, want true")
	}
}

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

// countLeakedWorkDirs counts leftover "ut-macupdate-*" temp dirs — used to
// confirm a failed applyMacApp cleans up the bad download rather than
// leaving it sitting in the OS temp dir.
func countLeakedWorkDirs(t *testing.T) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "ut-macupdate-*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}

// Intel Macs never have a .dmg published for them (.goreleaser.yaml builds
// darwin/arm64 only, and the macos-app release job only ever builds the
// arm64 dmg) — applyMacApp must refuse immediately with a clear error
// instead of attempting a fetch/download that can only ever fail with a
// confusing "no macOS .dmg in release" message.
func TestApplyMacAppRejectsIntelMac(t *testing.T) {
	oldArch := goarch
	goarch = "amd64"
	t.Cleanup(func() { goarch = oldArch })
	// Deliberately no newReleaseServer: releasesLatest still points at the
	// real GitHub API. If applyMacApp attempted a fetch before the arch
	// check, this test would hit the network (and likely hang/fail) instead
	// of returning immediately.
	err := applyMacApp(context.Background(), "/Applications/Universal Till.app")
	if err == nil || !strings.Contains(err.Error(), "Intel Mac") {
		t.Fatalf("err = %v, want a clear Intel Mac error", err)
	}
}

// The dmg checksum, once verified against checksums.txt, must let the flow
// proceed past the download into the mount step — proven by hitting
// hdiutil's real "mount .dmg" failure on the fixture's fake (non-dmg) bytes,
// never the checksum error.
func TestApplyMacAppDmgChecksumMatchProceeds(t *testing.T) {
	oldVer := buildinfo.Version
	buildinfo.Version = "0.1.0"
	t.Cleanup(func() { buildinfo.Version = oldVer })
	dmgName := "unitill-pos-0.2.0-macOS-arm64.dmg"
	dmg := []byte("not a real dmg, just bytes to checksum")
	newReleaseServer(t, "v0.2.0", map[string][]byte{
		dmgName:         dmg,
		"checksums.txt": []byte(sha256hex(dmg) + "  " + dmgName + "\n"),
	})
	err := applyMacApp(context.Background(), "/Applications/Universal Till.app")
	if err == nil || !strings.Contains(err.Error(), "mount .dmg") {
		t.Fatalf("err = %v, want mount failure (proving checksum passed)", err)
	}
}

// SECURITY: a mismatched dmg checksum must abort before hdiutil ever mounts
// it, and the bad download must not be left behind.
func TestApplyMacAppDmgChecksumMismatchAborts(t *testing.T) {
	oldVer := buildinfo.Version
	buildinfo.Version = "0.1.0"
	t.Cleanup(func() { buildinfo.Version = oldVer })
	before := countLeakedWorkDirs(t)
	dmgName := "unitill-pos-0.2.0-macOS-arm64.dmg"
	dmg := []byte("not a real dmg, just bytes to checksum")
	newReleaseServer(t, "v0.2.0", map[string][]byte{
		dmgName:         dmg,
		"checksums.txt": []byte(strings.Repeat("0", 64) + "  " + dmgName + "\n"),
	})
	err := applyMacApp(context.Background(), "/Applications/Universal Till.app")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	if after := countLeakedWorkDirs(t); after != before {
		t.Errorf("bad download not cleaned up: %d leaked work dirs (was %d)", after, before)
	}
}

// SECURITY: a release with a .dmg but no checksums.txt at all must fail
// closed, same as the archive path — a missing checksums.txt only ever means
// a broken or tampered release, never an unverified install.
func TestApplyMacAppDmgNoChecksumsFile(t *testing.T) {
	oldVer := buildinfo.Version
	buildinfo.Version = "0.1.0"
	t.Cleanup(func() { buildinfo.Version = oldVer })
	before := countLeakedWorkDirs(t)
	dmgName := "unitill-pos-0.2.0-macOS-arm64.dmg"
	newReleaseServer(t, "v0.2.0", map[string][]byte{dmgName: []byte("dmg bytes")}) // no checksums.txt
	err := applyMacApp(context.Background(), "/Applications/Universal Till.app")
	if err == nil || !strings.Contains(err.Error(), "checksums.txt") {
		t.Fatalf("err = %v, want refusal over missing checksums.txt", err)
	}
	if after := countLeakedWorkDirs(t); after != before {
		t.Errorf("bad download not cleaned up: %d leaked work dirs (was %d)", after, before)
	}
}

// SECURITY: checksums.txt present but missing an entry for the dmg's exact
// filename must also fail closed (e.g. goreleaser's checksums.txt not yet
// updated with the dmg line — see packaging/macos/update-checksums.sh).
func TestApplyMacAppDmgChecksumEntryMissing(t *testing.T) {
	oldVer := buildinfo.Version
	buildinfo.Version = "0.1.0"
	t.Cleanup(func() { buildinfo.Version = oldVer })
	before := countLeakedWorkDirs(t)
	dmgName := "unitill-pos-0.2.0-macOS-arm64.dmg"
	newReleaseServer(t, "v0.2.0", map[string][]byte{
		dmgName:         []byte("dmg bytes"),
		"checksums.txt": []byte(strings.Repeat("1", 64) + "  " + "some-other-file.tar.gz\n"),
	})
	err := applyMacApp(context.Background(), "/Applications/Universal Till.app")
	if err == nil || !strings.Contains(err.Error(), "checksum not found") {
		t.Fatalf("err = %v, want checksum-not-found", err)
	}
	if after := countLeakedWorkDirs(t); after != before {
		t.Errorf("bad download not cleaned up: %d leaked work dirs (was %d)", after, before)
	}
}
