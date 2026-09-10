package pages

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// translation_overrides isn't in the simplified seedForPages schema, so
// (like sync_admin_test.go's newMigratedSyncDeps) these use a real migrated
// database.
func newTranslationsTestDeps(t *testing.T) (*http.ServeMux, *common.Deps, *config.I18n) {
	t.Helper()
	chdirRoot(t)
	d, err := appdb.Open(filepath.Join(t.TempDir(), "translations.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20},
	}
	pm, err := plugins.Init(t.Context(), cfg, d.DB)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(d.DB), cfg)
	// audit_log.actor_id has a real FK to users(id) in the migrated schema;
	// seed the manager withManager() attaches so InsertAudit doesn't fail
	// its FK check (silently, since callers do `_ = posRepo.InsertAudit(...)`).
	if _, err := d.DB.ExecContext(t.Context(), `
INSERT INTO users(id, username, display_name, role, is_active) VALUES('mgr-1', 'manager', 'Manager', 'manager', 1)`); err != nil {
		t.Fatalf("seed manager user: %v", err)
	}
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       d.DB,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/settings", Label: "Settings"}},
		Pm:       pm,
		Settings: settings.NewStore(d.DB),
		AuthSvc:  auth.NewService(d.DB),
	}
	mux := http.NewServeMux()
	registerTranslations(mux, dp, i18n)
	return mux, dp, i18n
}

