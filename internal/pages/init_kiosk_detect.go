package pages

import "os"

// piKioskServiceInstalled reports whether unitill-kiosk.service is present
// on this box — see the WindowCtl selection comment in Init (ut-docs#883).
func piKioskServiceInstalled() bool {
	return kioskServiceUnitInstalledAt("/etc/systemd/system/unitill-kiosk.service")
}

func kioskServiceUnitInstalledAt(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
