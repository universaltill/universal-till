package pages

import (
	"bytes"
	"html/template"
	"io"
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
		// ut-docs#2116: /admin itself is now the two-pane shell's empty
		// landing state (PanelHTML nil, CurrentHref "" — never a member of
		// .Groups, so no row shows is-current) rather than its own
		// distinct page; admin_shell.html folds in the old web/ui/pages/
		// admin.html's page-head/back-button for exactly this empty case.
		// No httpx.IsFragmentSwap branch here: nothing inside the shell
		// ever hx-gets /admin (visibleAdminEntries excludes it from the
		// tree itself, see that function's own doc comment), so this route
		// is only ever hit as a bare/direct GET.
		httpx.Render("ui/pages/admin_shell.html", map[string]any{
			"title":     "Administration",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"Groups":    adminGroupsFor(visible),
			"BackHref":  "/menu",
		})(w, r)
	})
}

// writeAdminTreeOOB renders the /admin tree partial (web/ui/partials/
// admin_tree.html) as an out-of-band swap (id="admin-tree",
// hx-swap-oob="true") with currentHref's row marked is-current, and writes
// it to w — appended after every one of the six destination handlers' own
// htmx fragment response (via renderAdminDestination below) so the tree's
// active-node highlight follows the click no matter which handler answered
// it (ut-docs#2116). Mirrors internal/pages/itemsnav.WriteRailOOB's
// identical buffer-first shape: rendered into a buffer first, not straight
// to w, so a template error can't reach the client as a truncated
// hx-swap-oob="true" fragment that htmx would still swap into the DOM.
// Best-effort — silently does nothing on error, same as that helper.
//
// Unlike WriteRailOOB this needs no EmbedHeader/IsEmbed check: that exists
// only for /items' cross-handler HTTP-sub-request embedding (its default
// panel reaching a DIFFERENT handler's code), which nothing here does —
// every admin destination handler already IS the code that renders its own
// content (see renderAdminDestination/httpx.RenderContentFragmentToString).
func writeAdminTreeOOB(w io.Writer, funcs template.FuncMap, currentHref string, groups []adminGroup) {
	t, err := httpx.ClonedTemplate("pages.adminTreeOOB", "base.html", funcs, "ui/partials/admin_tree.html")
	if err != nil {
		return
	}
	var buf bytes.Buffer
	if t.ExecuteTemplate(&buf, "admin_tree", map[string]any{
		"Groups":      groups,
		"CurrentHref": currentHref,
		"OOB":         true,
	}) != nil {
		return
	}
	_, _ = w.Write(buf.Bytes())
}

// renderAdminDestination is the shared GET-response tail for each of the
// six /admin destination handlers (ut-docs#2116; call sites:
// locations_page.go, registers_page.go, fiscal_register_page.go,
// fiscal_device_page.go, translations_page.go, country_settings_page.go).
// tplPath/data are exactly what a call site would otherwise have handed
// straight to httpx.Render — this returns the same http.HandlerFunc shape
// so the call site's own final line stays a one-liner, just naming its own
// route as currentHref.
//
// An htmx panel-swap request (httpx.IsFragmentSwap) gets just the
// destination's own "content" block plus an out-of-band refresh of the
// tree (writeAdminTreeOOB), exactly the shape internal/pages/catalog/
// handlers.go's own /catalog fragment branch already follows for the
// /items rail. A bare/direct GET instead gets the ENTIRE admin_shell.html
// page, with this destination's own content rendered once
// (httpx.RenderContentFragmentToString) and embedded as PanelHTML —
// deliberately stronger than /items' own precedent (whose five section
// destinations still render as independent standalone pages on a direct
// hit, see items.html's own doc comment): every one of these six is a
// set-once/onboarding admin screen, never a page a shop bookmarks or
// deep-links from outside the till, so there is no unwrapped-standalone
// shape left worth preserving (ut-docs#2116 AC #3).
//
// groups (and so which rows the reader can even see) is recomputed fresh
// on every call from visibleAdminEntries/adminGroupsFor — the exact same
// per-request gating registerAdmin's own /admin handler applies — rather
// than trusted from any caller-held value, so a destination page can never
// render inside a shell advertising a tree entry this viewer isn't
// actually allowed to reach.
func renderAdminDestination(d *common.Deps, currentHref, tplPath string, data map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		funcs := httpx.FuncsFor(httpx.RequestLocale(r))
		groups := adminGroupsFor(visibleAdminEntries(d, r))
		if httpx.IsFragmentSwap(w, r) {
			httpx.RenderContentFragment(tplPath, data)(w, r)
			writeAdminTreeOOB(w, funcs, currentHref, groups)
			return
		}
		// Review finding (ut-docs#2116): this viewer can reach currentHref
		// itself (their own requireManager-shaped gate already passed
		// before this call) but groups can still legitimately come back
		// empty for them — a `layout` plugin regrouping every OTHER
		// administration entry out of this cluster (ADR-0088 Decision F;
		// adminGroupsFor's own doc comment already anticipates the reverse
		// direction) is a real, reachable case, not a defensive
		// hypothetical. Rendering the shell anyway would reserve
		// .admin-layout's tree-column width beside an empty <aside> — a
		// blank gutter with nothing in it. Falling back to the plain
		// standalone render (this destination's pre-ut-docs#2116 shape)
		// degrades to something real instead.
		if len(groups) == 0 {
			httpx.Render(tplPath, data)(w, r)
			return
		}
		panelHTML, err := httpx.RenderContentFragmentToString(tplPath, data, r)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		httpx.Render("ui/pages/admin_shell.html", map[string]any{
			"title":       data["title"],
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.MenuSnapshot(),
			"Groups":      groups,
			"CurrentHref": currentHref,
			"PanelHTML":   panelHTML,
			// Defensive only (review finding): admin_shell.html's
			// empty-state branch is the only reader of .BackHref, and
			// PanelHTML is non-empty on every reachable path here — but
			// setting it removes even the theoretical broken-href risk if
			// RenderContentFragmentToString ever legitimately returns "".
			"BackHref": "/menu",
		})(w, r)
	}
}
