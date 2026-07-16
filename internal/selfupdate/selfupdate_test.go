package selfupdate

import "testing"

// In-app update is supported for portable archives (.tar.gz swap) AND macOS
// .app bundles (whole-.dmg replace via applyMacApp). It must refuse a .deb
// (/usr, /opt → apt) and Windows (installer).
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
		{"deb /usr", "/usr/bin/unitill-pos", "linux", false},
		{"deb /opt", "/opt/unitill/unitill-pos", "linux", false},
		{"windows", `C:\\Program Files\\UniversalTill\\unitill-pos.exe`, "windows", false},
	}
	for _, c := range cases {
		if got := supportedFor(c.exe, c.goos); got != c.want {
			t.Errorf("%s: supportedFor(%q,%q) = %v, want %v", c.name, c.exe, c.goos, got, c.want)
		}
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
