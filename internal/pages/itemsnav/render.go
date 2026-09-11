package itemsnav

import (
	"bytes"
	"html/template"
	"io"
	"net/http"

	"github.com/universaltill/universal-till/internal/ui"
)

// EmbedHeader marks a sub-request whose fragment body is about to be
// INLINED into a page that already renders the rail itself — /items' own
// default panel load (items_page.go's embedItemsSection). Such a request
// still needs the "content"-only fragment (no base.html chrome), so it
// still sets HX-Request: true, but it must NOT get the out-of-band rail
// copy that a real htmx panel swap needs: /items has already rendered the
// rail immediately above the panel, so appending a second one emits a
// duplicate id="items-rail" and paints the whole section list a second
// time inside the right panel (review of ut-docs#1950 — a bare GET /items
// rendered 10 rows and two rails instead of 5 and one).
//
// Checked in WriteRailOOB rather than at each of the five call sites, so a
// sixth section handler added later cannot forget it.
const EmbedHeader = "X-UT-Items-Embed"

// IsEmbed reports whether r is such an inlined sub-request.
func IsEmbed(r *http.Request) bool {
	return r != nil && r.Header.Get(EmbedHeader) != ""
}

// WriteRailOOB renders the /items rail (web/ui/partials/items_rail.html) as
// an out-of-band swap (id="items-rail", hx-swap-oob="true") with
// currentHref's row marked is-current, and writes it to w — for appending
// after the htmx fragment response of any of the rail's five section
// destinations, so the rail's active-section highlight follows the click no
// matter which handler answered it (ut-docs#1950). Mirrors
// internal/pages/help_page.go's identical buffer-first pattern for
// help_nav.html's own OOB swap (ut-docs#351): rendered into a buffer first,
// not straight to w, so a template error can't reach the client as a
// truncated hx-swap-oob="true" fragment — htmx would swap that broken rail
// into the DOM, which is worse than just skipping the refresh. Best-effort:
// silently does nothing on error, same as help_page.go's version.
//
// sections is the caller's already-RESOLVED rail (Resolve(locale,
// amendments), ut-docs#1911) — this package cannot resolve it itself
// without importing internal/pages/common for the active layout
// amendments, which internal/pages/catalog (one of this rail's five
// callers) cannot reach without a cycle; every call site already has both
// a common.Deps and a request to resolve from, exactly like
// menu_page.go's own registerMenu does for the Menu slot.
//
// No-ops for an inlined embed (see EmbedHeader) — that caller's page draws
// the rail itself, so a second copy here would be a duplicate DOM id and a
// visibly doubled section list.
func WriteRailOOB(w io.Writer, r *http.Request, funcs template.FuncMap, currentHref string, sections []Section) {
	if IsEmbed(r) {
		return
	}
	view, err := ui.NewItemsRailView(funcs)
	if err != nil {
		return
	}
	var buf bytes.Buffer
	if view.Render(&buf, map[string]any{
		"Sections":    sections,
		"CurrentHref": currentHref,
		"OOB":         true,
	}) != nil {
		return
	}
	_, _ = w.Write(buf.Bytes())
}
