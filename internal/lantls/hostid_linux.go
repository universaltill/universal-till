//go:build linux && !android

package lantls

import "os"

// readHostID prefers the board's serial from the devicetree (a Raspberry
// Pi and most ARM boards): it is the hardware's, so an SD-card clone into
// another board is caught. Without one, the OS's machine-id, which a
// copied data folder doesn't carry but a whole-disk image does.
func readHostID() (source, raw string) {
	if b, err := os.ReadFile("/sys/firmware/devicetree/base/serial-number"); err == nil {
		if id := cleanHostID(string(b)); id != "" {
			return "dt-serial", id
		}
	}
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil {
			if id := cleanHostID(string(b)); id != "" {
				return "machine-id", id
			}
		}
	}
	return "", ""
}
