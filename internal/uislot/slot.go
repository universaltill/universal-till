// Package uislot is the declarative UI slot registry (ADR-0088): which
// destinations a slot lists, their order, grouping, labels and icons are
// DATA that core populates with its own defaults and a `layout` plugin may
// then amend — hide, reorder, re-label, re-group, re-icon.
//
// This package deliberately depends on nothing else in the product so that
// both sides of the mechanism can import it: internal/plugins validates an
// amendment document at install time (Decisions E and F), internal/pages
// resolves core defaults + amendments at render time (Decision C). It is a
// sibling of ADR-0041's behavioural extension points, not a part of them —
// an amendment is persisted manifest data read from rows, never a WASM
// dispatch on the render path (ADR-0088 Decision A).
//
// The first slot was the Menu launcher (MenuSlot); the second is the /items
// section list (ItemsSlot, ut-docs#1911, generalizing what ut-docs#1897
// shipped as a hardcoded list); the third is the nav rail (RailSlot,
// ut-docs#1912, generalizing what #1332 shipped as hardcoded markup); the
// fourth is Settings groupings (SettingsSlot, ut-docs#1913) — all four
// attach the same way: a new *Slot name, a new declared core table, and a
// slotSpec passed to the shared parse/validate path below — no redesign of
// Entry/Amendment/Resolve/FindConflict, which were already slot-agnostic.
// No further slot candidate is identified today: ADR-0091 considered the
// catalog list's own presentation (table vs. card grid) as a possible
// fifth entry and declined it — that choice is one view/one axis/one value
// at a time, not a list of keyed, orderable destinations like the four
// above, so it stays a core-only default reachable through the theme seam
// instead.
package uislot

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// MenuSlot is the slot name for the Menu launcher (internal/pages/menu_page.go).
const MenuSlot = "menu"

// ItemsSlot is the slot name for the /items left-rail section list
// (internal/pages/itemsnav, ut-docs#1911).
const ItemsSlot = "items"

// RailSlot is the slot name for the nav rail (web/ui/partials/nav.html's
// .nav-primary block, ut-docs#1912) — rendered on every page, not just the
// sale screen.
const RailSlot = "rail"

// SettingsSlot is the slot name for the /settings page's own section list
// (web/ui/pages/settings.html's #settings-tree sidebar, ut-docs#1913 —
// ADR-0088's "Scope of the first implementation" named this as the third
// named follow-up slot after Items and Rail). Unlike those two, Settings is
// a single scrolled page rather than separate routes per row, so this slot
// resolves the SIDEBAR's order/label/grouping only — the on-page card
// content keeps its own declared DOM order and heading text unchanged. See
// CoreSettings' own doc comment for why that split is deliberate for this
// first slice, not an oversight.
const SettingsSlot = "settings"

// PluginPagesOrder is the Order at which plugin `page` entries (ADR-0037)
// are placed when the menu slot is assembled: after every core InNav entry
// and before /help — exactly where they rendered before ADR-0088. The i-th
// plugin page gets PluginPagesOrder+i, so a `layout` reorder that wants to
// land before or after them has a documented number to compare against.
const PluginPagesOrder = 1000

// Entry is one declared destination in a slot.
type Entry struct {
	// Key is the stable identity an amendment names. For core entries it
	// equals Href — ADR-0088 names the protected set by path.
	Key string
	// Href is the destination the tile links to.
	Href string
	// LabelKey is a locale key resolved through {{ T }}; never a literal.
	// (A plugin `page` entry's label may be plain text — ADR-0037 leaves
	// that as-is; T passes an unknown key through unchanged.)
	LabelKey string
	// Icon is a NAME in core's built-in icon set (internal/httpx/icons.go),
	// never a file (Decision H). Empty for a plugin page with no mapping;
	// the renderer applies its generic fallback.
	Icon string
	// Order sorts the slot; the core table is declared ascending so the
	// zero-amendment path never sorts (Decision I).
	Order int
	// Group is an optional locale key; the renderer draws a heading when
	// consecutive entries change group. Resolve gathers same-group entries
	// so they ARE consecutive (groupTogether) — without that, a group whose
	// members land apart in Order rendered its heading twice.
	Group string
	// VisibleIf is the NAME of a predicate registered in core (never an
	// expression — Decision C's security decision). "" means always.
	VisibleIf string
	// InNav marks the core entries that also form the compact nav's item
	// list (pages/init.go's baseMenu); the Menu page renders those in the
	// nav snapshot's order and every other core entry after them. Menu-slot
	// only; zero value for every other slot.
	InNav bool
	// SubtitleKey is a locale key for a second descriptive line — the
	// Items slot's rows carry one (itemsnav.Section's own SubtitleKey);
	// the Menu slot never sets it. Not amendable: ADR-0088's amendment
	// schema (Amendment, below) has no subtitle field, so this always
	// carries the CORE declaration's value straight through Resolve.
	SubtitleKey string

	// LabelFallback / IconFallback are set by Resolve when an amendment
	// overrides LabelKey / Icon, so the renderer can degrade to the core
	// label (Decision G: never the raw key) and the core icon (Decision H).
	// Never declared, only produced.
	LabelFallback string
	IconFallback  string
}

// Amendment is one plugin's amendment of one slot key.
type Amendment struct {
	PluginID string
	// Slot is the slot this amendment targets (MenuSlot, ItemsSlot, ...) —
	// stamped by the parser (parseSlotAmendments) from the config
	// document's own "slot" field, never set by a caller directly. Lets a
	// caller holding a flat pool of every active plugin's amendments (e.g.
	// plugins.Manager.LayoutAmendments) filter to the one slot it renders,
	// and lets FindConflict below tell "two plugins restructure the same
	// /catalog key in the Items slot" apart from an unrelated same-string
	// key in a different slot.
	Slot     string
	Key      string
	Hide     bool
	Order    *int
	LabelKey string
	Group    string
	Icon     string
}

