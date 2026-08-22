package pages

import (
	"os"
	"path/filepath"
	"testing"
)

// TestKioskServiceUnitInstalledAt covers the second half of ut-docs#883's Pi-
// kiosk detection (review finding F1): a box already kiosked BEFORE
// ut-docs#883 landed has unitill-kiosk.service on disk even though
// pos.env/the unit's own environment never gained UT_KIOSK=1 —
// unitill-kiosk-setup.sh's own is_pi_appliance gate deliberately never
// re-triggers automatically on an upgrade (postinstall.sh: "an UPGRADE must
// never convert an existing field Pi"). Without also probing for the unit
// file, pages.Init would keep picking NoopWindowController on every such
// box, and the Settings toggle would silently do nothing — exactly the
// "silent no-op" ut-docs#883's own acceptance criteria forbids.
func TestKioskServiceUnitInstalledAt(t *testing.T) {
	dir := t.TempDir()
	unit := filepath.Join(dir, "unitill-kiosk.service")

	if kioskServiceUnitInstalledAt(unit) {
		t.Fatal("kioskServiceUnitInstalledAt = true before the unit file exists, want false")
	}
	if err := os.WriteFile(unit, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !kioskServiceUnitInstalledAt(unit) {
		t.Fatal("kioskServiceUnitInstalledAt = false once the unit file exists, want true")
	}
}
