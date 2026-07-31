package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// supportedFor is the OS/location POLICY gate (pure, no filesystem access):
// which install shapes can ever self-update. Windows uses its installer;
// android/ios have no self-swap; apt owns /usr. macOS is always eligible (the
// .dmg whole-bundle replace via applyMacApp). Everything else on unix is
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
		{"windows", `C:\Program Files\UniversalTill\unitill-pos.exe`, "windows", false},
		// The shipped installer (packaging/windows/installer.nsi) is a
		// per-user, non-admin install to %LOCALAPPDATA% specifically because
		// that directory IS writable by the running process — unlike Program
		// Files above. supportedFor must still refuse windows here: the
		// blocker is re-executing/overwriting a currently-running .exe (ut-docs#152
		// field report, v0.2.14), not directory writability, so the
		// dirWritable() carve-out that lets a service-writable /opt/unitill
		// self-update on linux must never extend to windows.
		{"windows per-user LOCALAPPDATA install (writable, still no self-swap)", `C:\Users\ali\AppData\Local\Programs\Universal Till\unitill-pos.exe`, "windows", false},
		// Same point for the portable .zip + run-unitill.bat layout, extracted
		// anywhere writable by the user (Desktop, Downloads, …).
		{"windows portable zip extraction (writable, still no self-swap)", `C:\Users\ali\Downloads\unitill-pos_0.2.51_windows_amd64\unitill-pos.exe`, "windows", false},
		{"android", "/data/app/com.universaltill.pos/lib/arm64/libmobile.so", "android", false},
		{"ios", "/var/containers/Bundle/Application/x/Universal Till.app/unitill-pos", "ios", false},
	}
	for _, c := range cases {
		if got := supportedFor(c.exe, c.goos); got != c.want {
			t.Errorf("%s: supportedFor(%q,%q) = %v, want %v", c.name, c.exe, c.goos, got, c.want)
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
