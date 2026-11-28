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
	// Tag is "" unless a shop-type template is in effect (ResolveClassed
	// with a non-nil ClassFunc): TagRecommended for an essential row,
	// TagTucked for a row the template moved to More settings.
	Tag string
	// HomeCat is, for a TagTucked row, the category it belongs to when not
	// demoted (its "Something missing?" hint goes on that category's page).
	HomeCat string
}

// Template tags carried by Row.Tag.
const (
	TagRecommended = "recommended"
	TagTucked      = "tucked"
)

// ClassFunc is the effective visibility class of a settings row: its base
// class given, the active shop-type template's answer (ADR-0190). A nil
// ClassFunc means "no template": every row keeps its base class.
type ClassFunc func(key string, base uislot.Class) uislot.Class

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

// pluginGroupTile is a known plugin group's tile description (a locale key)
// and icon, keyed by the group's category id (ut-docs#7205). A group not
// listed keeps the generic puzzle icon and no description.
var pluginGroupTile = map[string]struct{ DescKey, Icon string }{
	"g-layout-salon-settings_group": {"layout.salon.settings_group_desc", "printer"},
}

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

// ResolveClassed resolves the /settings sidebar rows for one render (see
// uislot.Resolve and ut-docs#1913, #3090; rows gather by category with a stable
// sort so a plugin Order amendment reorders WITHIN a category), with a shop-type template's effective classes
// (ut-docs#6332, ADR-0190). A row whose effective class is advanced (and
// whose base class is not) gets Cat = advanced: it leaves its category page
// for More settings. Its Group, and its place in the list, stay those of
// its normal category, so the More settings view still lists every row
// under the normal category headings. Rows a layout plugin put into its own
// group are never demoted (plugin layout wins), nor are fixed-class rows.
// Rows the template promotes to essential sort first within their category and are tagged. With a nil
// classOf the result is exactly Resolve's pre-template output.
func ResolveClassed(locale string, amendments []uislot.Amendment, classOf ClassFunc) []Row {
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
		rank[e.Key] = 2 * r
		row := Row{
			Key:   e.Key,
			Label: emojiPrefix[e.Key] + rowLabel(locale, e),
			Group: group,
			Cat:   cat,
		}
		if classOf != nil && !strings.HasPrefix(cat, "g-") && !uislot.FixedClassFor(uislot.KindSettings, e.Key) {
			switch classOf(e.Key, e.Class) {
			case uislot.ClassEssential:
				// Only a row the template PROMOTED is "recommended":
				// rows essential by default (e.g. Theme) stay as today.
				if e.Class != uislot.ClassEssential {
					row.Tag = TagRecommended
					rank[e.Key]-- // before the rest of its category
				}
			case uislot.ClassAdvanced:
				if cat != uislot.SettingsAdvancedCategory {
					row.Tag, row.HomeCat, row.Cat = TagTucked, cat, uislot.SettingsAdvancedCategory
				}
			}
		}
		out[i] = row
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
	return categories(locale, rows, true)
}

// CategoriesLegacy is Categories for a shop in legacy salon replay mode
// (type set, no template ledger): plugin-group tiles keep the generic icon and
// no description, so the Settings page stays byte-identical to the build that
// shipped the salon layout plugin (ADR-0190, ut-docs#6332). #7205's tile
// description applies once the shop accepts a template.
func CategoriesLegacy(locale string, rows []Row) []Category {
	return categories(locale, rows, false)
}

func categories(locale string, rows []Row, groupTiles bool) []Category {
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
		if t, ok := pluginGroupTile[r.Cat]; ok && groupTiles {
			c.Desc = httpx.T(locale, t.DescKey)
			c.Icon = t.Icon
		}
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