// Restructures reports whether the amendment reorders / re-labels /
// re-groups / re-icons — the kinds that conflict between plugins under
// Decision F. Hide never restructures (it is idempotent).
func (a Amendment) Restructures() bool {
	return a.Order != nil || a.LabelKey != "" || a.Group != "" || a.Icon != ""
}

// ProtectedMenuKeys may never be hidden by a plugin (ADR-0088 Decision E):
// §146a Abs. 4 AO's fiscal register, Türkiye's YN ÖKC device page, the
// journal, settings, plugins, help and issue reporting. Reorder / re-label
// / re-group / re-icon remain allowed on them — they keep the destination
// visible, which is the whole point of protecting it.
//
// ut-docs#2008: "/admin" joined this list because it is now the ONLY
// launcher path to /fiscal-register and /fiscal-device (menu_page.go's
// registerMenu unconditionally drops both from the flat /menu grid — see
// its own Group=="menu.group.administration" skip). Before this card, a
// plugin hiding one admin-group tile still left the other five, including
// both statutory pages, individually reachable; now hiding (or relabelling
// to something unfindable) the single /admin tile would make BOTH statutory
// pages unreachable from any UI surface at all — exactly what Decision E
// was tightened to prevent (see its own doc comment above). Note this does
// NOT stop /admin being re-grouped — protected keys stay re-groupable by
// design — see visibleAdminEntries' own explicit Key=="/admin" guard
// (menu_page.go) for why that specific amendment needs its own defence.
var ProtectedMenuKeys = []string{
	"/admin",
	"/fiscal-register",
	"/fiscal-device",
	"/journal",
	"/settings",
	"/plugins",
	"/help",
	"/report-issue",
}

// IsProtectedMenuKey reports whether key is in ProtectedMenuKeys.
func IsProtectedMenuKey(key string) bool {
	for _, p := range ProtectedMenuKeys {
		if p == key {
			return true
		}
	}
	return false
}

