package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

func helpMux(t *testing.T) *http.ServeMux {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerHelp(mux, dp)
	return mux
}

func get(t *testing.T, mux *http.ServeMux, path string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The manual's index renders the topic tree and the search box — the two
// halves of the reading experience the product owner asked for.
func TestHelpIndexRendersTreeAndSearch(t *testing.T) {
	rec := get(t, helpMux(t), "/help")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /help: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`id="manual"`, `class="manual-nav"`, `id="manual-q"`, `id="manual-panel"`} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %q", want)
		}
	}
	// Sections from the topic front matter, not a hardcoded Go list.
	for _, want := range []string{"Everyday selling", "Setting up your shop", "Running the business"} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing section %q", want)
		}
	}
	if !strings.Contains(body, `href="/help/sell"`) {
		t.Error("index has no link to the selling topic")
	}
	if strings.Contains(body, "help.") && strings.Contains(body, "help.search") {
		t.Error("an unresolved locale key leaked into the page")
	}
}

func TestHelpTopicRendersMarkdown(t *testing.T) {
	rec := get(t, helpMux(t), "/help/sell")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /help/sell: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-topic="sell"`) {
		t.Error("topic article missing")
	}
	if !strings.Contains(body, "<h1>") || !strings.Contains(body, "<ol>") {
		t.Errorf("markdown not rendered as HTML: %s", body)
	}
	// A directly-loaded topic still renders the whole two-pane page, so the
	// URL is shareable and survives a cold load.
	if !strings.Contains(body, `class="manual-nav"`) {
		t.Error("direct topic load did not render the shell")
	}
}

func TestHelpTopicHTMXReturnsFragmentOnly(t *testing.T) {
	rec := get(t, helpMux(t), "/help/catalog", "HX-Request", "true")
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx GET: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-topic="catalog"`) {
		t.Error("fragment missing the topic")
	}
	if strings.Contains(body, `class="manual-nav"`) || strings.Contains(body, "<html") {
		t.Error("htmx request re-rendered the whole shell")
	}
}

// ut-docs#351: an htmx topic swap only replaces #manual-panel, so without an
// out-of-band update the tree's is-current/aria-current markup would stay on
// whichever topic was current at the last full page load. The fragment must
// carry a second, OOB-swapped copy of the tree with the highlight moved.
func TestHelpTopicHTMXCarriesOOBNavWithMovedHighlight(t *testing.T) {
	rec := get(t, helpMux(t), "/help/catalog", "HX-Request", "true")
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx GET: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="manual-tree"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("fragment missing the OOB tree swap: %s", body)
	}
	if !strings.Contains(body, `href="/help/catalog"`) {
		t.Fatal("OOB tree missing a link to the current topic")
	}
	// The OOB tree's own markup must mark catalog SPECIFICALLY current, and
	// exactly one topic — never a stale highlight left on whatever was
	// current before, and never just "some" link lighting up regardless of
	// which topic it's anchored to.
	navStart := strings.Index(body, `id="manual-tree"`)
	nav := body[navStart:]
	if got := strings.Count(nav, "is-current"); got != 1 {
		t.Errorf("OOB tree has %d is-current links, want exactly 1: %s", got, nav)
	}
	catalogIdx := strings.Index(nav, `href="/help/catalog"`)
	if catalogIdx < 0 {
		t.Fatalf("catalog link not found in OOB tree: %s", nav)
	}
	catalogLink := nav[catalogIdx:]
	catalogTag := catalogLink[:strings.Index(catalogLink, ">")+1]
	if !strings.Contains(catalogTag, `class="manual-link is-current"`) {
		t.Errorf("catalog's own link is not marked is-current: %s", catalogTag)
	}
	if !strings.Contains(catalogTag, `aria-current="page"`) {
		t.Errorf("catalog's own link is missing aria-current: %s", catalogTag)
	}
}

// A stale "?" link must be visible as a 404, not silently swallowed into the
// index — otherwise a broken help link looks like a working one.
func TestHelpUnknownTopicIs404(t *testing.T) {
	if rec := get(t, helpMux(t), "/help/no-such-topic"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown topic: %d, want 404", rec.Code)
	}
}

