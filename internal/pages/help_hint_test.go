package pages

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/manual"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

var helpHintHref = regexp.MustCompile(`class="help-hint" href="([^"]+)"`)

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
	m := helpHintHref.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatal("no help hint rendered on /catalog")
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
		"/some/unclaimed/route": "/help",
	} {
		if got := manual.HelpHref(route); got != want {
			t.Errorf("HelpHref(%q) = %q, want %q", route, got, want)
		}
	}
}
