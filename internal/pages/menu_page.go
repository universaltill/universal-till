package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// The menu launcher: a full page of big touch tiles (a POS is a touch
// screen, so navigation is buttons, not small text links). Opened from the
// ☰ Menu button on the sale screen.

type menuTile struct {
	Href, Icon, Label string
}

// iconFor maps a nav route to a touch-friendly emoji glyph.
var iconFor = map[string]string{
	"/":                 "🧾",
	"/designer":         "🎨",
	"/inventory":        "📦",
	"/shifts":           "🕒",
	"/journal":          "📒",
	"/reports":          "📊",
	"/settings":         "⚙️",
	"/plugins":          "🧩",
	"/catalog":          "🏷️",
	"/help":             "❓",
	"/users":            "👤",
	"/locations":        "📍",
	"/registers":        "🧮",
	"/kitchen-stations": "🍳",
	"/tables":           "🪑",
	"/country-settings": "🌍",
	"/translations":     "🌐",
	"/tills":            "🖥️",
	"/report-issue":     "🐞",
}

func registerMenu(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/menu", func(w http.ResponseWriter, r *http.Request) {
		var tiles []menuTile
		add := func(href, label string) {
			icon := iconFor[href]
			if icon == "" {
				icon = "▪️"
			}
			tiles = append(tiles, menuTile{Href: href, Icon: icon, Label: label})
		}
		for _, m := range d.MenuSnapshot() {
			add(m.Href, m.Label)
		}
		add("/help", "nav.help")
		// Manager-only destinations (mirrors the session chip).
		//
		// ut-docs#866: audited, not wired onto checkOrElevate. This gate
		// controls which NAV TILES render, not a mutating+audit-writing
		// action — checkOrElevate's own doc comment (elevation.go) scopes
		// the mechanism to exactly that shape. Un-gating tile visibility
		// wouldn't itself grant any capability (each destination page is
		// independently gated), it would only let a cashier attempt
		// navigation to a page that then bounces them — the same
		// visible-but-blocked problem #870 tracks for the receipt-designer
		// link, deliberately not solved here too.
		if canPerform(d, r, "settings") {
			add("/users", "users.title")
			add("/kitchen-stations", "kitchenstations.title")
			add("/tables", "tables.title")
			add("/country-settings", "countrysettings.title")
			add("/translations", "translations.title")
			add("/report-issue", "issuereport.title")
		}
		// ut-docs#903: locations_page.go/registers_page.go moved off the
		// generic "settings" action onto their own dedicated
		// "stock_location_management" (migration 059) -- reusing "settings"
		// meant a super_admin editing that one row in role_permissions
		// (runtime-editable, permission_settings_page.go) moved stock-
		// location/register administration in lockstep with every other
		// settings-gated admin surface, with no way to grant or withhold it
		// independently. This tile gate must track that same action or the
		// tile/page desync ut-docs#901 fixed once already reappears.
		if canPerform(d, r, "stock_location_management") {
			add("/locations", "locations.title")
			add("/registers", "registers.title")
		}
		httpx.Render("ui/pages/menu.html", map[string]any{
			"title": "Menu",
			"theme": d.CurrentState().Theme,
			// menuScreen collapses the small-text top nav: the touch tiles below
			// ARE the navigation, so the header stays clean (logo + lock).
			"menuScreen": true,
			"menuItems":  d.MenuSnapshot(),
			"Tiles":      tiles,
		})(w, r)
	})
}
