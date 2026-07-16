package selfupdate

import "testing"

// In-app self-update is only for portable archive installs. It must refuse a
// macOS .app (breaks the signed bundle; the archive binary is unsigned), a .deb
// (/usr, /opt → apt), and Windows (installer). This is the check that would
// have caught the "clicked update, nothing happened" bug on the mac app.
func TestSupportedFor(t *testing.T) {
	cases := []struct {
		name string
		exe  string
		goos string
		want bool
	}{
		{"mac .app bundle", "/Applications/Universal Till.app/Contents/MacOS/unitill-pos", "darwin", false},
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
