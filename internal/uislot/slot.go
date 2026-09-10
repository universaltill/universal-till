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
// The first — and so far only — slot is the Menu launcher (MenuSlot);
// further slots (ut-docs#1897 sections, #1332 sale rail, settings groups)
// attach as follow-up cards without redesign.
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
	// nav snapshot's order and every other core entry after them.
	InNav bool

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
var ProtectedMenuKeys = []string{
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
// plugin `page` tiles, 1900 open orders, 2000 help, 2100–2500 the manager-gated destinations
// with no Group, 2600–3100 the same gated band but grouped under
// "menu.group.administration" (ut-docs#1959) — set-once/onboarding
// destinations (country, translations, the two statutory fiscal-device
// pages, stock locations/registers). Kept contiguous deliberately: Resolve
// only re-runs groupTogether when an AMENDMENT regroups something (Decision
// I), so with no plugin installed the renderer walks CoreMenu as declared —
// an ungrouped entry landing between two same-Group entries here would
// split one heading into two identical ones.
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
	{Key: "/open-orders", Href: "/open-orders", LabelKey: "open_orders.title", Icon: "shopping-cart", Order: 1900},
	{Key: "/help", Href: "/help", LabelKey: "nav.help", Icon: "help", Order: 2000},
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
			if in.Key == c.Key && in.PluginID != c.PluginID && in.Restructures() {
				return Conflict{Key: c.Key, Incumbent: in.PluginID}, true
			}
		}
	}
	return Conflict{}, false
}

// ParseMenuAmendmentsJSON parses the config_json persisted for a `layout`
// plugin entry (internal/data.PluginRepo.ListLayoutEntries). "" is a
// layout entry that declares nothing.
func ParseMenuAmendmentsJSON(pluginID, configJSON string) ([]Amendment, error) {
	if strings.TrimSpace(configJSON) == "" {
		return nil, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, fmt.Errorf("layout entry config is not a JSON object: %w", err)
	}
	return ParseMenuAmendments(pluginID, cfg)
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
		if !isStr || slot != MenuSlot {
			return nil, fmt.Errorf("layout entry names unsupported slot %v (supported: %s)", raw, MenuSlot)
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
		a, err := parseAmendment(pluginID, obj)
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

func parseAmendment(pluginID string, obj map[string]any) (Amendment, error) {
	a := Amendment{PluginID: pluginID}
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
	if _, ok := CoreMenuEntry(a.Key); !ok {
		return a, fmt.Errorf("key %q is not a core menu destination", a.Key)
	}
	if a.Hide && a.Restructures() {
		return a, fmt.Errorf("key %q: hide cannot be combined with order/label_key/group/icon", a.Key)
	}
	if !a.Hide && !a.Restructures() {
		return a, fmt.Errorf("key %q: amendment does nothing (set hide, order, label_key, group or icon)", a.Key)
	}
	if IsProtectedMenuKey(a.Key) {
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