// CoreMenu is core's own declared Menu launcher (ADR-0088 Decision C): the
// tile list internal/pages/menu_page.go used to build imperatively, one
// add(...) per destination, now as data that resolves through exactly the
// same path a plugin amendment does. Declared in ascending Order, so with
// no amendments installed the renderer walks it as-is (Decision I).
//
// Order bands (documented so a `layout` author knows what a reorder value
// lands against): 100–800 the InNav entries, PluginPagesOrder (1000)+ the
// plugin `page` tiles, 1900 open orders, 2000 help, 2100–2550 the
// manager-gated destinations with no Group (2550 is /admin, see below),
// 2600–3100 the same gated band but still grouped under
// "menu.group.administration" (ut-docs#1959) — set-once/onboarding
// destinations (country, translations, the two statutory fiscal-device
// pages, stock locations/registers). Kept contiguous deliberately: Resolve
// only re-runs groupTogether when an AMENDMENT regroups something (Decision
// I), so with no plugin installed the renderer walks CoreMenu as declared —
// an ungrouped entry landing between two same-Group entries here would
// split one heading into two identical ones.
//
// ut-docs#2008: the six Group: "menu.group.administration" entries no
// longer render as tiles on the flat /menu grid at all (menu_page.go's
// registerMenu skips any entry whose Group is this value when building
// .Tiles) — #1959's group-heading-on-a-flat-grid is replaced by a single
// gated "/admin" tile (Order 2550 — TestCoreMenu_IsWellFormed requires this
// table strictly ascending, so it can't also be 2600; still the slot the
// group heading itself used to occupy, immediately ahead of the grouped
// entries it now stands in for) that opens /admin, a dedicated tree page
// grouping those same six destinations into domain clusters
// (internal/pages/admin_page.go). The
// six entries themselves, and their Group field, are UNCHANGED here — the
// same Group value is what internal/pages.visibleAdminEntries filters on
// to build both the tile's own "administration" VisibleIf predicate and
// the tree page's content, so removing Group from them would break both.
// /admin itself deliberately has NO Group — giving it
// "menu.group.administration" would make menu_page.go's own skip filter
// hide the tile that is supposed to replace the heading.
//
// VisibleIf names are defined in menu_page.go's menuPredicates and pinned
// by TestMenuPage_EveryCoreVisibleIfPredicateIsRegistered; the WHY behind
// each gate's exact shape (ut-docs#866, #903, #1084, #1208, #1750, #665)
// is recorded on those predicates, not here.
var CoreMenu = []Entry{
	{Key: "/designer", Href: "/designer", LabelKey: "nav.designer", Icon: "palette", Order: 100, InNav: true},
	{Key: "/shifts", Href: "/shifts", LabelKey: "nav.shifts", Icon: "clock", Order: 200, InNav: true},
	{Key: "/journal", Href: "/journal", LabelKey: "nav.journal", Icon: "book-open", Order: 300, InNav: true},
	{Key: "/orders", Href: "/orders", LabelKey: "nav.orders", Icon: "bell", Order: 400, InNav: true},
	{Key: "/reports", Href: "/reports", LabelKey: "nav.reports", Icon: "chart-column", Order: 500, InNav: true},
	{Key: "/settings", Href: "/settings", LabelKey: "nav.settings", Icon: "settings", Order: 600, InNav: true},
	{Key: "/plugins", Href: "/plugins", LabelKey: "nav.plugins", Icon: "puzzle", Order: 700, InNav: true},
	// ut-docs#1897: the "Items" tile that replaced the flat "Catalog" one.
	{Key: "/items", Href: "/items", LabelKey: "nav.items", Icon: "tag", Order: 800, InNav: true},

	// ut-docs#1918: parked baskets -- a cashier surface (the sale screen's
	// held strip as a full list), so it sits ungated, not in baseMenu (which
	// also feeds the top nav rail, where a second orders entry next to the
	// bell would just be clutter). Not part of d.MenuSnapshot() either, so
	// it must be declared here to render at all.
	{Key: "/open-orders", Href: "/open-orders", LabelKey: "open_orders.title", Icon: "monitor", Order: 1900},
	{Key: "/help", Href: "/help", LabelKey: "nav.help", Icon: "help", Order: 2000},
	// ut-docs#582: the staff "pay at counter" board (kiosk.payment_mode
	// "counter") — no VisibleIf, same as /orders above: any operator needs
	// to see which orders are waiting to be called out/collected, not just
	// a manager. Order 2050, not InNav: visible to every operator on the
	// /menu grid (like /help just above), but not promoted into the
	// space-constrained top nav rail (see nav.html's own "no spare budget"
	// comment) — same non-InNav placement as every other non-rail tile
	// below.
	{Key: "/kiosk-counter-orders", Href: "/kiosk-counter-orders", LabelKey: "nav.kiosk_counter_orders", Icon: "check", Order: 2050},
	{Key: "/users", Href: "/users", LabelKey: "users.title", Icon: "users", Order: 2100, VisibleIf: "settings"},
	{Key: "/kitchen-stations", Href: "/kitchen-stations", LabelKey: "kitchenstations.title", Icon: "chef-hat", Order: 2200, VisibleIf: "settings"},
	// bluetoothdevices.title, not a separate nav.bluetooth_devices key — it
	// duplicated the exact same string across all locales with nothing to
	// keep the two in sync (ut-docs#1582 independent-review finding).
	{Key: "/bluetooth-devices", Href: "/bluetooth-devices", LabelKey: "bluetoothdevices.title", Icon: "bluetooth", Order: 2300, VisibleIf: "settings"}, // ut-docs#76
	{Key: "/tables", Href: "/tables", LabelKey: "tables.title", Icon: "utensils-crossed", Order: 2400, VisibleIf: "settings"},
	// ut-docs#1959: moved ahead of the Administration group below (was
	// Order 2700, between /translations and /fiscal-register) so the
	// group's members stay contiguous in this declaration — see the
	// package doc comment above for why that matters.
	{Key: "/report-issue", Href: "/report-issue", LabelKey: "issuereport.title", Icon: "bug", Order: 2500, VisibleIf: "settings"},
	// ut-docs#2008: the single gated "Administration" tile that replaces
	// #1959's group heading below — opens /admin, a tree page listing
	// whichever of the six grouped destinations below the viewer can see.
	// Reuses "menu.group.administration" as its own label (already
	// translated in all four locales, so this needs no new key) — same
	// text a merchant already associates with this set of destinations.
	// "administration" (menu_page.go's menuPredicates) is true only when
	// visibleAdminEntries(d, r) is non-empty, so the tile itself disappears
	// rather than opening onto a page with nothing on it.
	//
	// Order 2550, not 2600: TestCoreMenu_IsWellFormed requires this table
	// declared in STRICTLY ascending Order (ties included — the zero-
	// amendment path never sorts, Decision I, so Order here IS the render
	// order), and 2600 is already /country-settings' own value below. 2550
	// is still "the slot the removed group heading vacated" in every way
	// that matters: strictly between /report-issue (2500) and the group's
	// own band (2600+), i.e. immediately ahead of it.
	{Key: "/admin", Href: "/admin", LabelKey: "menu.group.administration", Icon: "lock", Order: 2550, VisibleIf: "administration"},
	// ut-docs#1959: "Administration" — setup/onboarding destinations a
	// merchant touches once and rarely returns to, grouped per the product
	// owner's 2026-09-10 feedback. /users, /kitchen-stations and
	// /bluetooth-devices were considered and deliberately left OUT: Users
	// is day-to-day staff management (the product owner's own call),
	// Kitchen Stations and Bluetooth Devices are reconfigured as often as
	// the floor/hardware changes, not one-time setup like the entries
	// below.
	{Key: "/country-settings", Href: "/country-settings", LabelKey: "countrysettings.title", Icon: "flag", Order: 2600, VisibleIf: "settings", Group: "menu.group.administration"},
	{Key: "/translations", Href: "/translations", LabelKey: "translations.title", Icon: "globe", Order: 2700, VisibleIf: "settings", Group: "menu.group.administration"},
	// Fiscal register (DE) and Fiscal device (TR) are the two statutory
	// hardware/registration pages: connected once during setup, same as
	// Locations/Registers below — structurally the twin of the entry the
	// product owner named explicitly, so grouped alongside it.
	{Key: "/fiscal-register", Href: "/fiscal-register", LabelKey: "fiscalregister.title", Icon: "clipboard-list", Order: 2800, VisibleIf: "fiscal_register_de", Group: "menu.group.administration"},
	{Key: "/fiscal-device", Href: "/fiscal-device", LabelKey: "fiscaldevice.title", Icon: "receipt", Order: 2900, VisibleIf: "fiscal_device_tr", Group: "menu.group.administration"},
	{Key: "/locations", Href: "/locations", LabelKey: "locations.title", Icon: "map-pin", Order: 3000, VisibleIf: "stock_location_management", Group: "menu.group.administration"},
	{Key: "/registers", Href: "/registers", LabelKey: "registers.title", Icon: "calculator", Order: 3100, VisibleIf: "stock_location_management", Group: "menu.group.administration"},
}

var coreMenuIndex = func() map[string]int {
	m := make(map[string]int, len(CoreMenu))
	for i, e := range CoreMenu {
		m[e.Key] = i
	}
	return m
}()

// CoreMenuEntry returns the declared core entry for key.
func CoreMenuEntry(key string) (Entry, bool) {
	i, ok := coreMenuIndex[key]
	if !ok {
		return Entry{}, false
	}
	return CoreMenu[i], true
}

// ProtectedItemsKeys is the Items slot's ADR-0088 Decision E set — deliberately
// empty. None of the /items rail's five destinations (Library/Catalog,
// Categories, Inventory, Modifiers, Option sets) is a legal/fiscal/safety
// surface the way /fiscal-register or /journal is on the Menu slot; a
// `layout` plugin may hide, reorder, re-label, re-group or re-icon any of
// them.
var ProtectedItemsKeys = []string{}

