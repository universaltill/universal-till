package pages

import (
	"bytes"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
	"github.com/universaltill/universal-till/internal/uislot"
)

// adminGroup is one domain cluster on the /admin tree page — a heading
// (translated through {{ T }}, never a literal) plus whichever of its
// member entries visibleAdminEntries decided this viewer can see. Built
// fresh per request in registerAdmin below.
type adminGroup struct {
	HeadingKey string
	Entries    []adminTreeEntry
}

// adminTreeEntry is one row in the tree: a uislot.Entry plus whether this
// destination has its own writeAdminTreeOOB/httpx.IsFragmentSwap-wired
// handler capable of answering the row's hx-get as a content fragment
// (found in review, ut-docs#2139). Only the six adminGroupOrder members do
// — an entry a `layout` plugin regroups INTO Administration (ADR-0088
// Decision F) and that lands in adminGroupsFor's "Other" catch-all below
// has no such handler at its own route, so wiring hx-get/hx-target onto it
// swaps that page's whole standalone HTML document into #admin-panel
// instead of a fragment. admin_tree.html only emits the htmx attributes
// when this is true; a non-fragment-capable row stays a plain <a href>
// real navigation, same as every row behaved before ut-docs#2116.
type adminTreeEntry struct {
	uislot.Entry
	FragmentCapable bool
}

// adminFragmentCapableHrefs is adminGroupOrder's own key set, flattened
// into a lookup — derived from it rather than re-listed by hand, so the
// two can never drift apart (adding a seventh fragment-capable destination
// to adminGroupOrder makes it fragment-capable automatically).
var adminFragmentCapableHrefs = func() map[string]bool {
	m := make(map[string]bool)
	for _, g := range adminGroupOrder {
		for _, k := range g.keys {
			m[k] = true
		}
	}
	return m
}()

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
	toEntry := func(e uislot.Entry) adminTreeEntry {
		return adminTreeEntry{Entry: e, FragmentCapable: adminFragmentCapableHrefs[e.Href]}
	}
	claimed := make(map[string]bool, len(visible))
	groups := make([]adminGroup, 0, len(adminGroupOrder)+1)
	for _, g := range adminGroupOrder {
		var entries []adminTreeEntry
		for _, k := range g.keys {
			if e, ok := byKey[k]; ok {
				entries = append(entries, toEntry(e))
				claimed[k] = true
			}
		}
		if len(entries) == 0 {
			continue
		}
		groups = append(groups, adminGroup{HeadingKey: g.headingKey, Entries: entries})
	}
	var uncategorized []adminTreeEntry
	for _, e := range visible {
		if !claimed[e.Key] {
			uncategorized = append(uncategorized, toEntry(e))
		}
	}
	if len(uncategorized) > 0 {
		groups = append(groups, adminGroup{HeadingKey: "admin.group.other", Entries: uncategorized})
	}
	return groups
}

// adminEmbedHeader marks a sub-request whose fragment body is about to be
// INLINED into /admin's own default panel load (embedAdminSection below) —
// mirrors itemsnav.EmbedHeader/IsEmbed exactly, and for the identical
// reason: /admin already renders the tree itself immediately above the
// panel, so the embedded destination's own OOB tree copy (writeAdminTreeOOB)
// must be suppressed there, or a bare GET /admin would paint the whole tree
// a second time inside the right panel.
const adminEmbedHeader = "X-UT-Admin-Embed"

// isAdminEmbed reports whether r is such an inlined sub-request.
func isAdminEmbed(r *http.Request) bool {
	return r != nil && r.Header.Get(adminEmbedHeader) != ""
}

// isAdminPanelSwap reports whether r is an htmx request whose target is
// /admin's own #admin-panel — i.e. a tree-row click, the only case where
// the out-of-band tree refresh has anything to swap into. htmx sends the
// target element's id in HX-Target, and every admin_tree.html row targets
// #admin-panel. An in-page htmx control on one of the six destinations
// (ut-docs#2167's country-scope chips) targets its own subtree instead, and
// the standalone page has no tree at all.
//
// What this does NOT do, stated plainly so nobody re-derives it: htmx 1.9
// silently DISCARDS an out-of-band fragment whose target is absent — no
// console error, nothing inserted into the DOM. Measured during ut-docs#2167
// by reverting this guard and re-running that card's e2e spec, which still
// passed. So this is not fixing a visible break; it stops the handler doing
// real work (visibleAdminEntries + adminGroupsFor + a template render) on
// every in-page toggle for output the browser will throw away, and keeps the
// response honest about what it is. internal/pages/country_settings_page.go
// is its only caller today; a destination growing its own in-page htmx
// control wants the same guard.
func isAdminPanelSwap(r *http.Request) bool {
	return r != nil && r.Header.Get("HX-Target") == "admin-panel"
}

