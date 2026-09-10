package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/uislot"
)

// adminGroup is one domain cluster on the /admin tree page — a heading
// (translated through {{ T }}, never a literal) plus whichever of its
// member entries visibleAdminEntries decided this viewer can see. Built
// fresh per request in registerAdmin below; uislot.Entry itself gains no
// new field for this (ut-docs#2008 scope: grouping is this handler's own
// concern, not the slot registry's).
type adminGroup struct {
	HeadingKey string
	Entries    []uislot.Entry
}

// adminGroupOrder is the three domain clusters /admin groups its six
// destinations into, and the six keys that belong to each — Fiscal
// (statutory hardware/registration), Locations (stock locations and their
// registers), Localization (country defaults and translations). Order here
// is the order the clusters render in; membership order within a cluster
// mirrors uislot.CoreMenu's own declared order (locations before
// registers, fiscal-register before fiscal-device) since adminGroupsFor
// walks visibleAdminEntries once, in that order, and buckets each entry
// into whichever cluster claims its Key.
var adminGroupOrder = []struct {
	headingKey string
	keys       []string
}{
	{"admin.group.fiscal", []string{"/fiscal-register", "/fiscal-device"}},
	{"admin.group.locations", []string{"/locations", "/registers"}},
	{"admin.group.localization", []string{"/translations", "/country-settings"}},
}

// adminGroupsFor buckets visible into the three domain clusters above, in
// adminGroupOrder's order, skipping any cluster that ends up with zero
// visible entries after filtering (no empty group heading rendered) — the
// AC's explicit requirement. Each of the six core entries lands in exactly
// one of the three named clusters — TestAdminGroupsFor_EveryCoreAdministrationEntryClaimedByExactlyOneNamedCluster
// pins that every key uislot.CoreMenu currently tags with the
// Administration group is claimed by exactly one cluster here.
//
// A visible entry NONE of the three named clusters claims still renders,
// in an "Other" catch-all appended last, rather than silently vanishing
// (found in review, ut-docs#2008): visibleAdminEntries counts a `layout`
// plugin regrouping some OTHER core entry INTO
// "menu.group.administration" (ADR-0088 Decision F, a supported,
// validated amendment) toward the tile's own visibility, but that key was
// never one of the six adminGroupOrder was written against — without this
// fallback the entry would make the tile appear/stay visible while never
// actually appearing anywhere on the page it opens.
// TestAdminPage_RegroupIntoAdministrationMovesEntryFromMenuToAdminTree
// exercises this via a real amendment, not just the static-coverage test
// above.
func adminGroupsFor(visible []uislot.Entry) []adminGroup {
	byKey := make(map[string]uislot.Entry, len(visible))
	for _, e := range visible {
		byKey[e.Key] = e
	}
	claimed := make(map[string]bool, len(visible))
	groups := make([]adminGroup, 0, len(adminGroupOrder)+1)
	for _, g := range adminGroupOrder {
		var entries []uislot.Entry
		for _, k := range g.keys {
			if e, ok := byKey[k]; ok {
				entries = append(entries, e)
				claimed[k] = true
			}
		}
		if len(entries) == 0 {
			continue
		}
		groups = append(groups, adminGroup{HeadingKey: g.headingKey, Entries: entries})
	}
	var uncategorized []uislot.Entry
	for _, e := range visible {
		if !claimed[e.Key] {
			uncategorized = append(uncategorized, e)
		}
	}
	if len(uncategorized) > 0 {
		groups = append(groups, adminGroup{HeadingKey: "admin.group.other", Entries: uncategorized})
	}
	return groups
}

// registerAdmin wires GET /admin (ut-docs#2008): the tree page the Menu
// launcher's single "Administration" tile opens, replacing #1959's
// group-heading-on-the-flat-grid with a dedicated page listing whichever of
// the six Group: "menu.group.administration" core destinations this viewer
// can reach, grouped into three domain clusters (adminGroupOrder above).
//
// Gated the same shape as country_settings_page.go's requireManager: a
// direct hit on /admin with no session (or a role that unlocks none of the
// six) 403s — this must refuse even when the tile that would have led here
// is already hidden (menu_page.go's "administration" predicate), the same
// "visibility is not the security boundary" rule every one of the six
// destination pages already enforces on itself (see this card's permission
// audit). Deliberately checked via visibleAdminEntries, the exact same
// function and the exact same menuVisibility mechanics the Menu launcher's
// own tile gate uses — one path decides who can reach these six, not two
// that could drift apart.
func registerAdmin(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		visible := visibleAdminEntries(d, r)
		if len(visible) == 0 {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return
		}
		httpx.Render("ui/pages/admin.html", map[string]any{
			"title":     "Administration",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"Groups":    adminGroupsFor(visible),
			"BackHref":  "/menu",
		})(w, r)
	})
}