// IsProtectedItemsKey reports whether key is in ProtectedItemsKeys — always
// false today, kept as a named function (rather than inlining "false") so a
// future protected Items destination is one slice entry away, matching
// IsProtectedMenuKey's shape.
func IsProtectedItemsKey(key string) bool {
	return IsProtectedKey(ProtectedItemsKeys, key)
}

// IsProtectedKey is IsProtectedMenuKey's shared implementation, generalized
// to any slot's protected-key slice (ut-docs#1911).
func IsProtectedKey(protected []string, key string) bool {
	for _, p := range protected {
		if p == key {
			return true
		}
	}
	return false
}

// ProtectedRailKeys is the rail slot's ADR-0088 Decision J set — a second
// protected-key category, structural rather than legal/fiscal. "/" (Sell)
// and "/menu" (Menu) are the rail's only two entries with no equivalent
// tile anywhere in CoreMenu: hiding or relabelling either is a dead end
// (no route back to the sale screen, or no route to anything not on the
// rail itself), not merely an inconvenience the way losing /inventory or
// /orders from the rail is (both stay independently reachable — /orders is
// already a CoreMenu tile, /inventory via the Items slot's own row).
var ProtectedRailKeys = []string{"/", "/menu"}

// IsProtectedRailKey reports whether key is in ProtectedRailKeys.
func IsProtectedRailKey(key string) bool {
	return IsProtectedKey(ProtectedRailKeys, key)
}

// CoreItems is the /items left-rail's declared section list (ADR-0088
// Decision C, generalized by ut-docs#1911 from the hardcoded slice
// internal/pages/itemsnav.Sections used to be): core's own defaults,
// resolved through the same uislot.Resolve path a `layout` plugin's
// amendment uses. Order is declared ascending (100 apart, matching
// CoreMenu's own spacing) so the zero-amendment path never sorts (Decision
// I). Label/subtitle keys and hrefs are unchanged from itemsnav.Sections —
// this is a relocation of the same five rows, not a redesign of them.
var CoreItems = []Entry{
	{Key: "/catalog", Href: "/catalog", LabelKey: "nav.catalog", SubtitleKey: "items.library.subtitle", Order: 100},
	{Key: "/categories", Href: "/categories", LabelKey: "items.categories.name", SubtitleKey: "items.categories.subtitle", Order: 200},
	{Key: "/inventory", Href: "/inventory", LabelKey: "items.inventory.name", SubtitleKey: "items.inventory.subtitle", Order: 300},
	{Key: "/modifiers", Href: "/modifiers", LabelKey: "items.modifiers.name", SubtitleKey: "items.modifiers.subtitle", Order: 400},
	{Key: "/catalog/option-sets", Href: "/catalog/option-sets", LabelKey: "items.option_sets.name", SubtitleKey: "items.option_sets.subtitle", Order: 500},
}

var coreItemsIndex = func() map[string]int {
	m := make(map[string]int, len(CoreItems))
	for i, e := range CoreItems {
		m[e.Key] = i
	}
	return m
}()

// CoreItemsEntry returns the declared core Items-slot entry for key —
// CoreMenuEntry's twin for ItemsSlot.
func CoreItemsEntry(key string) (Entry, bool) {
	i, ok := coreItemsIndex[key]
	if !ok {
		return Entry{}, false
	}
	return CoreItems[i], true
}

// CoreRail is the nav rail's declared entry list (ADR-0088 Decision C,
// generalized by ut-docs#1912 from the hardcoded <a> tags
// web/ui/partials/nav.html's .nav-primary block used to be): the four
// entries in their existing DOM order — Sell, Menu, Inventory, Orders.
// Declared ascending Order so the zero-amendment path never sorts
// (Decision I) — this table renders on EVERY page, more than CoreMenu or
// CoreItems combined, so the zero-alloc guarantee matters more here.
// No Group/SubtitleKey/VisibleIf/InNav: nav.html draws no heading, no
// subtitle line, and every entry is unconditionally visible.
var CoreRail = []Entry{
	{Key: "/", Href: "/", LabelKey: "nav.till", Icon: "shopping-cart", Order: 100},
	{Key: "/menu", Href: "/menu", LabelKey: "nav.menu", Icon: "menu", Order: 200},
	{Key: "/inventory", Href: "/inventory", LabelKey: "kiosk.inventory", Icon: "package", Order: 300},
	{Key: "/orders", Href: "/orders", LabelKey: "nav.orders", Icon: "bell", Order: 400},
}

var coreRailIndex = func() map[string]int {
	m := make(map[string]int, len(CoreRail))
	for i, e := range CoreRail {
		m[e.Key] = i
	}
	return m
}()

// CoreRailEntry returns the declared core rail entry for key —
// CoreMenuEntry's twin for RailSlot.
func CoreRailEntry(key string) (Entry, bool) {
	i, ok := coreRailIndex[key]
	if !ok {
		return Entry{}, false
	}
	return CoreRail[i], true
}

// ProtectedSettingsKeys is the Settings slot's ADR-0088 Decision E set
// (ut-docs#1913). Unlike the Menu slot's statutory-page reasoning, none of
// these is a legal filing surface — they are protected because a plugin
// that could disguise them via re-label would strand a merchant exactly
// the way a disguised /fiscal-register tile would (ADR-0088 Decision E's
// independent-review tightening: the label IS the identity a merchant
// recognises a surface by):
//   - "settings-data": GDPR customer erase, demo-catalogue reset and
//     backup restore — destructive, compliance-adjacent operations
//     (internal/pages/settings_page.go's own doc comments on this card).
//   - "settings-retention": the ADR-0040 report-retention mode and the
//     archive export a shop's accountant relies on.
//   - "settings-all": the unbounded, un-field-scoped raw settings
//     key/value browser — this card's own template comment already flags
//     it as a uniquely powerful, manager-only escape hatch.
//
// Hide is refused for this ENTIRE slot regardless of protection (see
// settingsSpec below) — a plugin cannot hide any settings section yet,
// protected or not — so this list currently only gates RE-LABEL (Group and
// Order stay permitted on a protected key, same as Menu: they move a
// section without disguising it).
var ProtectedSettingsKeys = []string{"settings-data", "settings-retention", "settings-all"}

