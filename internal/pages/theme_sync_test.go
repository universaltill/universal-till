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

// A real full-page render (index_osk_test.go's own setup pattern) proves
// base.html's actual markup, not just the handler in isolation: the
// #theme-css link exists for the OOB swap to target, and the poll div
// carries the theme the page was rendered with.
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
	if !strings.Contains(body, `id="theme-css"`) {
		t.Fatalf("base.html did not render the #theme-css swap target: %s", body)
	}
	if !strings.Contains(body, `hx-get="/ui/theme-sync?theme=monarch"`) {
		t.Fatalf("base.html's theme-sync poll did not carry the rendered theme: %s", body)
	}
	if !strings.Contains(body, `href="/themes/monarch.css`) {
		t.Fatalf("base.html did not render the active theme's own stylesheet: %s", body)
	}
}

// ut-docs#2343: a theme applied via a cloud set_setting directive (ADR-0018)
// lands in d.State (SetSetting -> rederive -> LoadState) the same way a local
// Settings-page change does, so any FUTURE page render already shows it. The
// gap is an already-open, long-lived kiosk session that never navigates
// again -- the local Settings page forces this with its own
// window.location.reload() (settings.html), but a directive landing in the
// background has nobody to trigger that. GET /ui/theme-sync is polled from
// every open page (base.html) and answers with an out-of-band swap of the
// <head> stylesheet <link> when the live theme has moved past what the page
// was rendered with -- never a reload, so an in-progress sale is never
// disturbed.
func TestThemeSync_TillReturnsOOBLinkWhenThemeChangedSincePageRender(t *testing.T) {
	d, _, _ := themeTestDeps(t)
	d.SetState(common.RuntimeState{Theme: "midnight"})

	mux := http.NewServeMux()
	registerThemeSync(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/ui/theme-sync?theme=default", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("expected an out-of-band swap fragment, got: %q", body)
	}
	if !strings.Contains(body, `id="theme-css"`) {
		t.Fatalf("OOB fragment must target the existing #theme-css link, got: %q", body)
	}
	if !strings.Contains(body, `/themes/midnight.css`) {
		t.Fatalf("OOB fragment must point at the LIVE theme (midnight), got: %q", body)
	}
}

// The common case (~every 30s, for the whole time a till sits on one theme)
// must be a true no-op: no markup, so htmx's OOB scan has nothing to do and
// the stylesheet is never needlessly re-fetched.
func TestThemeSync_EmptyWhenThemeUnchanged(t *testing.T) {
	d, _, _ := themeTestDeps(t)
	d.SetState(common.RuntimeState{Theme: "default"})

	mux := http.NewServeMux()
	registerThemeSync(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/ui/theme-sync?theme=default", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); strings.TrimSpace(body) != "" {
		t.Fatalf("expected an empty no-op body, got: %q", body)
	}
}

// Reverting to "default" (no override CSS of its own -- see base.html's
// own comment on why #theme-css is unconditionally rendered) must clear the
// override, not point the swapped <link> at a nonexistent /themes/default.css.
func TestThemeSync_RevertingToDefaultOmitsHref(t *testing.T) {
	d, _, _ := themeTestDeps(t)
	d.SetState(common.RuntimeState{Theme: "default"})

	mux := http.NewServeMux()
	registerThemeSync(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/ui/theme-sync?theme=midnight", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `hx-swap-oob="true"`) || !strings.Contains(body, `id="theme-css"`) {
		t.Fatalf("expected an OOB swap clearing the override, got: %q", body)
	}
	if strings.Contains(body, "href=") {
		t.Fatalf("reverting to default must not point at a themes/default.css that doesn't exist, got: %q", body)
	}
}

// A theme key containing HTML/URL-unsafe characters must never let the OOB
// fragment break out of the href attribute -- defence in depth, since
// nothing on the write path (cloud directive OR local settings) constrains
// the key's charset today.
func TestThemeSync_EscapesThemeKeyInOOBFragment(t *testing.T) {
	d, _, _ := themeTestDeps(t)
	d.SetState(common.RuntimeState{Theme: `"><script>alert(1)</script>`})

	mux := http.NewServeMux()
	registerThemeSync(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/ui/theme-sync?theme=default", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "<script>") {
		t.Fatalf("theme key was not escaped, OOB fragment breaks out of the href: %q", body)
	}
}
