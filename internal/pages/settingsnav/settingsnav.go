// Package settingsnav resolves the /settings page's sidebar section list
// (ut-docs#1913) — the third named follow-up slot ADR-0088's "Scope of the
// first implementation" listed after Items and Rail — and, since
// ut-docs#3090, the landing grid's category tiles derived from it
// (Row.Cat, Categories). It exists as its own leaf package for the same
// reason internal/pages/itemsnav does: a small, independently testable
// resolution step between uislot's generic registry and settings_page.go's
// already very large handler (2,700+ lines).
//
// Deliberately narrower than itemsnav.Resolve / the Menu slot: this package
// resolves the SIDEBAR's order/label/grouping (and the categories that
// group it) only. The on-page card content in web/ui/pages/settings.html
// keeps its own declared DOM order and heading text — see
// uislot.CoreSettings' own doc comment for why that split is this slice's
// explicit scope, not an oversight.
package settingsnav

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/uislot"
)

// Row is one RESOLVED row of the /settings sidebar — the shape
// settings.html's server-rendered #settings-nav-index consumes (its own JS
// then builds the visible <ul id="settings-tree"> from this, correlating
// each Key to whichever `.card` elements this particular render actually
// included — a card gated by e.g. isManager/payMethods that didn't render
// this time is simply absent from the DOM, and the JS skips any Row with no
// matching element rather than needing to know why).
type Row struct {
	Key   string
	Label string // already resolved/localized — settings.html's JS does no translation of its own
	Group string // already resolved/localized; "" means no heading before this row
	// Cat is the id of the landing-grid category this row belongs to
	// (ut-docs#3090): a uislot.SettingsCategories Key, or "g-<group>" for a
	// `layout` plugin's own group, which becomes a category of its own.
	Cat string
}

// Category is one resolved tile of the /settings landing grid
// (ut-docs#3090), already localized for this render.
type Category struct {
	ID    string // Row.Cat of its rows; the page links to #cat-<ID>
	Label string
	Desc  string // "" for a plugin group (no description to show)
	Icon  string // a name in internal/httpx/icons.go
}

// pluginGroupIcon is the tile icon for a `layout` plugin's own group,
// which declares no icon of its own (ADR-0088's amendment schema has
// none for a group).
const pluginGroupIcon = "puzzle"

var nonIDChars = regexp.MustCompile(`[^a-z0-9_-]+`)

// catOf maps a resolved entry's Group (a locale KEY, before translation)
// to its category id and sort rank. Core categories rank in declared
// order; a plugin's own group ranks after every core category but before
// Advanced; a row with no group at all is Advanced-only.
func catOf(group string) (id string, rank int) {
	n := len(uislot.SettingsCategories)
	for i, c := range uislot.SettingsCategories {
		if c.LabelKey == group {
			return c.Key, 2 * i
		}
	}
	if group == "" {
		return uislot.SettingsAdvancedCategory, 2 * (n - 1)
	}
	return "g-" + nonIDChars.ReplaceAllString(strings.ToLower(group), "-"), 2*(n-1) - 1
}

// emojiPrefix carries the small cosmetic decoration a handful of card
// headings render before their {{ T }} text (web/ui/pages/settings.html's
// own <h2> markup, e.g. "🧹 {{ T \"settings.data.title\" }}"). uislot.Entry
// has no such concept — it is generic across all four slots — so this is
// kept local to this package rather than widening that type for one slot's
// cosmetic detail. Needed so a zero-amendment resolution reproduces the
// sidebar's pre-#1913 text byte-for-byte (settings_page_test.go's golden
// test pins this).
var emojiPrefix = map[string]string{
	"settings-data":      "🧹 ",
	"settings-retention": "🗄️ ",
	"settings-tills":     "🔗 ",
	"settings-invoice":   "🧾 ",
}

// Resolve returns the /settings sidebar's rows for one render: uislot.CoreSettings
// amended by amendments (ADR-0088 Decision C) — the same
// uislot.Resolve+label-fallback pattern menu_page.go/itemsnav.go use for
// their own slots, generalized to the Settings slot by ut-docs#1913. With no
// amendments (the till's normal, zero-plugin state) uislot.Resolve hands
// CoreSettings straight back (Decision I's zero-allocation fast path).
//
// Rows are then gathered by category (ut-docs#3090) with a stable sort, so
// categories keep their declared order and a plugin's Order amendment
// reorders rows WITHIN a category.
func Resolve(locale string, amendments []uislot.Amendment) []Row {
	resolved := uislot.Resolve(uislot.CoreSettings, amendments)
	out := make([]Row, len(resolved))
	rank := make(map[string]int, len(resolved))
	// Two different plugin group keys can sanitize to the same id
	// ("layout.x.A" / "layout.x.a"); each distinct group keeps its own tile.
	idOfGroup := map[string]string{}
	groupOfID := map[string]string{}
	for i, e := range resolved {
		group := ""
		if e.Group != "" {
			group = httpx.T(locale, e.Group)
		}
		cat, r := catOf(e.Group)
		if id, ok := idOfGroup[e.Group]; ok {
			cat = id
		} else {
			base := cat
			for n := 2; groupOfID[cat] != "" && groupOfID[cat] != e.Group; n++ {
				cat = base + "-" + strconv.Itoa(n)
			}
			idOfGroup[e.Group], groupOfID[cat] = cat, e.Group
		}
		rank[e.Key] = r
		out[i] = Row{
			Key:   e.Key,
			Label: emojiPrefix[e.Key] + rowLabel(locale, e),
			Group: group,
			Cat:   cat,
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Key] < rank[out[j].Key] })
	return out
}

// Categories returns the landing grid's tiles for rows — the rows THIS
// render will actually show (after the page's own per-card gates), so a
// category whose every section is gated out gets no tile. Tiles follow
// the rows' own (already category-sorted) order. Advanced is always the
// last tile: its view lists every row.
func Categories(locale string, rows []Row) []Category {
	byKey := make(map[string]uislot.Category, len(uislot.SettingsCategories))
	for _, c := range uislot.SettingsCategories {
		byKey[c.Key] = c
	}
	var out []Category
	idx := map[string]int{}
	for _, r := range rows {
		if r.Cat == uislot.SettingsAdvancedCategory {
			continue
		}
		if _, ok := idx[r.Cat]; ok {
			continue
		}
		c := Category{ID: r.Cat, Label: r.Group, Icon: pluginGroupIcon}
		if decl, ok := byKey[r.Cat]; ok {
			c.Label = httpx.T(locale, decl.LabelKey)
			c.Desc = httpx.T(locale, decl.DescKey)
			c.Icon = decl.Icon
		}
		idx[r.Cat] = len(out)
		out = append(out, c)
	}
	adv := byKey[uislot.SettingsAdvancedCategory]
	return append(out, Category{
		ID:    adv.Key,
		Label: httpx.T(locale, adv.LabelKey),
		Desc:  httpx.T(locale, adv.DescKey),
		Icon:  adv.Icon,
	})
}

// rowLabel is menu_page.go's menuLabel / itemsnav.sectionLabel, restated
// here for the Settings slot (ADR-0088 Decision G): an amended entry's
// LabelKey resolves through the normal translator; if it does not resolve
// for this locale (T hands the key back unchanged), the CORE label key
// renders instead — a missing translation degrades to English, never to a
// raw, untranslated plugin key on a merchant's screen.
func rowLabel(locale string, e uislot.Entry) string {
	if e.LabelFallback != "" && httpx.T(locale, e.LabelKey) == e.LabelKey {
		return httpx.T(locale, e.LabelFallback)
	}
	return httpx.T(locale, e.LabelKey)
}
