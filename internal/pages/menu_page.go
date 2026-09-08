package pages

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// The menu launcher: a full page of big touch tiles (a POS is a touch
// screen, so navigation is buttons, not small text links). Opened from the
// ☰ Menu button on the sale screen.

type menuTile struct {
	Href, Label string
	// IconSVG is a drawn glyph from the shared icon set
	// (internal/httpx/icons.go). Every core route now has one — see
	// iconSVGFor — so this is never empty for a rendered tile; a
	// plugin-contributed route with no entry there falls back to
	// genericFallbackIcon, applied in add() below.
	IconSVG template.HTML
}

// iconSVGFor maps a nav route to a drawn icon name.
//
// ut-docs#1720 (product owner, on the real tablet) started this map with
// just Bluetooth Devices: the tile used 📶 (ANTENNA BARS) — the
// mobile-reception glyph, which says "signal strength", not "Bluetooth".
// ut-docs#76 picked it knowingly, because Unicode has no Bluetooth
// codepoint; what it could not do is make an emoji mean something Unicode
// has never encoded. ut-docs#1845 (product owner + German pilot merchant,
// "look at the design and icons — so modern and beautiful" re: a
// competitor POS) extended it to every remaining core route, retiring the
// old `iconFor` emoji map entirely: an emoji is a glyph from whatever
// colour-emoji font the platform ships, so the same build looked different
// on Android/desktop/Pi and read as unfinished ("like Windows 3.1",
// ut-docs#1830) next to a competitor's consistent line-icon set. Every
// entry here uses the icon set the nav rail already moved to
// (ut-docs#1423) rather than inventing a second mechanism.
//
// Anything added here must also be a real entry in httpx's icon map —
// TestMenuPage_EveryDrawnTileIconNameResolves is that guard, because
// icons_test.go's template scanner only sees a literal {{ icon "name" }}
// and these names are resolved in Go.
//
// This map only ever covers CORE routes — core cannot enumerate the routes
// a plugin brings with it, so a plugin-contributed tile can never get an
// entry here. See genericFallbackIcon below for that case (ut-docs#1722).
var iconSVGFor = map[string]string{
	"/":                  "receipt",
	"/designer":          "palette",
	"/inventory":         "package",
	"/shifts":            "clock",
	"/journal":           "book-open",
	"/reports":           "chart-column",
	"/settings":          "settings",
	"/plugins":           "puzzle",
	"/catalog":           "tag",
	"/help":              "help",
	"/users":             "users",
	"/locations":         "map-pin",
	"/registers":         "calculator",
	"/kitchen-stations":  "chef-hat",
	"/tables":            "table",
	"/country-settings":  "flag",
	"/translations":      "globe",
	"/tills":             "monitor",
	"/report-issue":      "bug",
	"/fiscal-register":   "clipboard-list",
	"/fiscal-device":     "receipt",
	"/orders":            "bell",
	"/bluetooth-devices": "bluetooth",
}

// genericFallbackIcon is the deliberate generic glyph a menu tile falls
// back to when its route has no entry in either map above — replacing the
// old "▪️" fallback (ut-docs#1722), which read as a rendering failure (the
// exact way it was reported, ut-docs#1371) rather than a deliberate icon.
// This is the ONLY fallback mechanism: a plugin cannot supply its own icon
// today (see the doc comment on the `add` closure below for why that's a
// deliberate, separate decision, not an oversight).
const genericFallbackIcon = "puzzle"