// IsProtectedSettingsKey reports whether key is in ProtectedSettingsKeys.
func IsProtectedSettingsKey(key string) bool {
	return IsProtectedKey(ProtectedSettingsKeys, key)
}

// CoreSettings is the /settings page's declared section list (ADR-0088
// Decision C, ut-docs#1913): the 25 `.card` sections
// web/ui/pages/settings.html always declared as static, hand-ordered
// markup, now ALSO available as data so a `layout` plugin can amend the
// SIDEBAR's presentation of them. Key is the card's own `id` attribute
// (settings_two_pane_test.go already pins every one of these as stable);
// LabelKey is the exact locale key that card's own `<h2>` already renders,
// so a zero-amendment resolution reproduces the sidebar's pre-#1913 text
// verbatim. Declared in the SAME order the cards appear in the template, so
// the zero-amendment path changes nothing (Decision I).
//
// Deliberately narrower than Menu/Items/Rail in one way: resolving this
// slot only reorders/relabels/regroups the SIDEBAR nav — it does not move
// or rewrite the on-page card content itself, and (like Items/Rail before
// it) HIDE is refused entirely because no restore/findability surface
// (Decision D) exists yet for a hidden settings section. Physically
// reordering ~2,100 lines of interleaved elevation-gated forms, and
// building that restore surface, is real further work — filed as its own
// follow-up rather than folded into this slice (see this card's own PR
// description / close-out comment for the tracking issue), matching how
// ADR-0088 itself shipped one slot at a time.
var CoreSettings = []Entry{
	{Key: "registration", Href: "#registration", LabelKey: "settings.enrol.title", Order: 100},
	{Key: "settings-issuereport", Href: "#settings-issuereport", LabelKey: "issuereport.title", Order: 200},
	{Key: "settings-menulayout", Href: "#settings-menulayout", LabelKey: "menulayout.title", Order: 300},
	{Key: "settings-update", Href: "#settings-update", LabelKey: "settings.update.title", Order: 400},
	{Key: "settings-theme", Href: "#settings-theme", LabelKey: "settings.theme.title", Order: 500},
	{Key: "settings-display", Href: "#settings-display", LabelKey: "settings.display.title", Order: 600},
	{Key: "settings-payments", Href: "#settings-payments", LabelKey: "settings.payments.title", Order: 700},
	{Key: "settings-order-no", Href: "#settings-order-no", LabelKey: "settings.order_no.title", Order: 800},
	{Key: "settings-barcode", Href: "#settings-barcode", LabelKey: "settings.barcode.symbologies_title", Order: 900},
	{Key: "settings-catalog-import-barcode-default", Href: "#settings-catalog-import-barcode-default", LabelKey: "settings.catalog_import_barcode_default.title", Order: 1000},
	{Key: "settings-stock-tracking", Href: "#settings-stock-tracking", LabelKey: "settings.stock_tracking.title", Order: 1100},
	{Key: "settings-backup", Href: "#settings-backup", LabelKey: "settings.backup.title", Order: 1200},
	{Key: "settings-data", Href: "#settings-data", LabelKey: "settings.data.title", Order: 1300},
	{Key: "settings-retention", Href: "#settings-retention", LabelKey: "settings.retention.title", Order: 1400},
	{Key: "settings-printer", Href: "#settings-printer", LabelKey: "settings.printer.title", Order: 1500},
	{Key: "settings-tills", Href: "#settings-tills", LabelKey: "tills.title", Order: 1600},
	{Key: "settings-invoice", Href: "#settings-invoice", LabelKey: "settings.invoice.title", Order: 1700},
	{Key: "settings-idle-lock", Href: "#settings-idle-lock", LabelKey: "settings.idle_lock.title", Order: 1800},
	{Key: "settings-kiosk-idle-reset", Href: "#settings-kiosk-idle-reset", LabelKey: "settings.kiosk_idle_reset.title", Order: 1900},
	{Key: "settings-kiosk-payment-mode", Href: "#settings-kiosk-payment-mode", LabelKey: "settings.kiosk.payment_mode", Order: 2000},
	{Key: "settings-telemetry", Href: "#settings-telemetry", LabelKey: "settings.telemetry.title", Order: 2100},
	{Key: "settings-currency", Href: "#settings-currency", LabelKey: "settings.currency.title", Order: 2200},
	{Key: "settings-language", Href: "#settings-language", LabelKey: "settings.language.title", Order: 2300},
	{Key: "settings-shop-type", Href: "#settings-shop-type", LabelKey: "settings.shop_type.title", Order: 2400},
	{Key: "settings-all", Href: "#settings-all", LabelKey: "settings.all", Order: 2500},
}

var coreSettingsIndex = func() map[string]int {
	m := make(map[string]int, len(CoreSettings))
	for i, e := range CoreSettings {
		m[e.Key] = i
	}
	return m
}()

// CoreSettingsEntry returns the declared core Settings-slot entry for key —
// CoreMenuEntry's twin for SettingsSlot.
func CoreSettingsEntry(key string) (Entry, bool) {
	i, ok := coreSettingsIndex[key]
	if !ok {
		return Entry{}, false
	}
	return CoreSettings[i], true
}

// Resolve applies amendments to entries and returns the slot to render.
//
// Zero-plugin guarantee (ADR-0088 Decision I): with no amendments this is a
// length check returning the caller's own slice — no copy, no sort, no
// allocation (TestResolve_ZeroAmendmentsAllocatesNothing pins it). With
// amendments it works on a copy and never mutates entries: a hidden key is
// dropped, a re-label / re-icon keeps the core value in LabelFallback /
// IconFallback, and a stable sort by Order runs only if some amendment
// actually reordered.
func Resolve(entries []Entry, amendments []Amendment) []Entry {
	if len(amendments) == 0 {
		return entries
	}
	out := make([]Entry, 0, len(entries))
	reordered := false
	regrouped := false
	for _, e := range entries {
		hidden := false
		for _, a := range amendments {
			if a.Key != e.Key {
				continue
			}
			if a.Hide {
				hidden = true
				break
			}
			if a.Order != nil {
				e.Order = *a.Order
				reordered = true
			}
			if a.LabelKey != "" {
				if e.LabelFallback == "" {
					e.LabelFallback = e.LabelKey
				}
				e.LabelKey = a.LabelKey
			}
			if a.Group != "" {
				e.Group = a.Group
				regrouped = true
			}
			if a.Icon != "" {
				if e.IconFallback == "" {
					e.IconFallback = e.Icon
				}
				e.Icon = a.Icon
			}
		}
		if !hidden {
			out = append(out, e)
		}
	}
	if reordered {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	}
	if regrouped {
		groupTogether(out)
	}
	return out
}

