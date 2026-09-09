package httpx

import (
	"fmt"
	"html/template"
	"sort"
)

// Rail icons as inline SVG (ut-docs#1423).
//
// The sale-screen nav rail used emoji glyphs (🧾 ☰ 📦 🛎️ 🐞 👥 🏷️ 🌐 👤 🔒)
// until 2026-09-02. Two rounds of per-glyph font-size tuning (#1332 second
// pass, #1348 — the `.ico-boost` class) were each verified on desktop
// Chromium and each still left 🔒 and 🐞 visibly smaller on the real
// tablet: an emoji is a glyph from whatever colour-emoji font the platform
// ships (Apple Color Emoji on macOS, Noto Color Emoji on Android and the
// Linux CI box), and every font pads every glyph differently inside its
// em-square, so a size bump tuned on one platform does not carry to the
// next. Vector icons at one box size render identically everywhere, which
// is the only thing that actually fixes it.
//
// Paths are from Lucide (https://lucide.dev, ISC licence), 24×24 grid,
// 2px stroke, currentColor — so the icon takes the rail's text colour and
// sizes purely by CSS (`.nav-toggle-ico svg` in app.css). Every entry MUST
// keep the same viewBox/stroke attributes: iconHTML wraps the bare paths in
// one shared <svg> element precisely so no icon can drift on its own again.
var railIcons = map[string]string{
	// Sale screen (nav.till)
	"receipt": `<path d="M4 2v20l2-1 2 1 2-1 2 1 2-1 2 1 2-1 2 1V2l-2 1-2-1-2 1-2-1-2 1-2-1-2 1Z"/><path d="M14 8H8"/><path d="M16 12H8"/><path d="M13 16H8"/>`,
	// Menu (nav.menu)
	"menu": `<line x1="4" x2="20" y1="12" y2="12"/><line x1="4" x2="20" y1="6" y2="6"/><line x1="4" x2="20" y1="18" y2="18"/>`,
	// Inventory (kiosk.inventory)
	"package": `<path d="m7.5 4.27 9 5.15"/><path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>`,
	// Orders (nav.orders) — concierge bell
	"bell": `<path d="M3 20a1 1 0 0 1-1-1v-1a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v1a1 1 0 0 1-1 1Z"/><path d="M20 16a8 8 0 1 0-16 0"/><path d="M12 4v4"/><path d="M10 4h4"/>`,
	// Help (help.open)
	"help": `<circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"/><path d="M12 17h.01"/>`,
	// Bug report (issuereport.nav_label)
	"bug": `<path d="m8 2 1.88 1.88"/><path d="M14.12 3.88 16 2"/><path d="M9 7.13v-1a3.003 3.003 0 1 1 6 0v1"/><path d="M12 20c-3.3 0-6-2.7-6-6v-3a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v3c0 3.3-2.7 6-6 6"/><path d="M12 20v-9"/><path d="M6.53 9C4.6 8.8 3 7.1 3 5"/><path d="M6 13H2"/><path d="M3 21c0-2.1 1.7-3.9 3.8-4"/><path d="M20.97 5c0 2.1-1.6 3.8-3.5 4"/><path d="M22 13h-4"/><path d="M17.2 17c2.1.1 3.8 1.9 3.8 4"/>`,
	// Users admin (users.title)
	"users": `<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/>`,
	// Promotions (promotions.title)
	"tag": `<path d="M12.586 2.586A2 2 0 0 0 11.172 2H4a2 2 0 0 0-2 2v7.172a2 2 0 0 0 .586 1.414l8.704 8.704a2.426 2.426 0 0 0 3.42 0l6.58-6.58a2.426 2.426 0 0 0 0-3.42z"/><circle cx="7.5" cy="7.5" r=".5" fill="currentColor"/>`,
	// Translations (translations.title)
	"globe": `<circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/>`,
	// Operator / change PIN (auth.change_pin)
	"user": `<path d="M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/>`,
	// Lock (auth.lock)
	"lock": `<rect width="18" height="11" x="3" y="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>`,
	// Sync chip / tills (sync.chip_tills_title, ut-docs#1539) — the one
	// rail item ut-docs#1423 missed, still a bare "⇅" emoji + tinted pill
	// until now. Two-way arrows, same visual idea as the emoji it replaces.
	"sync": `<path d="m21 16-4 4-4-4"/><path d="M17 20V4"/><path d="m3 8 4-4 4 4"/><path d="M7 4v16"/>`,
	// Fiscal chip (fiscal.chip_ok_title, ut-docs#1539) — was plain ✓/⚠ text
	// with no icon and no link at all. A shield mirrors the "signed and
	// verifiable" idea the ✓ glyph stood in for.
	"fiscal": `<path d="M20 13c0 5-3.5 7.5-7.35 8.95a1 1 0 0 1-.6-.01C8.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.79 17 5 19 5a1 1 0 0 1 1 1z"/><path d="m9 12 2 2 4-4"/>`,
	// Bluetooth devices (bluetoothdevices.title, ut-docs#1720) — the Menu
	// tile carried 📶 (ANTENNA BARS), the mobile-reception glyph, which
	// reads as signal strength rather than Bluetooth. Unlike every other
	// entry here this one could never have been an emoji: Unicode has no
	// Bluetooth codepoint at all, and the runic approximation (U+16D2)
	// depends on font coverage Android WebView does not reliably have — so
	// the standard mark is only ever a drawn glyph. First icon used outside
	// the nav rail (menu_page.go's tiles); see iconSVGFor there.
	"bluetooth": `<path d="m7 7 10 10-5 5V2l5 5L7 17"/>`,
	// Generic menu-tile fallback (ut-docs#1722) — replaces the "▪️" no-icon
	// square for any menu tile (core or plugin-contributed) with no mapped
	// icon. A plugin route can never get a specific icon here: core cannot
	// enumerate routes a plugin brings with it, so the map in menu_page.go
	// can only ever cover core routes. "▪️" read as a rendering failure
	// (reported as exactly that, ut-docs#1371) rather than a deliberate
	// generic icon; a puzzle piece reads as "extension/plugin", the actual
	// reason no specific icon exists, and is a drawn glyph so it carries no
	// per-platform emoji-metrics risk (ut-docs#1423). Lucide's "puzzle"
	// path, unmodified.
	"puzzle": `<path d="M15.39 4.39a1 1 0 0 0 1.68-.474 2.5 2.5 0 1 1 3.014 3.015 1 1 0 0 0-.474 1.68l1.683 1.682a2.414 2.414 0 0 1 0 3.414L19.61 15.39a1 1 0 0 1-1.68-.474 2.5 2.5 0 1 0-3.014 3.015 1 1 0 0 1 .474 1.68l-1.683 1.682a2.414 2.414 0 0 1-3.414 0L8.61 19.61a1 1 0 0 0-1.68.474 2.5 2.5 0 1 1-3.014-3.015 1 1 0 0 0 .474-1.68l-1.683-1.682a2.414 2.414 0 0 1 0-3.414L4.39 8.61a1 1 0 0 1 1.68.474 2.5 2.5 0 1 0 3.014-3.015 1 1 0 0 1-.474-1.68l1.683-1.682a2.414 2.414 0 0 1 3.414 0z"/>`,
	// Scissors — a services/salon tile (plugins/layout-salon re-icons
	// /items with it, ADR-0088 Decision H: a `layout` plugin names an icon
	// from THIS set, never a file). Lucide's "scissors" path, unmodified.
	"scissors": `<circle cx="6" cy="6" r="3"/><path d="M8.12 8.12 12 12"/><path d="M20 4 8.12 15.88"/><circle cx="6" cy="18" r="3"/><path d="M14.8 14.8 20 20"/>`,
	// The remaining entries below (ut-docs#1845) replace the Menu launcher's
	// per-tile emoji (menu_page.go's old `iconFor`) with the rest of this
	// same Lucide set, so every core menu tile — not just Bluetooth — now
	// draws instead of relying on the platform's colour-emoji font. Paths
	// are unmodified from https://lucide.dev (ISC licence, no attribution
	// required in-UI — same clearance already established for the entries
	// above).
	"palette":      `<path d="M12 22a1 1 0 0 1 0-20 10 9 0 0 1 10 9 5 5 0 0 1-5 5h-2.25a1.75 1.75 0 0 0-1.4 2.8l.3.4a1.75 1.75 0 0 1-1.4 2.8z"/><circle cx="13.5" cy="6.5" r=".5" fill="currentColor"/><circle cx="17.5" cy="10.5" r=".5" fill="currentColor"/><circle cx="6.5" cy="12.5" r=".5" fill="currentColor"/><circle cx="8.5" cy="7.5" r=".5" fill="currentColor"/>`,
	"clock":        `<circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/>`,
	"book-open":    `<path d="M12 5v16"/><path d="M20.001 19A2 2 0 0022 17V5a2 2 0 00-1.999-2L16 3.002A5 5 0 0012 5a5 5 0 00-4-2H4a2 2 0 00-2 2v12a2 2 0 001.999 2H8a5 5 0 014 2 5 5 0 014-2z"/>`,
	"chart-column": `<path d="M3 3v16a2 2 0 0 0 2 2h16"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/>`,
	"settings":     `<path d="M9.671 4.136a2.34 2.34 0 0 1 4.659 0 2.34 2.34 0 0 0 3.319 1.915 2.34 2.34 0 0 1 2.33 4.033 2.34 2.34 0 0 0 0 3.831 2.34 2.34 0 0 1-2.33 4.033 2.34 2.34 0 0 0-3.319 1.915 2.34 2.34 0 0 1-4.659 0 2.34 2.34 0 0 0-3.32-1.915 2.34 2.34 0 0 1-2.33-4.033 2.34 2.34 0 0 0 0-3.831A2.34 2.34 0 0 1 6.35 6.051a2.34 2.34 0 0 0 3.319-1.915"/><circle cx="12" cy="12" r="3"/>`,
	"map-pin":      `<path d="M20 10c0 4.993-5.539 10.193-7.399 11.799a1 1 0 0 1-1.202 0C9.539 20.193 4 14.993 4 10a8 8 0 0 1 16 0"/><circle cx="12" cy="10" r="3"/>`,
	"calculator":   `<rect width="16" height="20" x="4" y="2" rx="2"/><line x1="8" x2="16" y1="6" y2="6"/><line x1="16" x2="16" y1="14" y2="18"/><path d="M16 10h.01"/><path d="M12 10h.01"/><path d="M8 10h.01"/><path d="M12 14h.01"/><path d="M8 14h.01"/><path d="M12 18h.01"/><path d="M8 18h.01"/>`,
	"chef-hat":     `<path d="M17 21a1 1 0 0 0 1-1v-5.35c0-.457.316-.844.727-1.041a4 4 0 0 0-2.134-7.589 5 5 0 0 0-9.186 0 4 4 0 0 0-2.134 7.588c.411.198.727.585.727 1.041V20a1 1 0 0 0 1 1Z"/><path d="M6 17h12"/>`,
	// Tables & floor plan (tables.title) — NOT Lucide's "table" (a data-grid
	// icon: rows/columns, meant for spreadsheets). Independent review of
	// ut-docs#1845 caught that mismatch before merge: /tables is dining
	// tables and seating (web/help/en/tables.md), the exact "technically
	// drawn but reads wrong" failure ut-docs#1720 already fixed once for
	// Bluetooth. Crossed fork+knife is the icon this ecosystem's own
	// competitor comparison (SumUp, Toast) uses for a restaurant tables/
	// floor-plan surface.
	"utensils-crossed": `<path d="m16 2-2.3 2.3a3 3 0 0 0 0 4.2l1.8 1.8a3 3 0 0 0 4.2 0L22 8"/><path d="M15 15 3.3 3.3a4.2 4.2 0 0 0 0 6l7.3 7.3c.7.7 2 .7 2.8 0L15 15Zm0 0 7 7"/><path d="m2.1 21.8 6.4-6.3"/><path d="m19 5-7 7"/>`,
	"flag":             `<path d="M4 22V4a1 1 0 0 1 .4-.8A6 6 0 0 1 8 2c3 0 5 2 7.333 2q2 0 3.067-.8A1 1 0 0 1 20 4v10a1 1 0 0 1-.4.8A6 6 0 0 1 16 16c-3 0-5-2-8-2a6 6 0 0 0-4 1.528"/>`,
	"monitor":          `<rect width="20" height="14" x="2" y="3" rx="2"/><line x1="8" x2="16" y1="21" y2="21"/><line x1="12" x2="12" y1="17" y2="21"/>`,
	"clipboard-list":   `<rect width="8" height="4" x="8" y="2" rx="1" ry="1"/><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/><path d="M12 11h4"/><path d="M12 16h4"/><path d="M8 11h.01"/><path d="M8 16h.01"/>`,
	// Pfand-refund tile (menu.html) and the tender quick-pay row (index.html
	// phone-fallback) — see those templates' own comments.
	"recycle":       `<path d="M7 19H4.815a1.83 1.83 0 0 1-1.57-.881 1.785 1.785 0 0 1-.004-1.784L7.196 9.5"/><path d="M11 19h8.203a1.83 1.83 0 0 0 1.556-.89 1.784 1.784 0 0 0 0-1.775l-1.226-2.12"/><path d="m14 16-3 3 3 3"/><path d="M8.293 13.596 7.196 9.5 3.1 10.598"/><path d="m9.344 5.811 1.093-1.892A1.83 1.83 0 0 1 11.985 3a1.784 1.784 0 0 1 1.546.888l3.943 6.843"/><path d="m13.378 9.633 4.096 1.098 1.097-4.096"/>`,
	"shopping-cart": `<path d="m2.05 2.05 1.099-.028a1 1 0 0 1 1.008.815l2.69 14.347A1 1 0 0 0 7.83 18H18"/><path d="M4.563 5h16.435a1 1 0 0 1 .981 1.204l-1.026 6.226A2 2 0 0 1 18.962 14H6.25"/><circle cx="18" cy="20" r="2"/><circle cx="8" cy="20" r="2"/>`,
}

