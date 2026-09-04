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
	// Linked tills / sync status (ut-docs#1539). Two stacked
	// devices with a link between them — the rail's only "this shop has more
	// than one till" affordance. Added because the sync chip was the last rail
	// item still drawing itself with an emoji (⇅), which ut-docs#1423 removed
	// everywhere else precisely because emoji size differently per device font.
	"tills": `<rect x="2" y="3" width="9" height="7" rx="1"/><rect x="13" y="14" width="9" height="7" rx="1"/><path d="M6.5 10v3a2 2 0 0 0 2 2h4"/><path d="M17.5 14v-3a2 2 0 0 0-2-2h-4"/>`,
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

// IconNames lists the available rail icons, sorted — for tests and tooling.
func IconNames() []string {
	names := make([]string, 0, len(railIcons))
	for n := range railIcons {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
