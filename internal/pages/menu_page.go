package pages

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/uislot"
)

// The menu launcher: a full page of big touch tiles (a POS is a touch
// screen, so navigation is buttons, not small text links). Opened from the
// ☰ Menu button on the sale screen.
//
// Since ADR-0088 (ut-docs#1904) the tile list is DATA, not an imperative
// add(...) sequence: core declares its own tiles in uislot.CoreMenu, a
// `layout` plugin amends them (hide / reorder / re-label / re-group /
// re-icon), and this handler resolves both through ONE path —
// uislot.Resolve — so core's tiles are amendable by exactly the mechanism a
// plugin uses (Decision C's dogfooding requirement; if core did not use the
// registry it would rot). With no layout plugin installed Resolve hands the
// declared list straight back (Decision I), and the rendered tiles are
// pinned verbatim by TestMenuPage_GoldenZeroPluginTileOrder.

type menuTile struct {
	Href, Label string
	// IconSVG is a drawn glyph from the shared icon set
	// (internal/httpx/icons.go). Every core entry declares one — see
	// uislot.CoreMenu — so this is never empty for a rendered tile; a
	// plugin-contributed route with no mapping falls back to
	// genericFallbackIcon (menuIcon below).
	IconSVG template.HTML
	// GroupHeading, when set, is a locale key drawn as a heading before
	// this tile. Set only where consecutive tiles change group, so a till
	// with no grouped amendment renders exactly the markup it always did.
	GroupHeading string
}

// iconSVGFor maps a nav route that is NOT a declared core menu entry to a
// drawn icon name. This map used to cover every core route (started by
// ut-docs#1720 for Bluetooth, extended by ut-docs#1845 to retire the last
// emoji — see internal/httpx/icons.go for that history); those icons now
// live on the entries themselves in uislot.CoreMenu, one source per key
// (TestMenuPage_EveryCoreVisibleIfPredicateIsRegistered refuses a key in
// both). What remains here are routes that only ever reach the menu
// through d.Menu — a configured nav item or a test fixture — rather than
// the declared table.
//
// This map, like the table, only ever covers CORE routes — core cannot
// enumerate the routes a plugin brings with it, so a plugin-contributed
// tile can never get an entry here. See genericFallbackIcon below for that
// case (ut-docs#1722).
var iconSVGFor = map[string]string{
	// "/" is not a declared uislot.CoreMenu entry (ut-docs#1829's
	// TestBaseMenu_NoRedundantHomeTile / its ADR-0088 successor keep it
	// that way), so this key is unreachable through the tile-rendering
	// path today -- kept only so a future caller doesn't have to guess.
	// "shopping-cart", not "receipt": matches nav.html's own
	// sale-entry-point icon (ut-docs#1896).
	"/":          "shopping-cart",
	"/inventory": "package",
	"/catalog":   "tag",
	"/tills":     "monitor",
}

// genericFallbackIcon is the deliberate generic glyph a menu tile falls
// back to when neither its entry nor iconSVGFor names one — replacing the
// old "▪️" fallback (ut-docs#1722), which read as a rendering failure (the
// exact way it was reported, ut-docs#1371) rather than a deliberate icon.
//
// A plugin still cannot supply its own icon FILE here (ut-docs#1734 owns
// that: ListMenuEntries' SQL never selects icon_path, so data.MenuEntryRow
// has no IconPath to read; the button-icon path — plugin_icons.go's
// traversal-guarded route — is the prior art it should reuse). What a
// `layout` plugin CAN do since ADR-0088 Decision H is name an icon from
// core's own set for a core tile; an unknown name falls back to the core
// entry's icon, then to this one.
const genericFallbackIcon = "puzzle"

// menuVisibility evaluates a core entry's VisibleIf predicate, memoized per
// request: canPerform can hit role_permissions, and nine tiles share the
// "settings" gate, so it is answered once, not nine times — the same single
// evaluation the old add(...) sequence did.
type menuVisibility struct {
	d    *common.Deps
	r    *http.Request
	memo map[string]bool
}

