package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ut-docs#2733 review: the "reinstall the .deb" chip must fire only when the
// install is a self-updatable shape whose folder the till can't write — not
// for every reason Supported() says no (a /usr apt install, an unknown exe).
func TestLinuxInstallUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a 0555 directory; run as a normal user")
	}
	oldExe, oldGOOS := osExecutable, hostGOOS
	t.Cleanup(func() { osExecutable, hostGOOS = oldExe, oldGOOS })
	hostGOOS = "linux"

	ro := t.TempDir()
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	rw := t.TempDir()
	t.Chdir(rw)

	cases := []struct {
		name string
		exe  string
		err  error
		want bool
	}{
		{"read-only install folder", filepath.Join(ro, "unitill-pos"), nil, true},
		{"writable install folder", filepath.Join(rw, "unitill-pos"), nil, false},
		{"apt-owned /usr install: not a folder problem", "/usr/bin/unitill-pos", nil, false},
		{"unknown executable", "", errors.New("boom"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			osExecutable = func() (string, error) { return c.exe, c.err }
			if got := LinuxInstallUnwritable(); got != c.want {
				t.Fatalf("LinuxInstallUnwritable() = %v, want %v", got, c.want)
			}
		})
	}

	hostGOOS = "darwin"
	osExecutable = func() (string, error) { return filepath.Join(ro, "unitill-pos"), nil }
	if LinuxInstallUnwritable() {
		t.Fatal("non-linux must never report the .deb remedy")
	}
}
