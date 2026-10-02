package uislot

import "testing"

// ut-docs#3352 / ADR-0137: the shell's Back falls back to the page's
// declared parent when there is no in-app page to return to. The parent
// comes from the slot tables, never from a hand-kept route list.
func TestParentOf(t *testing.T) {
	cases := []struct{ path, want string }{
		// Sell is the root: no Back at all.
		{"/", ""},
		{"", ""},
		// The Menu hub returns to Sell.
		{"/menu", "/"},
		{"/menu/", "/"},
		// A Menu tile returns to the Menu.
		{"/items", "/menu"},
		{"/settings", "/menu"},
		{"/reports", "/menu"},
		{"/admin", "/menu"},
		// An Items section returns to Items — the owner's case:
		// Items -> Inventory -> Back lands on Items.
		{"/inventory", "/items"},
		{"/catalog", "/items"},
		{"/catalog/option-sets", "/items"},
		// An Administration destination returns to Administration.
		{"/country-settings", "/admin"},
		{"/registers", "/admin"},
		// A rail-only page that is no tile (Orders) returns to the Menu.
		{"/orders", "/menu"},
		// A sub-page returns to the nearest declared page above it.
		{"/settings/printers", "/settings"},
		{"/catalog/42/edit", "/catalog"},
		{"/reports/z/2026-10-01", "/reports"},
		// Anything else (a plugin page, an unknown route) returns to the Menu.
		{"/p/some-plugin/page", "/menu"},
		{"/unknown", "/menu"},
	}
	for _, c := range cases {
		if got := ParentOf(c.path); got != c.want {
			t.Errorf("ParentOf(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// Every declared Items section and Administration entry must resolve to its
// hub, so a new row added to either table gets the right Back for free.
func TestParentOf_EveryDeclaredEntry(t *testing.T) {
	for _, e := range CoreItems {
		if got := ParentOf(e.Href); got != "/items" {
			t.Errorf("Items entry %q: ParentOf = %q, want /items", e.Href, got)
		}
	}
	for _, e := range CoreMenu {
		want := "/menu"
		if e.Group == adminGroup {
			want = "/admin"
		}
		if _, isItem := coreItemsIndex[e.Href]; isItem {
			want = "/items"
		}
		if got := ParentOf(e.Href); got != want {
			t.Errorf("Menu entry %q: ParentOf = %q, want %q", e.Href, got, want)
		}
	}
}