// ut-docs#902: GET /translations must be reachable under UT_AUTH=off with
// no session — same fix and rationale as
// country_settings_page_test.go's TestCountrySettingsPage_ReachableUnderAuthOff
// (ut-docs#901's precedent).
func TestTranslationsPage_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newTranslationsTestDeps(t)

	req := httptest.NewRequest(http.MethodGet, "/translations?edit_locale=en", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /translations under UT_AUTH=off = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// Mutating handlers, not just the GET page, must also pick up canPerform's
// UT_AUTH=off bypass — independent review finding on ut-docs#901, applied
// here too.
func TestTranslationsPageSet_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newTranslationsTestDeps(t)

	rec := postForm(mux, "/api/translations/set", url.Values{
		"edit_locale": {"en"}, "key": {"nav.help"}, "value": {"Auth-off override"},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("set under UT_AUTH=off: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func withManager(req *http.Request) *http.Request {
	return auth.WithUser(req, auth.User{ID: "mgr-1", Username: "manager", Role: "manager"})
}

func TestRegisterTranslations_GET_RequiresManager(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/translations", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
	// ut-docs#1458: GET /translations must render the full layout on 403
	// too, not a bare rail-less body (same fix class as #1455) — the
	// page's own gate only, per requirePageManager; the fragment routes
	// (/ui/translations-table, /api/translations/*) keep the short
	// LocalizedError body their htmx callers expect.
	if body := rec.Body.String(); !strings.Contains(body, `class="nav"`) {
		t.Fatalf("403 on GET /translations has no nav rail:\n%s", body)
	}
}

func TestRegisterTranslations_GET_RendersAvailableLocales(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	req := withManager(httptest.NewRequest(http.MethodGet, "/translations?edit_locale=en", nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /translations: code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "en") {
		t.Fatalf("expected the en locale referenced in the page")
	}
}

func TestTranslationsTable_FiltersByQuery(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	// A key that certainly exists and is actually rendered somewhere
	// (sync_chip.html) — ut-docs#1759 removed sync.chip_queued (the bare
	// form this test used to use) as a dead key never referenced by any
	// template, so it can no longer stand in for "a key that exists".
	req := withManager(httptest.NewRequest(http.MethodGet, "/ui/translations-table?edit_locale=en&q=sync.chip_offline", nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/translations-table: code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sync.chip_offline") {
		t.Fatalf("expected the matching key in the filtered table, got %q", rec.Body.String())
	}

	req = withManager(httptest.NewRequest(http.MethodGet, "/ui/translations-table?edit_locale=en&q=zzz-no-such-key-zzz", nil))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/translations-table (no match): code %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sync.chip_offline") {
		t.Fatalf("expected the query to filter out non-matching keys")
	}
}

// ut-docs#2014: /translations rendered all ~2,258 keys (en.json's own
// count) in one response, and every search keystroke re-rendered the whole
// set. /ui/translations-table now pages at translationsPageSize and loads
// more via infinite scroll (a sentinel <tr> the client reveals to fetch the
// next offset).
func TestTranslationsTable_FirstPageIsBoundedWithSentinel(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	// No query: en.json alone is ~2,258 keys, comfortably more than one
	// page, so the first page must stop at translationsPageSize and offer
	// a sentinel for the next one rather than rendering everything.
	req := withManager(httptest.NewRequest(http.MethodGet, "/ui/translations-table?edit_locale=en", nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/translations-table: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, `<div id="translations-table">`) {
		t.Fatalf("first page (offset=0) must render the full table wrapper:\n%s", body)
	}
	if got := strings.Count(body, `<tr id="translations-row-`); got != translationsPageSize {
		t.Fatalf("expected exactly %d rows on the first page, got %d", translationsPageSize, got)
	}
	if !strings.Contains(body, `id="translations-sentinel"`) {
		t.Fatalf("expected a sentinel row when more than one page of results exists:\n%s", body)
	}
	wantNext := `offset=` + strconv.Itoa(translationsPageSize)
	if !strings.Contains(body, wantNext) {
		t.Fatalf("expected the sentinel's hx-get to request the next offset (%s):\n%s", wantNext, body)
	}
	if strings.Contains(body, "translations-end") {
		t.Fatalf("did not expect the end-of-list marker on a page with more results:\n%s", body)
	}
}

// The sentinel's own request (offset > 0) must return ONLY the next page's
// rows (plus a new sentinel, or the end marker) — not a second copy of the
// table wrapper, or htmx's outerHTML swap on the sentinel <tr> would nest a
// <div>/<table> inside a <tbody>.
func TestTranslationsTable_AppendPageHasNoOuterWrapper(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	req := withManager(httptest.NewRequest(http.MethodGet,
		"/ui/translations-table?edit_locale=en&offset="+strconv.Itoa(translationsPageSize), nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/translations-table (append): code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, `<div id="translations-table">`) || strings.Contains(body, `<table class="translations-grid">`) {
		t.Fatalf("an appended page must not repeat the table wrapper:\n%s", body)
	}
	if got := strings.Count(body, `<tr id="translations-row-`); got == 0 {
		t.Fatalf("expected at least one row on the second page:\n%s", body)
	}
}

// A ?limit= above translationsMaxPageSize must clamp to the cap, not
// silently fall back to the default page size (the bug this test guards
// against: an early version of the clamp rejected any out-of-range value
// instead of capping it, so ?limit=3000 was indistinguishable from no
// ?limit= at all).
func TestTranslationsTable_LimitClampsToMaxNotDefault(t *testing.T) {
	mux, _, i18n := newTranslationsTestDeps(t)
	total := len(i18n.Entries("en"))
	if total <= translationsMaxPageSize {
		t.Fatalf("test assumes en.json has more than %d keys (has %d) so the cap is actually exercised", translationsMaxPageSize, total)
	}
	req := withManager(httptest.NewRequest(http.MethodGet, "/ui/translations-table?edit_locale=en&limit=999999", nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/translations-table: code %d body %s", rec.Code, rec.Body.String())
	}
	if got := strings.Count(rec.Body.String(), `<tr id="translations-row-`); got != translationsMaxPageSize {
		t.Fatalf("expected an over-cap ?limit= to clamp to %d rows, got %d", translationsMaxPageSize, got)
	}
}

// A filtered result smaller than one page must show the end-of-list marker
// (an infinite list needs a real end state, ut-docs#2014's own acceptance
// criteria) and no sentinel — there is nothing more to load.
func TestTranslationsTable_ShortResultShowsEndOfListNotSentinel(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	req := withManager(httptest.NewRequest(http.MethodGet, "/ui/translations-table?edit_locale=en&q=sync.chip_offline", nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/translations-table: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `id="translations-sentinel"`) {
		t.Fatalf("did not expect a sentinel row once every match fits on one page:\n%s", body)
	}
	if !strings.Contains(body, `translations-end`) {
		t.Fatalf("expected the end-of-list marker once every match fits on one page:\n%s", body)
	}
}

// A search matching nothing must fall back to the existing empty-state row,
// never the end-of-list marker (which implies at least one real result) and
// never a sentinel (there is nothing to page into).
func TestTranslationsTable_EmptyResultShowsEmptyMessageNotEndOfList(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	req := withManager(httptest.NewRequest(http.MethodGet, "/ui/translations-table?edit_locale=en&q=zzz-no-such-key-zzz", nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/translations-table: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No matching texts") {
		t.Fatalf("expected the empty-state message for a query with no matches:\n%s", body)
	}
	if strings.Contains(body, `id="translations-sentinel"`) || strings.Contains(body, "translations-end") {
		t.Fatalf("did not expect a sentinel or end-of-list marker for a zero-result query:\n%s", body)
	}
}

// The real last page of the FULL (unfiltered) key set — not just a short
// filtered result — must also end cleanly: no sentinel past the true end,
// and the end-of-list marker present.
func TestTranslationsTable_TrueLastPageEndsCleanly(t *testing.T) {
	mux, _, i18n := newTranslationsTestDeps(t)
	total := len(i18n.Entries("en"))
	lastPageOffset := (total / translationsPageSize) * translationsPageSize
	if lastPageOffset == total { // total is an exact multiple of the page size
		lastPageOffset -= translationsPageSize
	}

	req := withManager(httptest.NewRequest(http.MethodGet,
		"/ui/translations-table?edit_locale=en&offset="+strconv.Itoa(lastPageOffset), nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/translations-table (last page): code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `id="translations-sentinel"`) {
		t.Fatalf("did not expect a sentinel on the true last page (offset=%d of %d total):\n%s", lastPageOffset, total, body)
	}
	if !strings.Contains(body, "translations-end") {
		t.Fatalf("expected the end-of-list marker on the true last page:\n%s", body)
	}
	if got, want := strings.Count(body, `<tr id="translations-row-`), total-lastPageOffset; got != want {
		t.Fatalf("expected %d rows on the true last page, got %d", want, got)
	}
}

// An ?offset= past the total must clamp to the end (empty page, no
// sentinel) rather than panic on an out-of-range slice bound — independent
// review finding, ut-docs#2014: the clamp at translations_page.go is the
// only thing standing between this request and a 500.
func TestTranslationsTable_OffsetBeyondTotalClampsInsteadOfPanicking(t *testing.T) {
	mux, _, i18n := newTranslationsTestDeps(t)
	total := len(i18n.Entries("en"))
	req := withManager(httptest.NewRequest(http.MethodGet,
		"/ui/translations-table?edit_locale=en&offset="+strconv.Itoa(total+999999), nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/translations-table (offset far past total): code %d body %s", rec.Code, rec.Body.String())
	}
}

// An append request whose result set shrank to zero between the sentinel's
// render and the client following it (a key removed concurrently — e.g. a
// plugin uninstalled, or another manager clearing an override-only key)
// must still show the end-of-list marker, not silently render nothing —
// independent review finding, ut-docs#2014: "an infinite list needs a real
// end state" applies here too, not just to the true last page reached by
// normal scrolling.
func TestTranslationsTable_AppendWithNoRowsStillShowsEndOfList(t *testing.T) {
	mux, _, i18n := newTranslationsTestDeps(t)
	total := len(i18n.Entries("en"))
	// offset == total: a real append request (offset > 0) whose page is
	// empty because there is nothing left, distinct from the "no query
	// match at all" case (offset 0) which correctly shows the empty-state
	// message instead.
	req := withManager(httptest.NewRequest(http.MethodGet,
		"/ui/translations-table?edit_locale=en&offset="+strconv.Itoa(total), nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/translations-table (empty append page): code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `id="translations-sentinel"`) {
		t.Fatalf("did not expect a sentinel once the set is exhausted:\n%s", body)
	}
	if !strings.Contains(body, "translations-end") {
		t.Fatalf("expected the end-of-list marker even on an empty append page (nothing left to load):\n%s", body)
	}
}

// The sentinel's own hx-get URL must round-trip a search query containing
// URL/HTML-significant characters without corrupting the URL's other query
// params or breaking out of the attribute — independent review finding,
// ut-docs#2014: the escaping itself was verified correct, but nothing in
// the suite exercised it before this.
func TestTranslationsTable_SentinelURLEscapesAdversarialQuery(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	const q = `a&edit_locale=x&"'<script>`
	req := withManager(httptest.NewRequest(http.MethodGet,
		"/ui/translations-table?edit_locale=en&q="+url.QueryEscape(q), nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/translations-table: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// This particular query matches nothing, so there's no sentinel to
	// inspect on THIS response — what matters is that the query string
	// itself, and the rest of the page, came through unharmed: no raw
	// "<script>" reached the output, and the edit_locale select still
	// shows only the real "en" option this request actually asked for
	// (a broken query-string split would smuggle a second, attacker-
	// controlled edit_locale param into view).
	if strings.Contains(body, "<script>") {
		t.Fatalf("expected the adversarial query to be escaped, not reach the output raw:\n%s", body)
	}

	// Exercise the sentinel path directly with a query that both (a) is
	// genuinely adversarial for URL-embedding and (b) actually matches
	// real content, so hasMore is real and a sentinel exists to inspect —
	// a search string with no real match (like the one above) never
	// reaches the sentinel-building code path at all. A bare "&" matches
	// several real en.json values ("Help & Support", "Confirm & Import",
	// …), so it's both realistic (a merchant could plausibly search for
	// one of those) and the single most dangerous character for this
	// query string.
	const matchingAdversarialQ = `&`
	req = withManager(httptest.NewRequest(http.MethodGet,
		"/ui/translations-table?edit_locale=en&q="+url.QueryEscape(matchingAdversarialQ)+"&limit=1", nil))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/translations-table: code %d body %s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if !strings.Contains(body, `id="translations-sentinel"`) {
		t.Fatalf("expected a sentinel: q=%q (limit=1) should match several real en.json values (\"Help & Support\" etc.) — got:\n%s", matchingAdversarialQ, body)
	}
	// The sentinel's hx-get must carry exactly ONE offset param and ONE
	// limit param — if q's own "&" leaked out of its escaped slot, it
	// would split the query string and a stray/duplicate param would
	// appear alongside these.
	if got := strings.Count(body, "offset="); got != 1 {
		t.Fatalf("expected exactly one offset= in the sentinel URL (q's own \"&\" must not have split the query string), got %d:\n%s", got, body)
	}
	if got := strings.Count(body, "limit="); got != 1 {
		t.Fatalf("expected exactly one limit= in the sentinel URL, got %d:\n%s", got, body)
	}
	// Follow the sentinel's own URL for real and confirm the server parses
	// it back to the SAME q — proving the round trip, not just counting
	// params.
	start := strings.Index(body, `hx-get="`) + len(`hx-get="`)
	end := strings.Index(body[start:], `"`)
	sentinelURL := html.UnescapeString(body[start : start+end])
	req2 := withManager(httptest.NewRequest(http.MethodGet, sentinelURL, nil))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("following the sentinel's own URL: code %d body %s", rec2.Code, rec2.Body.String())
	}
	parsed, err := url.Parse(sentinelURL)
	if err != nil {
		t.Fatalf("sentinel URL did not parse: %v (%q)", err, sentinelURL)
	}
	if got := parsed.Query().Get("q"); got != matchingAdversarialQ {
		t.Fatalf("sentinel URL's q did not round-trip: got %q, want %q (raw URL: %s)", got, matchingAdversarialQ, sentinelURL)
	}
}

// Regression for ut-docs#2014: before this, saving/clearing an override
// swapped the ENTIRE #translations-table (hx-swap="outerHTML" targeting the
// table), which would silently discard every page loaded beyond the first.
// The handler must now re-render only the one row that changed.
func TestTranslationsSet_ReturnsOnlyTheEditedRowNotTheWholeTable(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	form := url.Values{"edit_locale": {"en"}, "key": {"nav.home"}, "value": {"Custom Home Label"}}
	req := withManager(httptest.NewRequest(http.MethodPost, "/api/translations/set", strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/translations/set: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="translations-row-nav.home"`) {
		t.Fatalf("expected the edited row in the response:\n%s", body)
	}
	if strings.Contains(body, `<div id="translations-table">`) || strings.Contains(body, `<table class="translations-grid">`) {
		t.Fatalf("expected only the single edited row, not the whole table (would discard other loaded pages client-side):\n%s", body)
	}
	if got := strings.Count(body, "<tr"); got != 1 {
		t.Fatalf("expected exactly one <tr> in the response, got %d:\n%s", got, body)
	}
}

func TestTranslationsClear_ReturnsOnlyTheEditedRowNotTheWholeTable(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	setForm := url.Values{"edit_locale": {"en"}, "key": {"nav.home"}, "value": {"Temporary Override"}}
	req := withManager(httptest.NewRequest(http.MethodPost, "/api/translations/set", strings.NewReader(setForm.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	clearForm := url.Values{"edit_locale": {"en"}, "key": {"nav.home"}}
	req = withManager(httptest.NewRequest(http.MethodPost, "/api/translations/clear", strings.NewReader(clearForm.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/translations/clear: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="translations-row-nav.home"`) {
		t.Fatalf("expected the cleared row in the response:\n%s", body)
	}
	if strings.Contains(body, `<div id="translations-table">`) || strings.Contains(body, `<table class="translations-grid">`) {
		t.Fatalf("expected only the single cleared row, not the whole table:\n%s", body)
	}
}

func TestTranslationsSet_RequiresManager(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	form := url.Values{"edit_locale": {"en"}, "key": {"sync.chip_offline"}, "value": {"Offline"}}
	req := httptest.NewRequest(http.MethodPost, "/api/translations/set", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

func TestTranslationsSet_ValidatesRequiredFields(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	cases := []url.Values{
		{"edit_locale": {""}, "key": {"k"}, "value": {"v"}},
		{"edit_locale": {"en"}, "key": {""}, "value": {"v"}},
		{"edit_locale": {"en"}, "key": {"k"}, "value": {"  "}},
	}
	for _, form := range cases {
		req := withManager(httptest.NewRequest(http.MethodPost, "/api/translations/set", strings.NewReader(form.Encode())))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("form %+v: expected 400, got %d", form, rec.Code)
		}
	}
}

func TestTranslationsSet_PersistsReloadsLiveTranslatorAndAudits(t *testing.T) {
	mux, dp, i18n := newTranslationsTestDeps(t)
	form := url.Values{"edit_locale": {"en"}, "key": {"nav.home"}, "value": {"Custom Home Label"}}
	req := withManager(httptest.NewRequest(http.MethodPost, "/api/translations/set", strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/translations/set: code %d body %s", rec.Code, rec.Body.String())
	}

	// The live translator must reflect the override immediately, no restart.
	found := false
	for _, e := range i18n.Entries("en") {
		if e.Key == "nav.home" {
			found = true
			if e.Value != "Custom Home Label" || e.Source != "shop" {
				t.Fatalf("expected nav.home overridden to 'Custom Home Label' (source=shop), got %+v", e)
			}
		}
	}
	if !found {
		t.Fatalf("expected nav.home present in the live entries after the override")
	}

	var auditCount int
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_log WHERE action = 'translation_override_set'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one translation_override_set audit row, got %d", auditCount)
	}
}

func TestTranslationsClear_RemovesOverrideReloadsAndAudits(t *testing.T) {
	mux, dp, i18n := newTranslationsTestDeps(t)

	setForm := url.Values{"edit_locale": {"en"}, "key": {"nav.home"}, "value": {"Temporary Override"}}
	req := withManager(httptest.NewRequest(http.MethodPost, "/api/translations/set", strings.NewReader(setForm.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	clearForm := url.Values{"edit_locale": {"en"}, "key": {"nav.home"}}
	req = withManager(httptest.NewRequest(http.MethodPost, "/api/translations/clear", strings.NewReader(clearForm.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/translations/clear: code %d body %s", rec.Code, rec.Body.String())
	}

	for _, e := range i18n.Entries("en") {
		if e.Key == "nav.home" && e.Source == "shop" {
			t.Fatalf("expected the override cleared (source should fall back to base), got %+v", e)
		}
	}

	var auditCount int
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_log WHERE action = 'translation_override_cleared'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one translation_override_cleared audit row, got %d", auditCount)
	}
}

func TestTranslationsClear_RequiresManager(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	form := url.Values{"edit_locale": {"en"}, "key": {"nav.home"}}
	req := httptest.NewRequest(http.MethodPost, "/api/translations/clear", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}
