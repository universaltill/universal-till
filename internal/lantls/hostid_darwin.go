//go:build darwin && !ios

package lantls

import "golang.org/x/sys/unix"

// readHostID reads the Mac's hardware UUID (IOPlatformUUID, as sysctl
// kern.uuid), which a migrated or copied data folder doesn't carry.
func readHostID() (source, raw string) {
	v, err := unix.Sysctl("kern.uuid")
	if err != nil {
		return "", ""
	}
	if id := cleanHostID(v); id != "" {
		return "platform-uuid", id
	}
	return "", ""
}
