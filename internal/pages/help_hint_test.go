package pages

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/manual"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// Anchored on data-testid="help-hint" rather than the CSS class — ut-docs#1348
// changed the nav rail's help link from class="help-hint" to class="nav-toggle"
// (matching every other rail icon's button treatment), and attribute order
// isn't guaranteed, so this matches the whole opening tag first and then
// pulls href out of it, independent of which class or attribute order is used.
var helpHintTag = regexp.MustCompile(`<a\b[^>]*\bdata-testid="help-hint"[^>]*>`)
var helpHintHrefAttr = regexp.MustCompile(`\bhref="([^"]+)"`)

// The contextual "?" must resolve to the topic documenting the page it is on.
//
// Regression: it worked on / (rendered through httpx.Render) and silently fell
// back to the manual's index on /catalog and every other page, because those
// render through httpx.RenderWith, which wasn't binding the per-request
// helpHref. A "?" that always lands on the contents page looks like it works,
// which is exactly why this needs a test rather than a glance.
func TestHelpHintResolvesPerPage(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n("web/locales", "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Settings: settings.NewStore(db),
	}

	mux := http.NewServeMux()
	catalog.Register(mux, dp) // renders via httpx.RenderWith — the regressed path

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog: %d %s", rec.Code, rec.Body.String())
	}
	tag := helpHintTag.FindString(rec.Body.String())
	if tag == "" {
		t.Fatal("no help hint rendered on /catalog")
	}
	m := helpHintHrefAttr.FindStringSubmatch(tag)
	if m == nil {
		t.Fatalf("help hint tag has no href: %s", tag)
	}
	if m[1] != "/help/catalog" {
		t.Errorf("hint on /catalog → %s, want /help/catalog", m[1])
	}
}

// The route→topic mapping the "?" resolves through comes from the topics' own
// front matter, so a page whose topic exists must resolve to it, and a page
// with no topic yet must still get a working link rather than a dead one.
func TestHelpHrefMapping(t *testing.T) {
	chdirRoot(t)
	for route, want := range map[string]string{
		"/":                     "/help/sell",
		"/catalog":              "/help/catalog",
		"/inventory":            "/help/inventory",
		"/reports":              "/help/reports",
		"/plugins":              "/help/plugins",
		"/users":                "/help/users",
		"/backoffice":           "/help/alerts",
		"/menu":                 "/help/menu",
		"/invoice/12345":        "/help/invoices",
		"/journal/R-0001":       "/help/reports",
		"/some/unclaimed/route": "/help",
	} {
		if got := manual.HelpHref(route); got != want {
			t.Errorf("HelpHref(%q) = %q, want %q", route, got, want)
		}
	}
}

// /settings is one route claimed by one topic (display), but several other
// topics with real content — backups, claim, payments, printing, updates,
// reports (ADR-0040 report retention card, ut-docs#571) — document SECTIONS
// of that same page. Those can't claim the route (the duplicate-route guard
// exists precisely to forbid that), so the page carries explicit
// {{ helpLink "…" }} hints next to its section headings instead. This pins
// that each renders and points at the right topic.
func TestSettingsSectionsCarryExplicitHelpLinks(t *testing.T) {
	chdirRoot(t)
	// The payments/backups/printer cards are manager-gated; auth-off (the same
	// mode the e2e till runs in) renders them without building a session.
	t.Setenv("UT_AUTH", "off")
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n("web/locales", "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Settings: settings.NewStore(db),
	}

	mux := http.NewServeMux()
	registerSettings(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, id := range []string{"claim", "updates", "payments", "backups", "printing", "reports"} {
		if !strings.Contains(body, `href="/help/`+id+`"`) {
			t.Errorf("settings page is missing the explicit help link for the %q topic", id)
		}
	}
}
