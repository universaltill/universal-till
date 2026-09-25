package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#2343: a theme applied via a cloud set_setting directive (ADR-0018)
// lands in d.State (SetSetting -> rederive -> LoadState) the same way a local
// Settings-page change does, so any FUTURE page render already shows it. The
// gap is an already-open, long-lived kiosk session that never navigates
// again -- the local Settings page forces this with its own
// window.location.reload() (settings.html), but a directive landing in the
// background has nobody to trigger that. GET /ui/theme-sync is polled from
// every open page (base.html) and unconditionally reports the live theme in
// a body-safe OOB <div data-theme="...">; base.html's own
// htmx:oobAfterSwap listener is what actually updates the <head> stylesheet
// <link> -- never a reload, so an in-progress sale is never disturbed, and
// never the <link> itself in the OOB fragment (an earlier draft did that
// directly and was found, in independent review, to be silently dropped by
// htmx's response parser -- see registerThemeSync's own comment).
func TestThemeSync_AlwaysReportsTheLiveThemeInABodySafeOOBDiv(t *testing.T) {
	d, _, _ := themeTestDeps(t)
	d.SetState(common.RuntimeState{Theme: "midnight"})

	mux := http.NewServeMux()
	registerThemeSync(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/ui/theme-sync", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(strings.TrimSpace(body), "<div ") {
		t.Fatalf("OOB fragment must be a plain <div> -- a <link> (or any other head-only element) here gets silently dropped by htmx's response parser, got: %q", body)
	}
	if !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("expected an out-of-band swap fragment, got: %q", body)
	}
	if !strings.Contains(body, `id="theme-sync-poll"`) {
		t.Fatalf("OOB fragment must target the existing #theme-sync-poll div, got: %q", body)
	}
	if !strings.Contains(body, `data-theme="midnight"`) {
		t.Fatalf("OOB fragment must report the LIVE theme (midnight), got: %q", body)
	}
}

// No "did it change" logic lives server-side any more (that's what made the
// old design need a fragile query-string round-trip) -- every poll just
// reports the truth, including "default"/empty, and the client (base.html's
// listener) is the one that decides whether #theme-css needs updating.
func TestThemeSync_ReportsDefaultThemeAsIs(t *testing.T) {
	d, _, _ := themeTestDeps(t)
	d.SetState(common.RuntimeState{Theme: "default"})

	mux := http.NewServeMux()
	registerThemeSync(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/ui/theme-sync", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `data-theme="default"`) {
		t.Fatalf("expected data-theme=\"default\", got: %q", body)
	}
	if strings.Contains(body, "href=") {
		t.Fatalf("the handler must never build a <link>/href itself, got: %q", body)
	}
}

// A theme key containing HTML-breaking characters must never let the OOB
// fragment escape the data-theme attribute -- defence in depth, since
// nothing on the write path (cloud directive or local settings) constrains
// the key's charset today.
func TestThemeSync_EscapesThemeKeyInOOBFragment(t *testing.T) {
	d, _, _ := themeTestDeps(t)
	d.SetState(common.RuntimeState{Theme: `"><script>alert(1)</script>`})

	mux := http.NewServeMux()
	registerThemeSync(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/ui/theme-sync", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "<script>") {
		t.Fatalf("theme key was not escaped, OOB fragment breaks out of the attribute: %q", body)
	}
}

// A real full-page render (index_osk_test.go's own setup pattern) proves
// base.html's actual markup, not just the handler in isolation: #theme-css
// carries data-theme matching what it was rendered with, and the poll div
// exists with the id the handler's OOB response targets.
func TestIndexPage_RendersThemeSyncPollAndCSSLink(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	state.Theme = "monarch"
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerIndex(mux, dp)
	registerThemeSync(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="theme-css"`) || !strings.Contains(body, `data-theme="monarch"`) {
		t.Fatalf("base.html did not render #theme-css with the rendered theme recorded in data-theme: %s", body)
	}
	if !strings.Contains(body, `id="theme-sync-poll"`) || !strings.Contains(body, `hx-get="/ui/theme-sync"`) {
		t.Fatalf("base.html did not render the theme-sync poll div: %s", body)
	}
	if !strings.Contains(body, `href="/themes/monarch.css`) {
		t.Fatalf("base.html did not render the active theme's own stylesheet: %s", body)
	}
	if !strings.Contains(body, "htmx:oobAfterSwap") {
		t.Fatalf("base.html is missing the listener that actually applies a theme-sync update to #theme-css: %s", body)
	}
}

// ut-docs#2783: whatever a background admin pull or cloud directive does to
// the live theme, the poll's client half must never reload or navigate the
// page, and must only touch #theme-css when the reported theme differs from
// what it already shows — so a settled page never re-applies the stylesheet,
// and no combination of pull + poll can turn into a reload loop. Reads the
// listener straight from base.html (the only place it lives).
func TestThemeSyncListener_NeverReloadsAndOnlyAppliesARealChange(t *testing.T) {
	chdirRoot(t)
	raw, err := os.ReadFile(filepath.Join("web", "ui", "layouts", "base.html"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, `<div id="theme-sync-poll"`)
	if start < 0 {
		t.Fatal("base.html has no #theme-sync-poll")
	}
	rest := src[start:]
	open := strings.Index(rest, "<script>")
	end := strings.Index(rest, "</script>")
	if open < 0 || end < open {
		t.Fatal("no listener <script> after #theme-sync-poll")
	}
	js := rest[open:end]
	if !strings.Contains(js, "htmx:oobAfterSwap") {
		t.Fatalf("the script after #theme-sync-poll is not the theme-sync listener: %s", js)
	}
	for _, banned := range []string{"location.reload", "location.assign", "location.href", "location.replace", "htmx.ajax"} {
		if strings.Contains(js, banned) {
			t.Fatalf("theme-sync listener must never reload/navigate/re-request (%s): %s", banned, js)
		}
	}
	if !strings.Contains(js, "live === (link.dataset.theme || '')) return;") {
		t.Fatalf("theme-sync listener lost its no-change early return — a settled page would re-apply #theme-css on every poll: %s", js)
	}
}
