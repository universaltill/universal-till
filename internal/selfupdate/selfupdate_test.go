package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// supportedFor is the OS/location POLICY gate (pure, no filesystem access):
// which install shapes can ever self-update. Windows runs its signed
// installer (ut-docs#160); android/ios have no self-swap; apt owns /usr. macOS is always
// location-eligible here (the .dmg whole-bundle replace via applyMacApp) —
// the arch gate (only arm64 .dmgs are ever published, ut-docs#18) is applied
// by Supported() via macAppBundleSupported, NOT here, same split as the
// writability precondition below. Everything else on unix is
// location-eligible — the final precondition (is the binary's dir actually
// writable by the running service user?) is applied by Supported() via
// dirWritable, NOT here. /opt/unitill is deliberately NOT blocklisted: the Pi
// kiosk installs there as the service user, and that install self-updates
// fine — the old blanket /opt block was the bug behind the kiosk dead-end
// (board ut-docs#147).
func TestSupportedFor(t *testing.T) {
	cases := []struct {
		name string
		exe  string
		goos string
		want bool
	}{
		{"mac .app bundle", "/Applications/Universal Till.app/Contents/MacOS/unitill-pos", "darwin", true},
		{"mac portable archive", "/Users/ali/unitill/unitill-pos", "darwin", true},
		{"linux portable archive", "/home/ali/unitill/unitill-pos", "linux", true},
		{"linux /opt kiosk install (writability decides in Supported)", "/opt/unitill/bin/unitill-pos", "linux", true},
		{"deb /usr (apt's domain)", "/usr/bin/unitill-pos", "linux", false},
		// ut-docs#160: Windows is location-eligible; the release's signed
		// installer does the update once this process has stopped, so the
		// running-.exe rename problem (ut-docs#152) never arises. Supported()
		// adds the precondition — a writable installer install
		// (uninstall.exe), not a portable zip (wininstaller_test.go).
		{"windows per-user LOCALAPPDATA install", `C:\Users\ali\AppData\Local\Programs\Universal Till\unitill-pos.exe`, "windows", true},
		{"windows portable zip (Supported refuses: no uninstall.exe)", `C:\Users\ali\Downloads\unitill-pos_0.2.51_windows_amd64\unitill-pos.exe`, "windows", true},
		{"android", "/data/app/com.universaltill.pos/lib/arm64/libmobile.so", "android", false},
		{"ios", "/var/containers/Bundle/Application/x/Universal Till.app/unitill-pos", "ios", false},
	}
	for _, c := range cases {
		if got := supportedFor(c.exe, c.goos); got != c.want {
			t.Errorf("%s: supportedFor(%q,%q) = %v, want %v", c.name, c.exe, c.goos, got, c.want)
		}
	}
}

// DownloadLinkActionable is the single source of truth for "can a user on
// this OS get the new version themselves by clicking a website link", shared
// by both the Settings-page fallback (internal/pages/update_api.go) and the
// status-bar chip (web/ui/layouts/base.html, ut-docs#159) so the two never
// drift into separately-maintained copies of the same windows||darwin check.
// Windows and macOS are windowed desktop OSes with a browser — actionable.
// A unix kiosk is fullscreen with no browser chrome — a website link there
// is a dead end (ut-docs#147/#159), so it must report false.
func TestDownloadLinkActionable(t *testing.T) {
	cases := []struct {
		goos string
		want bool
	}{
		{"windows", true},
		{"darwin", true},
		{"linux", false},
		{"android", false},
		{"ios", false},
	}
	for _, c := range cases {
		if got := DownloadLinkActionable(c.goos); got != c.want {
			t.Errorf("DownloadLinkActionable(%q) = %v, want %v", c.goos, got, c.want)
		}
	}
}

// dirWritable is the real self-update precondition: the rename-based binary
// swap (os.Rename within the binary's directory) only succeeds where that
// directory is writable by the current process. A service-writable /opt/unitill
// kiosk install passes; a root-owned one (or a missing dir) fails — which is
// exactly why the kiosk should report "no in-app update" instead of dead-ending
// at a website link it can't act on.
func TestDirWritable(t *testing.T) {
	// A freshly-created temp dir is writable by us.
	if !dirWritable(t.TempDir()) {
		t.Errorf("expected a temp dir to be writable")
	}
	// A path that does not exist can never be swapped into. (Deterministic
	// regardless of uid — unlike a chmod 0555 dir, which root would still
	// write, flaking in root-run CI containers.)
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if dirWritable(missing) {
		t.Errorf("expected a non-existent dir to be reported not writable")
	}
	// Sanity: a real file's parent (the temp dir) is writable, a bare file is
	// not a directory we can create siblings in only if its parent isn't.
	f := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(f, 0o755); err != nil {
		t.Fatal(err)
	}
	if !dirWritable(f) {
		t.Errorf("expected an owned 0755 dir to be writable")
	}
}

// appBundlePath extracts the enclosing .app from a bundle exe path, else "".
func TestAppBundlePath(t *testing.T) {
	if got := appBundlePath("/Applications/Universal Till.app/Contents/MacOS/unitill-pos"); got != "/Applications/Universal Till.app" {
		t.Errorf("bundle path = %q", got)
	}
	if got := appBundlePath("/Users/ali/unitill/unitill-pos"); got != "" {
		t.Errorf("non-bundle should be empty, got %q", got)
	}
}

// macAppBundleSupported gates Supported()'s darwin .app-bundle branch: only
// arm64 dmgs are ever published (.goreleaser.yaml + the macos-app release job
// both build arm64 only), so an Intel Mac must never see "Update now" — it
// would only ever fail at applyMacApp's own Intel refusal (macapp_darwin.go).
// Pure/parameterized (ut-docs#18) so this is unit-testable without real Intel
// hardware or a darwin build, mirroring supportedFor's exe/goos param seam.
func TestMacAppBundleSupported(t *testing.T) {
	if !macAppBundleSupported("arm64") {
		t.Error("arm64 (the only published .dmg arch) should be supported")
	}
	if macAppBundleSupported("amd64") {
		t.Error("Intel (amd64) must not be supported — no Intel .dmg is ever published")
	}
}
