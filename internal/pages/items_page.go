package pages

import (
	"html/template"
	"net/http"
	"net/http/httptest"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pages/itemsnav"
)

// registerItemsPage wires GET /items — the section-list landing page the
// top-level "Items" nav tile opens (ut-docs#1897), rebuilt as a two-pane
// master-detail screen by ut-docs#1950 (product-owner feedback: "make it
// like sumup" — a compact section list on the left, the selected section's
// own content on the right, in the same screen). The section list itself
// (name/subtitle/href per row) lives in internal/pages/itemsnav now, not
// here — see that package's doc comment for why: three of the five section
// handlers live in internal/pages/catalog, which cannot import this
// package (internal/pages already imports internal/pages/catalog), so the
// list has to live somewhere both can reach. Static content, no DB access
// of its own.
func registerItemsPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/items", func(w http.ResponseWriter, r *http.Request) {
		// RequestLocale, not ResolveLocale: Render resolves (and sets the
		// ?lang= cookie) itself below; resolving twice would emit the
		// cookie twice (same reasoning as menu_page.go's registerMenu).
		locale := httpx.RequestLocale(r)
		sections := itemsnav.Resolve(locale, d.ItemsAmendmentsSnapshot())
		// Defensive, not reachable via any amendment a plugin can install
		// today (the Items slot refuses `hide` at parse time — see
		// itemsSpec's own comment — so uislot.CoreItems' five rows can
		// never drop to zero through Resolve). Kept anyway: the old code
		// indexed a statically non-empty package var, and this handler
		// must never panic just because a future capability widening or a
		// bug elsewhere left the resolved list empty (independent review
		// of ut-docs#1911).
		if len(sections) == 0 {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", nil)
			return
		}
		// AC #3: the first section (Library/Catalog, or whatever a `layout`
		// plugin amendment reordered to the front) is selected by default —
		// the right panel is never empty on a bare /items load.
		current := sections[0].Href
		data := map[string]any{
			"title":       "Items",
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.MenuSnapshot(),
			"Sections":    sections,
			"CurrentHref": current,
			"PanelHTML":   embedItemsSection(mux, r, current, locale),
		}
		httpx.Render("ui/pages/items.html", data)(w, r)
	})
}

// embedItemsSection replays r as a GET to href with "HX-Request: true" set,
// through the SAME mux this handler is registered on, and returns the htmx
// fragment body that handler renders — reusing its exact data-fetch and
// template logic instead of duplicating any of it here, which would drift
// the moment that handler's own query/columns changed. Same in-process
// sub-request idiom as import_stage.go's commitStagedImportForSetup
// (ut-docs#1168): mux is the raw, not-yet-auth-wrapped mux app.Init passes
// to every registerXxxPage call, so the caller's own session (r's context)
// is carried over explicitly via auth.WithUser rather than relying on
// middleware that this call bypasses. Locale/theme/other cookies ride along
// unchanged via AddCookie, so the embedded content matches what a direct
// visit to href would render for this same user — EXCEPT ut_lang, which is
// set explicitly from locale (the caller's already-resolved
// httpx.RequestLocale(r)) rather than copied: href carries no ?lang= of its
// own, so on a first-ever ?lang= visit — before Render's own ResolveLocale
// call has had a chance to write the ut_lang cookie — copying r's cookies
// alone would leave the sub-request with no locale signal at all, and it
// would silently fall back to the default locale instead of inheriting
// fa/whatever was actually requested (ut-docs#2114).
//
// On any non-200 (a permission gate this user doesn't clear, a DB error),
// logs the real status/body server-side (same logging.L().Errorf pattern as
// import_stage.go's commitStagedImportForSetup) and returns a translated,
// visible fallback card instead of silently rendering nothing — ux-
// guidelines.md is explicit that no user-facing action gets a silent
// failure, and AC #3 ("the right panel is never empty on arrival") would
// otherwise fail exactly when something is actually wrong. The section is
// still reachable directly at its own URL either way.
func embedItemsSection(mux *http.ServeMux, r *http.Request, href, locale string) template.HTML {
	sub := httptest.NewRequest(http.MethodGet, href, nil).WithContext(r.Context())
	sub.Header.Set("HX-Request", "true")
	// This body is INLINED into /items, which draws the rail itself just
	// above the panel — so the section handler must skip its out-of-band
	// rail copy here (review of ut-docs#1950: without this a bare GET /items
	// emitted a duplicate id="items-rail" and painted the whole section list
	// a second time inside the right panel). See itemsnav.EmbedHeader.
	sub.Header.Set(itemsnav.EmbedHeader, "1")
	for _, c := range r.Cookies() {
		if c.Name == "ut_lang" {
			continue // set explicitly below from the already-resolved locale
		}
		sub.AddCookie(c)
	}
	sub.AddCookie(&http.Cookie{Name: "ut_lang", Value: locale})
	if u, ok := auth.FromContext(r.Context()); ok {
		sub = auth.WithUser(sub, u)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, sub)
	if rec.Code != http.StatusOK {
		logging.L().Errorf("items panel: embedding %s failed (code %d): %s", href, rec.Code, rec.Body.String())
		msg := template.HTMLEscapeString(httpx.T(locale, "common.error.server"))
		return template.HTML(`<div class="card" style="color: var(--danger)">` + msg + `</div>`) //nolint:gosec // msg is HTML-escaped above; the surrounding markup is a fixed literal
	}
	return template.HTML(rec.Body.String()) //nolint:gosec // rec.Body is this same server's own rendered HTML for an authorized request, not user input
}
