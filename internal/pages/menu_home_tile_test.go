package pages

import "testing"

// TestBaseMenu_NoRedundantHomeTile (ut-docs#1829, product owner's German
// pilot merchant): the Menu launcher used to open with a "Home"/"Start"
// tile (href "/") that led back to the exact same screen the menu's own
// "← Back to sale" button already returns to — two controls, two different
// names, for one destination. baseMenu is production's actual boot-time
// tile list (internal/pages/init.go); asserting against it directly, not a
// synthetic menu, is what makes this test prove the tile is gone from the
// real till and not just from some other fixture.
func TestBaseMenu_NoRedundantHomeTile(t *testing.T) {
	for _, m := range baseMenu {
		if m.Href == "/" {
			t.Fatalf("baseMenu still has a %q tile (%+v) — back_to_sale on the Menu screen is the only way back to selling now", m.Href, m)
		}
	}
}