// visible resolves a predicate NAME (uislot.Entry.VisibleIf). "" is always
// visible; an unregistered name fails closed — a tile is never shown on a
// gate nobody defined (TestMenuPage_EveryCoreVisibleIfPredicateIsRegistered
// keeps the table and this map in step).
func (v *menuVisibility) visible(name string) bool {
	if name == "" {
		return true
	}
	if got, ok := v.memo[name]; ok {
		return got
	}
	pred, ok := menuPredicates[name]
	if !ok {
		return false
	}
	got := pred(v)
	if v.memo == nil {
		v.memo = make(map[string]bool, len(menuPredicates))
	}
	v.memo[name] = got
	return got
}

// menuPredicates are the visibility predicates a core menu entry may name
// (ADR-0088 Decision C). A NAME, never an expression, and that is a
// security decision: these are real authorization and jurisdiction gates,
// and a plugin that could author one could author an authorization bypass.
// Plugins reference predicates; only core defines them, here. Populated in
// init because the fiscal predicates compose the memoized "settings" one
// through visible(), which reads this map — a package-level literal would
// be an initialization cycle.
var menuPredicates map[string]func(v *menuVisibility) bool

func init() {
	menuPredicates = map[string]func(v *menuVisibility) bool{
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
		"settings": func(v *menuVisibility) bool {
			return canPerform(v.d, v.r, "settings")
		},
		// ut-docs#903: locations_page.go/registers_page.go moved off the
		// generic "settings" action onto their own dedicated
		// "stock_location_management" (migration 060) -- reusing "settings"
		// meant a super_admin editing that one row in role_permissions
		// (runtime-editable, permission_settings_page.go) moved stock-
		// location/register administration in lockstep with every other
		// settings-gated admin surface, with no way to grant or withhold it
		// independently. This tile gate must track that same action or the
		// tile/page desync ut-docs#901 fixed once already reappears.
		"stock_location_management": func(v *menuVisibility) bool {
			return canPerform(v.d, v.r, "stock_location_management")
		},
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
		// visibility-only fix, not a reachability one. Nested under the
		// manager gate, as the tile always was.
		"fiscal_register_de": func(v *menuVisibility) bool {
			return v.visible("settings") && v.d.CurrentState().Country == "DE" && fiscalRegisterPluginActive(v.r.Context(), v.d)
		},
		// Türkiye fiscal-device page (YN ÖKC, fiscal_device_page.go):
		// same shape as the German tile — country AND the Turkish
		// fiscal-device plugin installed and active, so a TR shop with
		// no plugin (shadow mode beside its existing register) never
		// sees a tile for a device the till isn't driving.
		// ut-docs#1750 (review F7): normalized like fiscalDeviceMarketActive,
		// which gates the actions this tile leads to — otherwise a shop
		// stored as "tr" gets no tile but fully working actions on the
		// direct URL.
		"fiscal_device_tr": func(v *menuVisibility) bool {
			return v.visible("settings") && strings.EqualFold(strings.TrimSpace(v.d.CurrentState().Country), "TR") && fiscalDevicePluginActive(v.r.Context(), v.d)
		},
		// ut-docs#2008: gates the /admin tile itself -- visible only when the
		// viewer can see at least one of the six destinations /admin would
		// list, so the tile is never shown-then-refused (the same shape every
		// other gate here already follows).
		"administration": func(v *menuVisibility) bool {
			return len(visibleAdminEntries(v.d, v.r)) > 0
		},
	}
}

