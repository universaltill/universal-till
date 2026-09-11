package pages

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// translationsPageSize bounds how many rows /ui/translations-table renders
// per request. web/locales/en.json alone is 2,258 keys (ut-docs#2014) and
// every row carries its own editable form, so rendering them all in one
// response built a multi-thousand-node DOM on first load AND on every
// search keystroke (translations.html debounces the search input, but each
// debounced request still re-rendered the whole set). The page now loads
// this many rows at a time and fetches more via infinite scroll as the
// merchant reaches the bottom.
const translationsPageSize = 100

// translationsMaxPageSize caps a client-supplied ?limit= (see renderTable) —
// the sentinel's own requests never send one (it always wants the server
// default), but the endpoint accepts an explicit limit too so a caller isn't
// locked to translationsPageSize. Bounded rather than unbounded so a crafted
// ?limit= can't force a full, multi-thousand-row render back onto the page
// this card exists to stop that from happening on.
const translationsMaxPageSize = 500

// registerTranslations wires the manager translation editor (docs repo:
// architecture/translation-editor.md): every visible string — built-in POS
// keys and plugin-shipped keys — editable per locale in one place. Shop
// overrides win over base locale files and plugin overlays; "reset" deletes
// the override so the string falls back.
func registerTranslations(mux *http.ServeMux, d *common.Deps, i18n *config.I18n) {
	repo := data.NewTranslationRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	// requireManager gates on the "settings" action (039's catalog) via
	// canPerform — see country_settings_page.go's identical
	// requireManager for why (ut-docs#901/#902): the old raw
	// IsManager() check never saw canPerform's UT_AUTH=off escape hatch,
	// so this page 403'd permanently under the dev/CI auth-bypass. No
	// change to gated (UT_AUTH on) behavior.
	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	// requirePageManager is requireManager's gate, answered as a full
	// RenderError page instead of a bare LocalizedError body — for the
	// /translations page route itself (ut-docs#1458, same fix class as
	// tables_page.go's requirePageManager/#1455). requireManager stays as-is
	// for /ui/translations-table and /api/translations/{set,clear}, which
	// are all htmx fragments (hx-get/hx-post, hx-swap="outerHTML") that
	// need the short body their JS callers expect.
	requirePageManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	// reload pushes the persisted overrides into the live translator so
	// edits apply immediately, no restart.
	reload := func(r *http.Request) {
		if overrides, err := repo.ListOverrides(r.Context()); err == nil {
			i18n.SetShopOverrides(overrides)
		}
	}

	type rowVM struct {
		Key, Value, Source, Reference string
	}
	// filteredEntries applies the search filter over the FULL key set
	// (ut-docs#2014 acceptance criteria: search must not be limited to
	// whatever page happens to be loaded already — that would silently
	// hide most matches) and returns it in the stable, already-sorted-by-
	// key order i18n.Entries itself guarantees, so slicing it into pages
	// is deterministic across requests.
	filteredEntries := func(editLocale, q string) []rowVM {
		entries := i18n.Entries(editLocale)
		q = strings.ToLower(strings.TrimSpace(q))
		rows := make([]rowVM, 0, len(entries))
		for _, e := range entries {
			if q != "" && !strings.Contains(strings.ToLower(e.Key), q) &&
				!strings.Contains(strings.ToLower(e.Value), q) &&
				!strings.Contains(strings.ToLower(e.Reference), q) {
				continue
			}
			rows = append(rows, rowVM(e))
		}
		return rows
	}

	// Keyset (cursor) paging, anchored on the last key seen on the previous
	// page (ut-docs#2019 — follow-up from the ut-docs#2014 review, which
	// shipped offset/limit paging with this as a documented, accepted
	// limitation). filteredEntries always returns entries sorted ascending
	// by Key (i18n.Entries' own guarantee — keys come from a map, so no
	// duplicates), so "the next page" can be expressed as "every entry
	// whose Key sorts after ?after=" instead of a numeric position. That
	// makes it immune to the set's LENGTH changing between rendering a
	// sentinel and the client following it (a plugin install/uninstall, or
	// a DIFFERENT manager's concurrent edit changing whether a key matches
	// q — renderRow deliberately ignores q, so an in-place edit by the SAME
	// manager never causes this): a key inserted or removed elsewhere in
	// the set shifts everyone's numeric position, but never moves anything
	// relative to an already-served key, so resuming after that key always
	// picks up exactly where the client left off — no skip, no duplicate.
	renderTable := func(w http.ResponseWriter, r *http.Request, editLocale, q string) {
		q = strings.TrimSpace(q)
		all := filteredEntries(editLocale, q)

		// Deliberately NOT strings.TrimSpace'd (independent review finding,
		// ut-docs#2019): unlike q/editLocale, this is a machine-generated
		// exact key round-tripped from a prior response's own
		// nextAfterEscaped, not user-typed input to tolerate stray
		// whitespace in. Trimming it would wedge paging into an infinite
		// loop for any key that itself has leading/trailing whitespace
		// (reachable — a plugin's locale overlay keys are taken verbatim
		// with no validation, internal/plugins/plugins.go): sort.Search
		// would then always find that same padded key as "the first key
		// after the trimmed cursor", so nextAfter never advances and the
		// revealed sentinel re-fires forever, appending a duplicate row
		// each time.
		after := r.URL.Query().Get("after")
		// sort.Search finds the first entry sorting strictly after `after`
		// — the first row of the next page. An `after` that no longer
		// exists in the current set (the concurrent-edit case above) still
		// lands in the right place, since the search is purely by sort
		// order, not by matching the key itself. `after == ""` searches
		// trivially to index 0 (every key sorts after ""), same as the old
		// offset=0 start.
		start := sort.Search(len(all), func(i int) bool { return all[i].Key > after })
		limit := translationsPageSize
		if v, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit"))); err == nil && v > 0 {
			// Clamp rather than ignore: a caller asking for more than the
			// cap still gets the largest page this endpoint will hand out
			// in one response, not a silent fallback to the default size
			// (that previously made an over-cap ?limit= indistinguishable
			// from no ?limit= at all — ut-docs#2014 review).
			if v > translationsMaxPageSize {
				v = translationsMaxPageSize
			}
			limit = v
		}
		end := start + limit
		if end > len(all) {
			end = len(all)
		}
		nextAfter := ""
		if end > start {
			nextAfter = all[end-1].Key
		}

		httpx.RenderPartial("ui/partials/translations_table.html", map[string]any{
			"editLocale": editLocale,
			// Pre-escaped for embedding in the sentinel row's own hx-get
			// query string (ut-docs#2014 review): a raw search query can
			// contain "&"/"=" and would otherwise corrupt that URL's other
			// params. html/template still HTML-escapes the attribute value
			// around it; url.QueryEscape's own output (alnum, -_.~%+ only)
			// has nothing left for that escaping to change, so the two
			// compose safely. nextAfterEscaped follows the same pattern —
			// a translator key is not expected to contain "&"/"=", but
			// nothing enforces that, so it's escaped exactly like q and
			// editLocale rather than assumed safe.
			"editLocaleEscaped": url.QueryEscape(editLocale),
			"q":                 q,
			"qEscaped":          url.QueryEscape(q),
			"rows":              all[start:end],
			"isAppend":          after != "",
			"hasMore":           end < len(all),
			"nextAfterEscaped":  url.QueryEscape(nextAfter),
			// Carried into the sentinel's own hx-get so a client-chosen
			// ?limit= stays consistent across every later page too, rather
			// than reverting to translationsPageSize from the second page
			// on (ut-docs#2014 review).
			"limit": limit,
		})(w, r)
	}

	// renderRow re-renders exactly the one row a save/clear just touched,
	// rather than the whole table (ut-docs#2014 acceptance criteria): with
	// paging, swapping the whole #translations-table on every save would
	// discard every page loaded beyond the first. The search filter does
	// NOT apply here — the row is already on screen and must be re-rendered
	// in place regardless of whether its new value still matches q. Reuses
	// translations_table.html itself (rowOnly:true suppresses the table
	// wrapper AND the sentinel/end-of-list row) rather than a second
	// template file with the same row markup copied into it — httpx.
	// RenderPartial parses one file per call, so a `{{ template }}` call
	// from a second file couldn't reach a `define` living in this one
	// (ut-docs#2014 review), and a hand-duplicated copy across two files
	// has nothing to stop it silently drifting from the original.
	renderRow := func(w http.ResponseWriter, r *http.Request, editLocale, q, key string) {
		rows := []rowVM{}
		for _, e := range i18n.Entries(editLocale) {
			if e.Key == key {
				rows = append(rows, rowVM(e))
				break
			}
		}
		// A key that vanished from the live entry set between the request
		// and this lookup (e.g. a plugin was uninstalled concurrently)
		// renders zero rows here — same as before, the row's own DOM node
		// is simply removed by the outerHTML swap on the client rather than
		// replaced.
		httpx.RenderPartial("ui/partials/translations_table.html", map[string]any{
			"editLocale": editLocale,
			"q":          strings.TrimSpace(q),
			"rows":       rows,
			"isAppend":   true,
			"hasMore":    false,
			"rowOnly":    true,
		})(w, r)
	}

	mux.HandleFunc("GET /translations", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requirePageManager(w, r); !ok {
			return
		}
		editLocale := strings.TrimSpace(r.URL.Query().Get("edit_locale"))
		if editLocale == "" {
			editLocale = httpx.ResolveLocale(w, r)
		}
		// ut-docs#2116: renders inside the /admin two-pane shell (tree +
		// panel) rather than as its own standalone page — see
		// admin_page.go's renderAdminDestination. Independent of, and
		// unaffected by, this page's OWN pre-existing hx-get="/ui/
		// translations-table" fragment mechanism (its search/filter UI,
		// wired below) — that swap targets an element inside this page's
		// own content, not #admin-panel, so both keep working side by
		// side.
		renderAdminDestination(d, "/translations", "ui/pages/translations.html", map[string]any{
			"title":      "Translations",
			"theme":      d.CurrentState().Theme,
			"menuItems":  d.MenuSnapshot(),
			"editLocale": editLocale,
			"locales":    i18n.Available(),
		})(w, r)
	})

	mux.HandleFunc("GET /ui/translations-table", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		renderTable(w, r, strings.TrimSpace(r.URL.Query().Get("edit_locale")), r.URL.Query().Get("q"))
	})

	mux.HandleFunc("POST /api/translations/set", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		_ = r.ParseForm()
		editLocale := strings.TrimSpace(r.Form.Get("edit_locale"))
		key := strings.TrimSpace(r.Form.Get("key"))
		value := r.Form.Get("value")
		if editLocale == "" || key == "" || strings.TrimSpace(value) == "" {
			http.Error(w, "edit_locale, key and value required", http.StatusBadRequest)
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		if err := repo.SetOverride(r.Context(), editLocale, key, value, actor.ID, now); err != nil {
			http.Error(w, "could not save override", http.StatusInternalServerError)
			return
		}
		reload(r)
		_ = posRepo.InsertAudit(r.Context(), nil, actor.ID, "translation", editLocale+"/"+key,
			"translation_override_set", map[string]string{"locale": editLocale, "key": key}, now, "")
		renderRow(w, r, editLocale, r.Form.Get("q"), key)
	})

	mux.HandleFunc("POST /api/translations/clear", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		_ = r.ParseForm()
		editLocale := strings.TrimSpace(r.Form.Get("edit_locale"))
		key := strings.TrimSpace(r.Form.Get("key"))
		if editLocale == "" || key == "" {
			http.Error(w, "edit_locale and key required", http.StatusBadRequest)
			return
		}
		if _, err := repo.ClearOverride(r.Context(), editLocale, key); err != nil {
			http.Error(w, "could not clear override", http.StatusInternalServerError)
			return
		}
		reload(r)
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actor.ID, "translation", editLocale+"/"+key,
			"translation_override_cleared", map[string]string{"locale": editLocale, "key": key}, now, "")
		renderRow(w, r, editLocale, r.Form.Get("q"), key)
	})
}
