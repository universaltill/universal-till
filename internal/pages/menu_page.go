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
	"/":             "🧾",
	"/designer":     "🎨",
	"/inventory":    "📦",
	"/shifts":       "🕒",
	"/journal":      "📒",
	"/reports":      "📊",
	"/settings":     "⚙️",
	"/plugins":      "🧩",
	"/catalog":      "🏷️",
	"/help":         "❓",
	"/users":        "👤",
	"/locations":    "📍",
	"/translations": "🌐",
	"/tills":        "🖥️",
	"/report-issue": "🐞",
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
		for _, m := range d.Menu {
			add(m.Href, m.Label)
		}
		add("/help", "nav.help")
		// Manager-only destinations (mirrors the session chip).
		if isManagerOrAuthOff(r) {
			add("/users", "users.title")
			add("/locations", "locations.title")
			add("/translations", "translations.title")
			add("/report-issue", "issuereport.title")
		}
		httpx.Render("ui/pages/menu.html", map[string]any{
			"title": "Menu",
			"theme": d.CurrentState().Theme,
			// menuScreen collapses the small-text top nav: the touch tiles below
			// ARE the navigation, so the header stays clean (logo + lock).
			"menuScreen": true,
			"menuItems":  d.Menu,
			"Tiles":      tiles,
		})(w, r)
	})
}
