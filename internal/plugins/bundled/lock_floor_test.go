package bundled

import (
	"fmt"
	"testing"
)

// minLocked is the lowest release of each payment plugin the lock may pin
// (ut-docs#6565). The lock sat on stripe 1.5.0 / sumup 1.5.1 while the live
// plugins were 1.9.x, so the App Store build lacked reader_id, the
// payment.<key>.readers hook, per-leg refund and fail-closed refund. CI has no
// network, so it cannot ask GitHub for the latest release: when a plugin
// release adds something the App Store build needs, raise the floor here in
// the same PR that refreshes bundled.lock.json (procedure in the lock's
// _comment). Checking the lock against `gh release list` before every release
// is part of the release checklist.
var minLocked = map[string][3]int{
	"com.universaltill.payment-stripe": {1, 10, 0},
	"com.universaltill.payment-sumup":  {1, 9, 2},
	"com.universaltill.tax-uk":         {1, 3, 1},
}

func TestCommittedLock_IsNotBehindTheFloor(t *testing.T) {
	l, err := compiledLock()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range l.Plugins {
		floor, ok := minLocked[e.ID]
		if !ok {
			t.Errorf("%s has no floor in minLocked", e.ID)
			continue
		}
		var got [3]int
		if _, err := fmt.Sscanf(e.Version, "%d.%d.%d", &got[0], &got[1], &got[2]); err != nil {
			t.Fatalf("%s: version %q: %v", e.ID, e.Version, err)
		}
		if got[0] < floor[0] || (got[0] == floor[0] && (got[1] < floor[1] || (got[1] == floor[1] && got[2] < floor[2]))) {
			t.Errorf("%s pinned at %s, below the floor %d.%d.%d: refresh bundled.lock.json (ut-docs#6565)", e.ID, e.Version, floor[0], floor[1], floor[2])
		}
		if e.ReleaseTag != "v"+e.Version {
			t.Errorf("%s: release_tag %q does not match version %s", e.ID, e.ReleaseTag, e.Version)
		}
	}
}