func registerMenu(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/menu", func(w http.ResponseWriter, r *http.Request) {
		var tiles []menuTile
		// add resolves a tile's icon in two steps: a drawn glyph from
		// iconSVGFor (core routes only), else the generic drawn fallback
		// (ut-docs#1722) — never the bare "▪️" square ut-docs#1371 first
		// reported, and never an emoji (ut-docs#1845 retired the last of
		// those, iconFor, entirely).
		//
		// A plugin cannot declare its own icon here (Architect decision,
		// ut-docs#1722): the manifest's existing ManifestEntry.IconPath
		// field IS already read for rendering — but only for
		// BUTTON entries (data.ButtonEntryRow.IconPath, plugin_buttons.html's
		// <img src="/plugin-icons/{plugin}/{version}/{IconPath}">, served
		// through registerPluginIcons' path-traversal-guarded route,
		// plugin_icons.go). For MENU entries specifically it is unplumbed:
		// ListMenuEntries' SQL never selects icon_path, so data.MenuEntryRow
		// (what d.MenuSnapshot() surfaces, what this closure actually sees)
		// has no IconPath field to read at all — that is why wiring IconPath
		// into a menu tile is out of scope here, not because no safe serving
		// mechanism exists for it. Reusing the button-icon mechanism for menu
		// tiles is still real, separate scope (MenuEntryRow/ListMenuEntries
		// schema + query change, template wiring) — filed as ut-docs#1734,
		// which should cite the button path as prior art rather than starting
		// from nothing.
		add := func(href, label string) {
			svg := httpx.Icon(iconSVGFor[href])
			if svg == "" {
				svg = httpx.Icon(genericFallbackIcon)
			}
			tiles = append(tiles, menuTile{Href: href, Label: label, IconSVG: svg})
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
			// bluetoothdevices.title, not a separate nav.bluetooth_devices
			// key — it duplicated the exact same string across all locales
			// with nothing to keep the two in sync (ut-docs#1582 independent-
			// review finding), unlike every sibling entry above/below.
			add("/bluetooth-devices", "bluetoothdevices.title") // ut-docs#76
			add("/tables", "tables.title")
			add("/country-settings", "countrysettings.title")
			add("/translations", "translations.title")
			add("/report-issue", "issuereport.title")
			// §146a Abs. 4 AO fiscal register (ut-docs#665): the nav TILE is
			// Germany-only -- no other market has this obligation, so
			// surfacing it elsewhere would just be clutter. Checked as an
			// explicit country=="DE" comparison, not fiscal.RequiresHardGate
			// (ut-docs#1208 widened that to also cover Turkey's unrelated YN
			// ÖKC obligation -- reusing it here would wrongly surface this
			// §146a-specific tile for a TR shop too). As of ut-docs#1084,
			// country alone is no longer sufficient: the tile also requires
			// the German tax plugin to be installed and active
			// (fiscalRegisterPluginActive, fiscal_register_page.go) -- a shop
			// with country=DE and zero plugins installed must not see this
			// tile (the exact objection ut-docs#1026 raised). The page ROUTE
			// itself is deliberately NOT gated the same way -- see
			// fiscalRegisterPluginActive's own doc comment for why (a
			// docs-shots screenshot-harness constraint) -- so this is a
			// visibility-only fix, not a reachability one.
			if d.CurrentState().Country == "DE" && fiscalRegisterPluginActive(r.Context(), d) {
				add("/fiscal-register", "fiscalregister.title")
			}
			// Türkiye fiscal-device page (YN ÖKC, fiscal_device_page.go):
			// same shape as the German tile — country AND the Turkish
			// fiscal-device plugin installed and active, so a TR shop with
			// no plugin (shadow mode beside its existing register) never
			// sees a tile for a device the till isn't driving.
			// ut-docs#1750 (review F7): normalized like fiscalDeviceMarketActive,
			// which gates the actions this tile leads to — otherwise a shop
			// stored as "tr" gets no tile but fully working actions on the
			// direct URL.
			if strings.EqualFold(strings.TrimSpace(d.CurrentState().Country), "TR") && fiscalDevicePluginActive(r.Context(), d) {
				add("/fiscal-device", "fiscaldevice.title")
			}
		}
		// ut-docs#903: locations_page.go/registers_page.go moved off the
		// generic "settings" action onto their own dedicated
		// "stock_location_management" (migration 060) -- reusing "settings"
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