// writeAdminTreeOOB renders web/ui/partials/admin_tree.html as an
// out-of-band swap (id="admin-tree", hx-swap-oob="true") with currentHref's
// row marked is-current, and writes it to w — for appending after the htmx
// fragment response of any of the tree's six destinations, so the tree's
// active-node highlight follows the click no matter which handler answered
// it (ut-docs#2116, mirroring itemsnav.WriteRailOOB/ut-docs#1950 exactly).
// Rendered into a buffer first, not straight to w, so a template error
// can't reach the client as a truncated hx-swap-oob="true" fragment.
// Best-effort: silently does nothing on error, same as WriteRailOOB.
//
// No-ops for an inlined embed (see adminEmbedHeader) — that caller's page
// draws the tree itself, so a second copy here would be a duplicate DOM id
// and a visibly doubled tree.
func writeAdminTreeOOB(w io.Writer, r *http.Request, funcs template.FuncMap, currentHref string, groups []adminGroup) {
	if isAdminEmbed(r) {
		return
	}
	view, err := ui.NewAdminTreeView(funcs)
	if err != nil {
		return
	}
	var buf bytes.Buffer
	if view.Render(&buf, map[string]any{
		"Groups":      groups,
		"CurrentHref": currentHref,
		"OOB":         true,
	}) != nil {
		return
	}
	_, _ = w.Write(buf.Bytes())
}

// embedAdminSection replays r as a GET to href with "HX-Request: true" and
// adminEmbedHeader set, through the SAME mux this handler is registered on,
// and returns the htmx fragment body that handler renders — reusing its
// exact data-fetch and template logic instead of duplicating any of it
// here. Mirrors embedItemsSection (items_page.go) exactly, including its
// cookie/auth carry-over and its translated fallback-card behavior on any
// non-200 response; see that function's own doc comment for the full
// reasoning.
func embedAdminSection(mux *http.ServeMux, r *http.Request, href string) template.HTML {
	sub := httptest.NewRequest(http.MethodGet, href, nil).WithContext(r.Context())
	sub.Header.Set("HX-Request", "true")
	sub.Header.Set(adminEmbedHeader, "1")
	for _, c := range r.Cookies() {
		sub.AddCookie(c)
	}
	if u, ok := auth.FromContext(r.Context()); ok {
		sub = auth.WithUser(sub, u)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, sub)
	if rec.Code != http.StatusOK {
		logging.L().Errorf("admin panel: embedding %s failed (code %d): %s", href, rec.Code, rec.Body.String())
		locale := httpx.RequestLocale(r)
		msg := template.HTMLEscapeString(httpx.T(locale, "common.error.server"))
		return template.HTML(`<div class="card" style="color: var(--danger)">` + msg + `</div>`) //nolint:gosec // msg is HTML-escaped above; the surrounding markup is a fixed literal
	}
	return template.HTML(rec.Body.String()) //nolint:gosec // rec.Body is this same server's own rendered HTML for an authorized request, not user input
}

// firstFragmentCapableHref returns the first FragmentCapable entry's Href
// across groups, in order, or "" if none is (found in review, ut-docs#2139
// follow-up). registerAdmin uses this instead of blindly
// groups[0].Entries[0].Href: an always-visible core entry (VisibleIf=="",
// e.g. /open-orders) a `layout` plugin regroups into Administration can be
// the ONLY thing a low-privilege viewer sees here — landing in
// adminGroupsFor's "Other" catch-all as groups[0] for that viewer — and
// that entry has no fragment-capable handler at its own route. Embedding it
// as the default panel content would nest its whole standalone HTML
// document inside #admin-panel, the exact defect class this card exists to
// fix, just reached via the arrival path instead of the click path.
func firstFragmentCapableHref(groups []adminGroup) string {
	for _, g := range groups {
		for _, e := range g.Entries {
			if e.FragmentCapable {
				return e.Href
			}
		}
	}
	return ""
}

// registerAdmin wires GET /admin (ut-docs#2008, converted to the same
// two-pane master-detail shell /items uses by ut-docs#2116): the tree page
// the Menu launcher's single "Administration" tile opens, listing whichever
// of the six Group: "menu.group.administration" core destinations this
// viewer can reach, grouped into three domain clusters (adminGroupOrder
// above), with the first fragment-capable entry's own content embedded into
// the panel by default — mirrors items_page.go's registerItemsPage
// ("the right panel is never empty on arrival"), except when nothing
// visible is fragment-capable (see firstFragmentCapableHref above), in
// which case the panel is left empty rather than embedding a
// non-fragment-capable destination's whole standalone page.
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
		groups := adminGroupsFor(visible)
		current := firstFragmentCapableHref(groups)
		data := map[string]any{
			"title":       "Administration",
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.MenuSnapshot(),
			"Groups":      groups,
			"CurrentHref": current,
		}
		if current != "" {
			data["PanelHTML"] = embedAdminSection(mux, r, current)
		}
		httpx.Render("ui/pages/admin.html", data)(w, r)
	})
}