func TestHelpSearchFindsTopics(t *testing.T) {
	rec := get(t, helpMux(t), "/help/search?q=barcode")
	if rec.Code != http.StatusOK {
		t.Fatalf("search: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/help/catalog") {
		t.Errorf("search for 'barcode' did not surface the catalog topic: %s", body)
	}
	if strings.Contains(body, `class="manual-nav"`) {
		t.Error("search returned the whole shell instead of a fragment")
	}
}

func TestHelpSearchNoResultsIsFriendly(t *testing.T) {
	rec := get(t, helpMux(t), "/help/search?q=zzzznotathing")
	body := rec.Body.String()
	if !strings.Contains(body, "manual-noresults") {
		t.Errorf("no-results state missing: %s", body)
	}
	if strings.Contains(body, "help.search.none") {
		t.Error("unresolved locale key in the no-results message")
	}
}

// Every topic the tree lists must actually resolve — this is the cheap
// version of the route guard, catching a topic that got renamed but stayed
// linked.
func TestEveryTopicResolves(t *testing.T) {
	mux := helpMux(t)
	l, err := Library()
	if err != nil {
		t.Fatalf("library: %v", err)
	}
	for _, id := range l.IDs() {
		if rec := get(t, mux, "/help/"+id); rec.Code != http.StatusOK {
			t.Errorf("/help/%s: %d", id, rec.Code)
		}
	}
}

// The manual has to be complete in every locale the till ships, per the
// product's multilingual rule. When it isn't, the page says so rather than
// showing a blank — but for the migrated set it should be complete.
func TestManualIsTranslatedInEveryShippedLocale(t *testing.T) {
	chdirRoot(t)
	l, err := Library()
	if err != nil {
		t.Fatalf("library: %v", err)
	}
	for _, loc := range []string{"fa", "ar", "tr"} {
		if missing := l.MissingTranslations(loc); len(missing) > 0 {
			t.Errorf("locale %s is missing manual topics: %v", loc, missing)
		}
	}
}

// Routes declared in front matter are what the contextual "?" resolves
// through, so the mapping has to hold for the pages that have one today.
func TestRouteRegistryResolvesKnownPages(t *testing.T) {
	l, err := Library()
	if err != nil {
		t.Fatalf("library: %v", err)
	}
	for route, want := range map[string]string{
		"/":             "sell",
		"/catalog":      "catalog",
		"/inventory":    "inventory",
		"/reports":      "reports",
		"/plugins":      "plugins",
		"/users":        "users",
		"/backoffice":   "alerts",
		"/menu":         "menu",
		"/translations": "translations",
		// /self-order and /self-order/shop are deliberately NOT claimed here
		// (ut-docs#326 review): those pages render via RenderPartial with no
		// base layout/nav, so they carry no "?" to resolve in the first
		// place — a routes: claim would make the guard green while
		// documenting a link that can never appear on screen. The
		// self-order topic still exists (reachable via manual search), just
		// routeless, same as quickstart.
		//
		// Parameterized pages: the front matter declares /invoice/{display_no}
		// etc., and a real concrete path must resolve through the segment-wise
		// matcher, not just the literal pattern string.
		"/invoice/INV-2026-0001":  "invoices",
		"/journal/R-0001":         "reports",
		"/refund/R-0001":          "sell",
		"/plugins/faq-1/settings": "plugins",
	} {
		got, ok := l.TopicForRoute(route)
		if !ok || got != want {
			t.Errorf("TopicForRoute(%q) = %q,%v; want %q", route, got, ok, want)
		}
	}
}

// Help has always been a route through to the plugin store (an installed
// plugin can contribute help content). Replacing the old accordion page
// dropped that link, which two specs in tests/e2e/ — a suite separate from
// e2e/ — assert is reachable from Menu → Help. CI caught it; this keeps the
// Go suite honest about it too.
func TestHelpIndexKeepsPluginEntryPoint(t *testing.T) {
	body := get(t, helpMux(t), "/help").Body.String()
	if !strings.Contains(body, `data-testid="plugin-faq-entry"`) {
		t.Error("manual index lost the plugin entry point")
	}
	if !strings.Contains(body, `href="/plugins"`) {
		t.Error("plugin entry point does not link to /plugins")
	}
}