// groupTogether makes "re-group" actually group (independent review of
// ut-docs#1904, F2). The renderer draws a heading whenever CONSECUTIVE
// entries change group, so two entries given the same group but landing
// apart in Order rendered the same heading TWICE with unrelated tiles
// between them — run-labelling, not grouping.
//
// Each group is anchored at the position of its earliest member and its
// remaining members are pulled up behind it; ungrouped entries keep their
// relative order around them. Stable: a slot with no grouped entries, or
// one whose groups are already contiguous, comes out byte-identical.
func groupTogether(out []Entry) {
	first := make(map[string]int, len(out))
	for i, e := range out {
		if e.Group == "" {
			continue
		}
		if _, seen := first[e.Group]; !seen {
			first[e.Group] = i
		}
	}
	if len(first) == 0 {
		return
	}
	// rank: an entry sorts at its group's earliest position (ungrouped
	// entries at their own), so groups gather without reordering anything
	// across group boundaries.
	rank := func(i int) int {
		if g := out[i].Group; g != "" {
			return first[g]
		}
		return i
	}
	idx := make([]int, len(out))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return rank(idx[a]) < rank(idx[b]) })
	sorted := make([]Entry, len(out))
	for pos, i := range idx {
		sorted[pos] = out[i]
	}
	copy(out, sorted)
}

// Conflict is an install-time collision between two plugins restructuring
// the same key (Decision F).
type Conflict struct {
	Key       string
	Incumbent string // plugin id already holding the key
}

// FindConflict returns the first candidate amendment that restructures a
// key an amendment of a DIFFERENT plugin already restructures. Hides never
// conflict (idempotent); a plugin never conflicts with its own rows, so
// reinstall / upgrade stay clean. Deterministic: candidates are checked in
// their declared order, so callers can report conflicts[0].
func FindConflict(candidate, installed []Amendment) (Conflict, bool) {
	for _, c := range candidate {
		if !c.Restructures() {
			continue
		}
		for _, in := range installed {
			// Slot equality (ut-docs#1911): two amendments only conflict
			// when they restructure the SAME destination — a slot+key
			// pair, not a bare key. Every Amendment produced by this
			// package's own parser always carries its Slot, so this is a
			// no-op for any caller that predates multi-slot (all of that
			// caller's Amendments share the same Slot value already).
			if in.Slot == c.Slot && in.Key == c.Key && in.PluginID != c.PluginID && in.Restructures() {
				return Conflict{Key: c.Key, Incumbent: in.PluginID}, true
			}
		}
	}
	return Conflict{}, false
}

// ParseAmendmentsJSON parses the config_json persisted for a `layout`
// plugin entry (internal/data.PluginRepo.ListLayoutEntries) and dispatches
// on its declared slot (ut-docs#1911: generalized from the original
// Menu-only ParseMenuAmendmentsJSON, which had no other slot to route to).
// "" is a layout entry that declares nothing. A config with no "slot"
// field — every manifest written before this card — defaults to MenuSlot,
// ParseMenuAmendments' own long-standing default, so no existing manifest
// changes behaviour.
func ParseAmendmentsJSON(pluginID, configJSON string) ([]Amendment, error) {
	cfg, err := unmarshalConfig(configJSON)
	if err != nil || cfg == nil {
		return nil, err
	}
	switch slot, _ := cfg["slot"].(string); slot {
	case ItemsSlot:
		return ParseItemsAmendments(pluginID, cfg)
	case RailSlot:
		return ParseRailAmendments(pluginID, cfg)
	case SettingsSlot:
		return ParseSettingsAmendments(pluginID, cfg)
	case "", MenuSlot:
		return ParseMenuAmendments(pluginID, cfg)
	default:
		// A slot name that is none of the known ones — refuse directly
		// here, naming every supported slot, rather than falling through to
		// ParseMenuAmendments: that would still correctly refuse (its own
		// slot check doesn't match either), but its error message only
		// knows about menuSpec's own name, under-reporting what this
		// dispatcher actually supports (independent review of ut-docs#1911).
		return nil, fmt.Errorf("layout entry names unsupported slot %q (supported: %s, %s, %s, %s)", slot, MenuSlot, ItemsSlot, RailSlot, SettingsSlot)
	}
}

func unmarshalConfig(configJSON string) (map[string]any, error) {
	if strings.TrimSpace(configJSON) == "" {
		return nil, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, fmt.Errorf("layout entry config is not a JSON object: %w", err)
	}
	return cfg, nil
}

// menuSpec and itemsSpec are the two slotSpecs parseSlotAmendments resolves
// against — see that function's doc comment for the shape a new slot needs.
var menuSpec = slotSpec{name: MenuSlot, lookup: CoreMenuEntry, protected: IsProtectedMenuKey, allowHide: true, allowIcon: true, allowGroup: true}

// itemsSpec allows neither hide (no restore surface exists for this slot
// yet — Decision D) nor icon/group (items_rail.html draws neither): only
// relabel and reorder, exactly what the shipped plugins/layout-salon demo
// needs. Widen this once the render side and a restore surface exist.
var itemsSpec = slotSpec{name: ItemsSlot, lookup: CoreItemsEntry, protected: IsProtectedItemsKey}

