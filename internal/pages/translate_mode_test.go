package pages

import (
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#4370 (ADR-0160 §7): translate mode — a manager-only view mode
// whose key panel links each string into Weblate. Gated like the override
// editor and refused outright on a self-order kiosk.

func tmCookie(req *http.Request) *http.Request {
	req.AddCookie(&http.Cookie{Name: translateModeCookie, Value: "1"})
	return req
}

func tmPost(mux *http.ServeMux, req *http.Request, form url.Values) *httptest.ResponseRecorder {
	req.Body = io.NopCloser(strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestTranslateModeToggle_ManagerTurnsItOn(t *testing.T) {
	mux, dp, _ := newTranslationsTestDeps(t)
	rec := tmPost(mux, withManager(httptest.NewRequest(http.MethodPost, "/api/translate-mode", nil)), url.Values{"on": {"1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle on = %d: %s", rec.Code, rec.Body.String())
	}
	c := cookieNamed(rec, translateModeCookie)
	if c == nil || c.Value != "1" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("cookie = %+v", c)
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Fatal("the page must reload so its strings render with keys")
	}
	var n int
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_log WHERE action = 'translate_mode_on'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows = %d (%v)", n, err)
	}
}

func TestTranslateModeToggle_RefusedWithoutPermission(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	rec := tmPost(mux, httptest.NewRequest(http.MethodPost, "/api/translate-mode", nil), url.Values{"on": {"1"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no session: %d, want 403", rec.Code)
	}
	if c := cookieNamed(rec, translateModeCookie); c != nil && c.Value == "1" {
		t.Fatal("refused toggle still set the cookie")
	}
}

func TestTranslateModeToggle_RefusedOnSelfOrderKiosk(t *testing.T) {
	mux, dp, _ := newTranslationsTestDeps(t)
	if err := dp.Settings.Set(t.Context(), "display.mode", "self_order"); err != nil {
		t.Fatal(err)
	}
	rec := tmPost(mux, withManager(httptest.NewRequest(http.MethodPost, "/api/translate-mode", nil)), url.Values{"on": {"1"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("self-order kiosk: %d, want 403", rec.Code)
	}
	if c := cookieNamed(rec, translateModeCookie); c != nil && c.Value == "1" {
		t.Fatal("kiosk toggle set the cookie")
	}
}

func TestTranslateModeToggle_OffAlwaysClears(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	rec := tmPost(mux, tmCookie(httptest.NewRequest(http.MethodPost, "/api/translate-mode", nil)), url.Values{"on": {"0"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle off = %d", rec.Code)
	}
	if c := cookieNamed(rec, translateModeCookie); c == nil || c.MaxAge >= 0 {
		t.Fatalf("off must expire the cookie, got %+v", c)
	}
}

func TestTranslateModeActive(t *testing.T) {
	_, dp, _ := newTranslationsTestDeps(t)
	on := func(r *http.Request) bool { return translateModeActive(dp, r) }
	if !on(withManager(tmCookie(httptest.NewRequest(http.MethodGet, "/", nil)))) {
		t.Fatal("manager + cookie should be on")
	}
	if on(withManager(httptest.NewRequest(http.MethodGet, "/", nil))) {
		t.Fatal("no cookie should be off")
	}
	if on(tmCookie(httptest.NewRequest(http.MethodGet, "/", nil))) {
		t.Fatal("a left-over cookie without the permission (a cashier signed in after) must be off")
	}
	for _, p := range []string{"/self-order", "/self-order/menu", "/api/self-order/basket"} {
		if on(withManager(tmCookie(httptest.NewRequest(http.MethodGet, p, nil)))) {
			t.Fatalf("customer page %s must never render in translate mode", p)
		}
	}
	if err := dp.Settings.Set(t.Context(), "display.mode", "self_order"); err != nil {
		t.Fatal(err)
	}
	if on(withManager(tmCookie(httptest.NewRequest(http.MethodGet, "/", nil)))) {
		t.Fatal("a self-order kiosk must never be in translate mode")
	}
}

func getPanel(mux *http.ServeMux, q string, cookie bool) *httptest.ResponseRecorder {
	req := withManager(httptest.NewRequest(http.MethodGet, "/ui/translate-mode/panel?"+q, nil))
	if cookie {
		tmCookie(req)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestTranslatePanel_ShowsKeyMetadataWeblateLinkAndQR(t *testing.T) {
	restore := httpx.SnapshotStateForTests()
	t.Cleanup(restore)
	mux, _, _ := newTranslationsTestDeps(t)
	rec := getPanel(mux, "key=common.close&ns=pos,translations,common", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("panel = %d: %s", rec.Code, rec.Body.String())
	}
	body := html.UnescapeString(rec.Body.String())
	for _, want := range []string{
		"common.close",
		"Button that closes a panel or dialog.", // description, en.meta.json
		"24",                                    // max_length
		`https://translate.universaltill.com/translate/universal-till/common/en/?q=key%3A%3D%22common.close%22`,
		"data:image/png;base64,",
		`data-testid="translate-panel-area-sale"`,
		`data-testid="translate-panel-area-settings"`,
		`name="value"`,
		`hx-post="/api/translate-mode/override"`,
		`target="_blank"`,
		`rel="noopener noreferrer"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("panel lacks %q", want)
		}
	}
	// The till only links to Weblate: no script, fetch or form ever targets it.
	if strings.Contains(body, `action="https://translate`) || strings.Contains(body, `hx-get="https://`) {
		t.Fatal("the panel must never call Weblate itself")
	}
}

func TestTranslatePanel_RefusedOutsideTranslateMode(t *testing.T) {
	mux, dp, _ := newTranslationsTestDeps(t)
	if rec := getPanel(mux, "key=common.close", false); rec.Code != http.StatusForbidden {
		t.Fatalf("no cookie: %d, want 403", rec.Code)
	}
	if err := dp.Settings.Set(t.Context(), "display.mode", "self_order"); err != nil {
		t.Fatal(err)
	}
	if rec := getPanel(mux, "key=common.close", true); rec.Code != http.StatusForbidden {
		t.Fatalf("self-order kiosk: %d, want 403", rec.Code)
	}
}

func TestTranslatePanel_UnknownKey(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	if rec := getPanel(mux, "key=no.such.key.anywhere", true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown key: %d, want 404", rec.Code)
	}
}

func TestTranslatePanel_OverrideSaveAndReset(t *testing.T) {
	mux, dp, i18n := newTranslationsTestDeps(t)
	rec := tmPost(mux, withManager(tmCookie(httptest.NewRequest(http.MethodPost, "/api/translate-mode/override", nil))),
		url.Values{"key": {"nav.home"}, "value": {"Start"}, "op": {"save"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}
	if got := i18n.T("en", "nav.home"); got != "Start" {
		t.Fatalf("override not live: %q", got)
	}
	if !strings.Contains(rec.Body.String(), `data-testid="translate-panel-saved"`) {
		t.Fatal("save must confirm in the panel")
	}
	rec = tmPost(mux, withManager(tmCookie(httptest.NewRequest(http.MethodPost, "/api/translate-mode/override", nil))),
		url.Values{"key": {"nav.home"}, "op": {"reset"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d", rec.Code)
	}
	if got := i18n.T("en", "nav.home"); got == "Start" {
		t.Fatal("reset left the override in place")
	}
	var n int
	_ = dp.Db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_log WHERE action IN ('translation_override_set','translation_override_cleared')`).Scan(&n)
	if n != 2 {
		t.Fatalf("audit rows = %d, want 2", n)
	}
	// Refused without translate mode (the same endpoint is not a back door).
	rec = tmPost(mux, withManager(httptest.NewRequest(http.MethodPost, "/api/translate-mode/override", nil)),
		url.Values{"key": {"nav.home"}, "value": {"X"}, "op": {"save"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("override outside translate mode = %d, want 403", rec.Code)
	}
}

// A till whose locale is en-US (the env default on a US host) serves the till's "en" strings; the panel's
// Weblate link and its override must use that same served locale, not the
// browser's region tag (Weblate has no en_US translation; an en-US
// override would never show for a plain "en" request).
func TestTranslatePanel_UsesTheServedLocaleNotTheBrowserTag(t *testing.T) {
	mux, _, i18n := newTranslationsTestDeps(t)
	req := withManager(tmCookie(httptest.NewRequest(http.MethodGet, "/ui/translate-mode/panel?key=common.close&lang=en-US", nil)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := html.UnescapeString(rec.Body.String())
	if !strings.Contains(body, "/universal-till/common/en/?q=") {
		t.Fatalf("Weblate link should use the served locale en:\n%s", body)
	}
	save := withManager(tmCookie(httptest.NewRequest(http.MethodPost, "/api/translate-mode/override?lang=en-US", nil)))
	if rec := tmPost(mux, save, url.Values{"key": {"nav.home"}, "value": {"Start"}, "op": {"save"}}); rec.Code != http.StatusOK {
		t.Fatalf("save = %d", rec.Code)
	}
	if got := i18n.T("en", "nav.home"); got != "Start" {
		t.Fatalf("override saved under the browser tag, not the served locale: T(en) = %q", got)
	}
}

// Review L1: the public demo (ADR-0113) offers no translate mode — no
// Weblate links, no shop overrides. The three routes are on the denied
// side, a hand-set cookie never turns the mode on, and the editor shows no
// card.
func TestTranslateMode_NeverInTheDemo(t *testing.T) {
	for _, p := range []string{
		"POST /api/translate-mode",
		"GET /ui/translate-mode/panel",
		"POST /api/translate-mode/override",
	} {
		if !demoDeniedRoutes[p] || demoAllowedRoutes[p] {
			t.Errorf("%q must be denied in the demo", p)
		}
	}
	mux, dp, _ := newTranslationsTestDeps(t)
	dp.Cfg.Demo = true
	if translateModeActive(dp, withManager(tmCookie(httptest.NewRequest(http.MethodGet, "/", nil)))) {
		t.Fatal("a demo till must never render in translate mode, even with the cookie")
	}
	rec := tmPost(mux, withManager(httptest.NewRequest(http.MethodPost, "/api/translate-mode", nil)), url.Values{"on": {"1"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("demo toggle = %d, want 403", rec.Code)
	}
	req := withManager(httptest.NewRequest(http.MethodGet, "/translations?edit_locale=en", nil))
	page := httptest.NewRecorder()
	mux.ServeHTTP(page, req)
	if page.Code != http.StatusOK {
		t.Fatalf("GET /translations = %d", page.Code)
	}
	if strings.Contains(page.Body.String(), `data-testid="translate-mode-card"`) {
		t.Fatal("the demo's translation editor must not offer translate mode")
	}
}

// Review L2: the key chooser's hx-get URLs carry each key and the page's
// key/namespace lists as encoded query values, so a value holding &, #, +
// comes back unchanged. A space separates list entries (splitList), so the
// space case checks that the re-emitted lists still parse to the same
// entries.
func TestTranslatePanel_ChooserURLsAreQueryEncoded(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	keys := "common.close,a&b,#c+d e%20f"
	ns := "pos,x&y #z+w"
	wantKeys := splitList(keys, 20)
	rec := getPanel(mux, url.Values{"key": {"common.close"}, "keys": {keys}, "ns": {ns}}.Encode(), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("panel = %d: %s", rec.Code, rec.Body.String())
	}
	var got []url.Values
	body := rec.Body.String()
	for {
		i := strings.Index(body, `hx-get="`)
		if i < 0 {
			break
		}
		body = body[i+len(`hx-get="`):]
		j := strings.IndexByte(body, '"')
		raw := html.UnescapeString(body[:j])
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("hx-get %q: %v", raw, err)
		}
		if u.Fragment != "" || strings.ContainsAny(u.RawQuery, " #") {
			t.Fatalf("hx-get %q carries an unencoded # or space", raw)
		}
		got = append(got, u.Query())
		body = body[j:]
	}
	if len(got) != len(wantKeys) {
		t.Fatalf("chooser buttons = %d, want %d (%q)", len(got), len(wantKeys), wantKeys)
	}
	for i, q := range got {
		if q.Get("key") != wantKeys[i] {
			t.Errorf("button %d key = %q, want %q", i, q.Get("key"), wantKeys[i])
		}
		if gk := splitList(q.Get("keys"), 20); strings.Join(gk, "\n") != strings.Join(wantKeys, "\n") {
			t.Errorf("button %d keys = %q, want %q", i, gk, wantKeys)
		}
		if gn := q.Get("ns"); gn != strings.Join(splitList(ns, 200), ",") {
			t.Errorf("button %d ns = %q", i, gn)
		}
	}
}
