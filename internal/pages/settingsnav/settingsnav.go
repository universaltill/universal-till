// Package settingsnav resolves the /settings page's sidebar section list
// (ut-docs#1913) — the third named follow-up slot ADR-0088's "Scope of the
// first implementation" listed after Items and Rail. It exists as its own
// leaf package for the same reason internal/pages/itemsnav does: a small,
// independently testable resolution step between uislot's generic registry
// and settings_page.go's already very large handler (2,700+ lines).
//
// Deliberately narrower than itemsnav.Resolve / the Menu slot: this package
// resolves the SIDEBAR's order/label/grouping only. The on-page card
// content in web/ui/pages/settings.html keeps its own declared DOM order
// and heading text — see uislot.CoreSettings' own doc comment for why that
// split is this slice's explicit scope, not an oversight.
package settingsnav

import (
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
func Resolve(locale string, amendments []uislot.Amendment) []Row {
	resolved := uislot.Resolve(uislot.CoreSettings, amendments)
	out := make([]Row, len(resolved))
	for i, e := range resolved {
		group := ""
		if e.Group != "" {
			group = httpx.T(locale, e.Group)
		}
		out[i] = Row{
			Key:   e.Key,
			Label: emojiPrefix[e.Key] + rowLabel(locale, e),
			Group: group,
		}
	}
	return out
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