// railSpec mirrors itemsSpec's capability set exactly (ADR-0088 Decision
// J): no restore surface exists for a hidden rail entry either, so hide
// stays refused; nav.html draws no group heading, so group stays refused
// too. Icon is ALSO refused here even though nav.html DOES draw an icon
// (unlike items_rail.html), because the render side
// (internal/httpx/rail.go) resolves it as a fixed core-icon lookup and
// doesn't yet expose a per-amendment icon override — so allowIcon could be
// widened later with no render changes needed, once that's built. Only
// relabel and reorder are accepted, validated against railSpec.protected's
// allowHide/allowIcon/allowGroup/protected checks the other two slots use.
var railSpec = slotSpec{name: RailSlot, lookup: CoreRailEntry, protected: IsProtectedRailKey}

// settingsSpec mirrors itemsSpec/railSpec's capability set (ut-docs#1913):
// hide is refused (no restore/findability surface exists for this slot yet
// — Decision D), icon is refused (the settings-tree draws no icon), and
// group IS allowed — the sidebar draws a heading between consecutive
// entries whose resolved Group differs, the same groupTogether/GroupHeading
// mechanism the Menu slot already uses. Widen allowHide once a "Settings →
// Hidden sections" restore page exists, mirroring menu_layout_settings_page.go.
var settingsSpec = slotSpec{name: SettingsSlot, lookup: CoreSettingsEntry, protected: IsProtectedSettingsKey, allowGroup: true}

// ParseSettingsAmendments is ParseMenuAmendments' twin for the /settings
// sidebar (ADR-0088, ut-docs#1913): same schema and refusal shapes,
// validated against uislot.CoreSettings / ProtectedSettingsKeys instead of
// CoreMenu / ProtectedMenuKeys.
func ParseSettingsAmendments(pluginID string, config map[string]any) ([]Amendment, error) {
	return parseSlotAmendments(pluginID, settingsSpec, config)
}

// ParseMenuAmendments parses and validates a `layout` entry's config
// document (the manifest entry's `config`):
//
//	{"slot": "menu", "amendments": [
//	   {"key": "/tables", "hide": true},
//	   {"key": "/items", "label_key": "layout.salon.services", "icon": "scissors", "order": 50, "group": "layout.salon.group"}
//	]}
//
// Refused, each with an error naming what is wrong: an unsupported slot,
// a key that is not a declared core menu key, a hide of a protected key
// (Decision E), a hide combined with a restructure, an amendment that does
// nothing, the same key twice, a field this schema does not define (a typo
// would otherwise be the silent no-op Decision E rejects), and a
// wrong-typed value. A nil/empty config declares nothing and parses as
// zero amendments (the minimal taxonomy-test manifest shape). Icon names
// are NOT validated here — an unknown name falls back at render (Decision
// H), and the icon set is core's, not this package's.
func ParseMenuAmendments(pluginID string, config map[string]any) ([]Amendment, error) {
	return parseSlotAmendments(pluginID, menuSpec, config)
}

// ParseItemsAmendments is ParseMenuAmendments' twin for the /items section
// list (ADR-0088, generalized by ut-docs#1911): same schema and refusal
// shapes, validated against uislot.CoreItems / ProtectedItemsKeys instead
// of CoreMenu / ProtectedMenuKeys.
func ParseItemsAmendments(pluginID string, config map[string]any) ([]Amendment, error) {
	return parseSlotAmendments(pluginID, itemsSpec, config)
}

// ParseRailAmendments is ParseMenuAmendments' twin for the nav rail
// (ADR-0088, ut-docs#1912): same schema and refusal shapes, validated
// against uislot.CoreRail / ProtectedRailKeys instead of CoreMenu/CoreItems.
func ParseRailAmendments(pluginID string, config map[string]any) ([]Amendment, error) {
	return parseSlotAmendments(pluginID, railSpec, config)
}

// slotSpec is what parseSlotAmendments needs to validate one slot's
// amendments — everything ADR-0088 Decision C/E require per slot: the
// slot's own name (matched against the config's "slot" field), how to look
// up whether a key is a declared core destination of THIS slot, how to
// tell whether a key is protected (Decision E) in THIS slot, and which
// amendment KINDS this slot's renderer actually consumes.
//
// allowHide/allowIcon/allowGroup exist because "the schema accepts it" and
// "the slot can safely render it" are two different questions (independent
// review of ut-docs#1911, findings 1/2/5) — accepting an amendment kind the
// renderer ignores is not a harmless no-op:
//   - hide without a findability/restore surface (ADR-0088 Decision D:
//     "a hide mechanism without this surface is not shippable") strands a
//     merchant with no way back except uninstalling the plugin, AND an
//     all-rows-hidden slot crashes any render path that assumes its
//     resolved list is non-empty (items_page.go's sections[0]).
//   - group on a slot whose template draws no heading is not cosmetically
//     inert: uislot.Resolve's groupTogether still physically reorders
//     same-group entries adjacent, so it silently moves rows with no
//     visible explanation, and it still satisfies Restructures() so it
//     squats the key under Decision F, blocking a legitimate amendment
//     from a different plugin.
//   - icon on a slot whose template never draws one is the one genuinely
//     harmless case of these three, but refusing it now costs nothing and
//     keeps the same "every amendment kind either renders or is refused,
//     never accepted-and-ignored" contract this mechanism was built to
//     hold — permissive-now/strict-later is the one direction that can't
//     be taken back once a third-party plugin ships one.
//
// A new slot wires up its own core table (uislot.CoreItems' shape),
// protected-key slice, and exactly the capability flags its OWN template
// already renders; nothing else in this file changes.
type slotSpec struct {
	name       string
	lookup     func(key string) (Entry, bool)
	protected  func(key string) bool
	allowHide  bool
	allowIcon  bool
	allowGroup bool
}