// iconSVGOpen is the one shared wrapper every rail icon renders inside.
// Decorative: the accessible name lives in the sibling .nav-toggle-label
// (visually hidden in the rail, visible in the ≤480px top bar), exactly as
// it did with the emoji, so aria-hidden here is correct and unchanged.
const iconSVGOpen = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false" data-icon="%s">`

// iconHTML renders {{ icon "lock" }}. An unknown name renders nothing
// rather than a broken glyph; TestRailIconsReferencedByTemplatesExist keeps
// the templates honest so that path is never taken in a shipped build.
func iconHTML(name string) template.HTML {
	body, ok := railIcons[name]
	if !ok {
		return ""
	}
	return template.HTML(fmt.Sprintf(iconSVGOpen, template.HTMLEscapeString(name)) + body + `</svg>`) //nolint:gosec // body is a compile-time constant from railIcons; name is escaped
}

// Icon is iconHTML for Go code that builds a view model directly instead of
// naming its icon in a template (menu_page.go's tiles, ut-docs#1720). Same
// contract, deliberately: an unknown — or empty — name renders nothing, so a
// caller can look a name up in a sparse map and pass the miss straight
// through. Because such a call site is invisible to icons_test.go's
// template scanner, each one owns a test that its names resolve.
func Icon(name string) template.HTML { return iconHTML(name) }

// IconNames lists the available rail icons, sorted — for tests and tooling.
func IconNames() []string {
	names := make([]string, 0, len(railIcons))
	for n := range railIcons {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