// visibleAdminEntries is the Group: "menu.group.administration" entries
// this viewer is actually allowed to see, filtered through the SAME
// menuVisibility.visible(e.VisibleIf) check used to build menu.html's own
// .Tiles -- shared by registerMenu (gating the /admin tile itself, via the
// "administration" predicate above) and registerAdmin (admin_page.go: both
// the page's own 403 gate and what it renders), so there is exactly one
// place that decides which of these a given request can reach.
//
// Resolves the SAME uislot.Resolve(menuSlotEntries(...), amendments) list
// registerMenu itself builds -- NOT raw uislot.CoreMenu (found in review,
// ut-docs#2008): reading CoreMenu directly silently broke three amendment
// cases a `layout` plugin already relies on --
//   - hiding one of the six became a no-op (registerMenu's own /menu skip
//     already dropped it from the flat grid, but the raw-CoreMenu read
//     here still listed it inside /admin);
//   - regrouping some OTHER core entry INTO "menu.group.administration"
//     (a supported, validated amendment -- ADR-0088 Decision F) vanished
//     from BOTH surfaces: registerMenu's skip keys on the RESOLVED Group
//     so it left .Tiles, but this function's raw-CoreMenu read never saw
//     the amended Group so it never entered the tree either;
//   - regrouping one of the six OUT of that group duplicated it: back on
//     the flat grid (resolved Group no longer matches registerMenu's skip)
//     AND still inside /admin (raw CoreMenu's original Group still matched
//     here).
//
// Reading the identical resolved list registerMenu builds is what keeps
// "which surface renders this entry" agreeing in both directions.
//
// Explicitly excludes Key=="/admin" regardless of its (amended) Group:
// /admin is uislot.ProtectedMenuKeys, and protected keys stay re-groupable
// by design (only hide/relabel/re-icon are refused) -- so a plugin
// amendment `{"key":"/admin","group":"menu.group.administration"}` is
// otherwise valid and would make /admin a member of its own tree. Since
// the "administration" VisibleIf predicate (menuPredicates above) computes
// itself as len(visibleAdminEntries(...))>0, /admin appearing in its own
// result would recompute that same predicate for itself on every call --
// unbounded recursion, a stack-overflow crash on every /menu and /admin
// request, not merely a display glitch. TestVisibleAdminEntries_ExcludesAdminItselfEvenIfRegroupedIntoItsOwnGroup
// pins this directly.
func visibleAdminEntries(d *common.Deps, r *http.Request) []uislot.Entry {
	resolved := uislot.Resolve(menuSlotEntries(d.MenuSnapshot()), d.MenuAmendmentsSnapshot())
	vis := &menuVisibility{d: d, r: r}
	var out []uislot.Entry
	for _, e := range resolved {
		// Both conditions guard the same recursion (found in review,
		// ut-docs#2008): Key=="/admin" is the identity check for the one
		// key this can happen to today; VisibleIf=="administration" is the
		// actual recursive trigger (vis.visible on THIS predicate is what
		// calls back into visibleAdminEntries) and stays the guard even
		// against a hypothetical future core entry that reused this
		// VisibleIf under some other Key — not plugin-reachable (Amendment
		// has no VisibleIf field), but a core-declared one wouldn't need to
		// be.
		if e.Key == "/admin" || e.VisibleIf == "administration" {
			continue
		}
		if e.Group != "menu.group.administration" {
			continue
		}
		if vis.visible(e.VisibleIf) {
			out = append(out, e)
		}
	}
	return out
}

// menuSlotEntries assembles the Menu slot for one render: the nav
// snapshot's items in their own order — a declared core key becomes its
// table entry, anything else (a plugin `page` entry, ADR-0037, or a
// configured route the table doesn't declare) becomes a page entry at
// uislot.PluginPagesOrder+i — followed by every core entry that is not
// InNav, in declared order. That is exactly the sequence the old add(...)
// calls produced, now as data uislot.Resolve can amend.
func menuSlotEntries(snapshot []common.MenuItem) []uislot.Entry {
	entries := make([]uislot.Entry, 0, len(snapshot)+len(uislot.CoreMenu))
	pages := 0
	for _, m := range snapshot {
		if e, ok := uislot.CoreMenuEntry(m.Href); ok {
			entries = append(entries, e)
			continue
		}
		entries = append(entries, uislot.Entry{
			Key:      m.Href,
			Href:     m.Href,
			LabelKey: m.Label,
			Icon:     iconSVGFor[m.Href],
			Order:    uislot.PluginPagesOrder + pages,
		})
		pages++
	}
	for _, e := range uislot.CoreMenu {
		if !e.InNav {
			entries = append(entries, e)
		}
	}
	return entries
}