// parseSlotAmendments is ParseMenuAmendments'/ParseItemsAmendments' shared
// body (ut-docs#1911: the original menu-only mechanism generalized without
// changing either function's external behaviour — every existing caller
// and test of ParseMenuAmendments keeps its exact refusal shapes, because
// menuSpec above validates against exactly the same MenuSlot/CoreMenu/
// ProtectedMenuKeys it always did).
func parseSlotAmendments(pluginID string, spec slotSpec, config map[string]any) ([]Amendment, error) {
	if len(config) == 0 {
		return nil, nil
	}
	for k := range config {
		if k != "slot" && k != "amendments" {
			return nil, fmt.Errorf("layout entry config has unknown field %q (allowed: slot, amendments)", k)
		}
	}
	if raw, ok := config["slot"]; ok {
		slot, isStr := raw.(string)
		if !isStr || slot != spec.name {
			return nil, fmt.Errorf("layout entry names unsupported slot %v (supported: %s)", raw, spec.name)
		}
	}
	rawList, ok := config["amendments"]
	if !ok {
		return nil, fmt.Errorf("layout entry config has no amendments list")
	}
	list, ok := rawList.([]any)
	if !ok {
		return nil, fmt.Errorf("layout entry config field amendments must be a list")
	}
	out := make([]Amendment, 0, len(list))
	seen := make(map[string]bool, len(list))
	for i, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("layout amendment #%d must be an object", i+1)
		}
		a, err := parseAmendment(pluginID, spec, obj)
		if err != nil {
			return nil, fmt.Errorf("layout amendment #%d: %w", i+1, err)
		}
		if seen[a.Key] {
			return nil, fmt.Errorf("layout amendment #%d: key %q is amended more than once in this entry", i+1, a.Key)
		}
		seen[a.Key] = true
		out = append(out, a)
	}
	return out, nil
}

func parseAmendment(pluginID string, spec slotSpec, obj map[string]any) (Amendment, error) {
	a := Amendment{PluginID: pluginID, Slot: spec.name}
	for field, raw := range obj {
		switch field {
		case "key":
			s, ok := raw.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return a, fmt.Errorf("field key must be a non-empty string")
			}
			a.Key = s
		case "hide":
			b, ok := raw.(bool)
			if !ok {
				return a, fmt.Errorf("field hide must be true or false")
			}
			a.Hide = b
		case "order":
			n, ok := integerValue(raw)
			if !ok {
				return a, fmt.Errorf("field order must be an integer")
			}
			a.Order = &n
		case "label_key", "group", "icon":
			s, ok := raw.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return a, fmt.Errorf("field %s must be a non-empty string", field)
			}
			switch field {
			case "label_key":
				a.LabelKey = s
			case "group":
				a.Group = s
			case "icon":
				a.Icon = s
			}
		default:
			return a, fmt.Errorf("unknown field %q (allowed: key, hide, order, label_key, group, icon)", field)
		}
	}
	if a.Key == "" {
		return a, fmt.Errorf("missing required field key")
	}
	if _, ok := spec.lookup(a.Key); !ok {
		return a, fmt.Errorf("key %q is not a core %s-slot destination", a.Key, spec.name)
	}
	// Capability refusal (independent review of ut-docs#1911, findings
	// 1/2/5): an amendment kind this slot's spec doesn't allow is refused
	// here, at install, never accepted and silently ignored at render —
	// see slotSpec's own doc comment for why each of these three is a real
	// hazard, not ceremony.
	if a.Hide && !spec.allowHide {
		return a, fmt.Errorf("key %q: the %s slot has no restore surface for a hidden destination yet (ADR-0088 Decision D requires one) — hide is refused here until one exists", a.Key, spec.name)
	}
	if a.Icon != "" && !spec.allowIcon {
		return a, fmt.Errorf("key %q: the %s slot's renderer does not draw an icon — icon is refused here rather than silently ignored", a.Key, spec.name)
	}
	if a.Group != "" && !spec.allowGroup {
		return a, fmt.Errorf("key %q: the %s slot's renderer draws no group heading, but group would still silently reorder rows — refused here rather than accepted and ignored", a.Key, spec.name)
	}
	if a.Hide && a.Restructures() {
		return a, fmt.Errorf("key %q: hide cannot be combined with order/label_key/group/icon", a.Key)
	}
	if !a.Hide && !a.Restructures() {
		return a, fmt.Errorf("key %q: amendment does nothing (set hide, order, label_key, group or icon)", a.Key)
	}
	if spec.protected(a.Key) {
		// A protected destination's IDENTITY is protected, not just its
		// presence (ADR-0088 Decision E, tightened by the independent
		// review of ut-docs#1904). Hiding /fiscal-register is refused —
		// but re-labelling it "Catalog" with a tag icon left it nominally
		// "visible" while making it unfindable to the merchant who needs
		// it under §146a Abs. 4 AO, which defeats the whole purpose of
		// protecting it. The label and the icon ARE how a merchant
		// recognises a tile, so they are refused too.
		//
		// order and group stay allowed: they move a protected tile
		// without disguising it, and a vertical legitimately needs to
		// position statutory destinations alongside its own.
		switch {
		case a.Hide:
			return a, fmt.Errorf("key %q is a protected destination and cannot be hidden by a plugin (ADR-0088)", a.Key)
		case a.LabelKey != "":
			return a, fmt.Errorf("key %q is a protected destination and cannot be re-labelled by a plugin (ADR-0088)", a.Key)
		case a.Icon != "":
			return a, fmt.Errorf("key %q is a protected destination and cannot be re-iconed by a plugin (ADR-0088)", a.Key)
		}
	}
	return a, nil
}

// integerValue reads an amendment's order: a JSON-decoded manifest yields
// float64, a Config map assembled in Go (tests, programmatic installs)
// yields int — both are the same declaration.
func integerValue(raw any) (int, bool) {
	switch v := raw.(type) {
	case float64:
		if v != math.Trunc(v) || math.Abs(v) > math.MaxInt32 {
			return 0, false
		}
		return int(v), true
	case int:
		return v, true
	case int64:
		if v > math.MaxInt32 || v < math.MinInt32 {
			return 0, false
		}
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		if err != nil || n > math.MaxInt32 || n < math.MinInt32 {
			return 0, false
		}
		return int(n), true
	}
	return 0, false
}