// menuLabel is ADR-0088 Decision G: a re-labelled entry's label_key
// resolves through the normal translator; if it does not resolve for this
// locale (T hands the key back), the CORE label renders — a missing
// translation degrades to English, never to `layout.salon.services` on a
// merchant's screen. An unamended entry has no fallback and renders its
// own key through {{ T }} in the template as before.
func menuLabel(locale string, e uislot.Entry) string {
	if e.LabelFallback != "" && httpx.T(locale, e.LabelKey) == e.LabelKey {
		return e.LabelFallback
	}
	return e.LabelKey
}

// menuIcon is Decision H's fallback chain: the entry's icon name (amended
// or declared), then the core icon an amendment displaced, then
// genericFallbackIcon.
func menuIcon(e uislot.Entry) template.HTML {
	if svg := httpx.Icon(e.Icon); svg != "" {
		return svg
	}
	if svg := httpx.Icon(e.IconFallback); svg != "" {
		return svg
	}
	return httpx.Icon(genericFallbackIcon)
}

func registerMenu(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/menu", func(w http.ResponseWriter, r *http.Request) {
		// RequestLocale, not ResolveLocale: Render resolves (and sets the
		// ?lang= cookie) itself below; resolving twice would emit the
		// cookie twice.
		locale := httpx.RequestLocale(r)
		resolved := uislot.Resolve(menuSlotEntries(d.MenuSnapshot()), d.MenuAmendmentsSnapshot())
		vis := &menuVisibility{d: d, r: r}
		tiles := make([]menuTile, 0, len(resolved))
		prevGroup := ""
		for _, e := range resolved {
			// ut-docs#2008: the six Group: "menu.group.administration"
			// entries no longer get their own tiles (or #1959's group
			// heading) on this flat grid -- they render inside /admin
			// instead, reached through the single "/admin" tile above them
			// (also declared in uislot.CoreMenu, gated by its own
			// "administration" VisibleIf so it hides when this viewer can
			// see none of them -- see visibleAdminEntries).
			//
			// Key != "/admin" is deliberate and load-bearing (found in
			// second-round review, ut-docs#2008 -- see ADR-0088 Decision
			// E's 2026-09-10 amendment): /admin is itself Protected and
			// stays re-groupable by design, so a `layout` plugin amendment
			// {"key":"/admin","group":"menu.group.administration"} installs
			// cleanly under that permission. Without this exemption, /admin's
			// OWN resolved entry would then satisfy this same skip -- making
			// the tile that is the ONLY path to /fiscal-register and
			// /fiscal-device vanish from the flat grid via the ordinary
			// group-skip mechanism, with no hide amendment involved at all.
			// visibleAdminEntries carries the mirror-image guard (excluding
			// Key=="/admin" from its own tree) for the same reason from the
			// other direction. TestMenuPage_AdminTileRendersRegardlessOfGroupAmendment
			// pins this directly.
			if e.Group == "menu.group.administration" && e.Key != "/admin" {
				continue
			}
			if !vis.visible(e.VisibleIf) {
				continue
			}
			tile := menuTile{Href: e.Href, Label: menuLabel(locale, e), IconSVG: menuIcon(e)}
			if e.Group != "" && e.Group != prevGroup {
				tile.GroupHeading = e.Group
			}
			prevGroup = e.Group
			tiles = append(tiles, tile)
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
