package httpx

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/universaltill/universal-till/internal/config"
	moneypkg "github.com/universaltill/universal-till/internal/money"
)

func realI18n(t *testing.T) *config.I18n {
	t.Helper()
	i18n, err := config.NewI18n(filepath.Join("..", "..", "web", "locales"), "en")
	if err != nil {
		t.Fatalf("NewI18n: %v", err)
	}
	return i18n
}

// TestTFallsBackToKeyWithoutTranslator guards the typed-nil trap: InitI18n(nil,
// ...) stores a typed nil *config.I18n in the atomic, which still compares
// non-nil as an interface — T must fall back to the key, not call a method on
// a nil receiver and panic.
func TestTFallsBackToKeyWithoutTranslator(t *testing.T) {
	InitI18n(nil, "en")
	if got := T("en", "some.key"); got != "some.key" {
		t.Fatalf("T without translator = %q; want the key itself", got)
	}
}

func TestTTranslatesWithRealTranslator(t *testing.T) {
	InitI18n(realI18n(t), "en")
	got := T("en", "nav.settings")
	if got == "" || got == "nav.settings" {
		t.Fatalf("T with real translator returned %q; want a translation", got)
	}
	// Unknown keys fall back to the key.
	if got := T("en", "definitely.not.a.key"); got != "definitely.not.a.key" {
		t.Fatalf("T unknown key = %q; want key fallback", got)
	}
}

// TestFuncsForTAndLocalesWithoutTranslator covers the same typed-nil trap for
// the closures FuncsFor hands to templates.
func TestFuncsForTAndLocalesWithoutTranslator(t *testing.T) {
	InitI18n(nil, "en")
	funcs := FuncsFor("en")
	tFn := funcs["T"].(func(string) string)
	if got := tFn("some.key"); got != "some.key" {
		t.Fatalf("template T without translator = %q; want the key", got)
	}
	localesFn := funcs["locales"].(func() []string)
	if got := localesFn(); len(got) != 1 || got[0] != "en" {
		t.Fatalf("locales without translator = %v; want [en]", got)
	}
}

func TestDefaultLocale(t *testing.T) {
	InitI18n(nil, "de")
	if got := DefaultLocale(); got != "de" {
		t.Fatalf("DefaultLocale = %q; want de", got)
	}
	InitI18n(nil, "")
	if got := DefaultLocale(); got != "en" {
		t.Fatalf("DefaultLocale with empty fallback = %q; want en", got)
	}
	InitI18n(nil, "en") // restore the value other tests assume
}

func TestResolveLocaleFallsBackToEnWithEmptyDefault(t *testing.T) {
	InitI18n(nil, "")
	defer InitI18n(nil, "en")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	if got := ResolveLocale(w, r); got != "en" {
		t.Fatalf("ResolveLocale with empty default = %q; want en", got)
	}
}

func TestStripWebPrefixes(t *testing.T) {
	got := stripWebPrefixes([]string{
		filepath.Join("web", "ui", "pages", "x.html"),
		"ui/partials/nav.html", // already stripped stays as-is
	})
	want := []string{"ui/pages/x.html", "ui/partials/nav.html"}
	if len(got) != len(want) {
		t.Fatalf("stripWebPrefixes returned %d entries; want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stripWebPrefixes[%d] = %q; want %q", i, got[i], want[i])
		}
	}
}

func TestNewRendererRendersEmbeddedPage(t *testing.T) {
	InitI18n(realI18n(t), "en")
	r, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "pin.html"),
		FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	w := httptest.NewRecorder()
	data := map[string]any{"title": "Change PIN", "theme": "", "menuItems": nil, "errKey": ""}
	if err := r.Render(w, "base", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(w.Body.String(), "<html") {
		t.Fatalf("rendered page missing <html>: %.200s", w.Body.String())
	}
}

func TestNewRendererFailsOnMissingPage(t *testing.T) {
	_, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "no-such-page.html"),
		template.FuncMap{},
	)
	if err == nil {
		t.Fatal("NewRenderer with a missing page: want error, got nil")
	}
}

func TestRenderWithRendersAndReports500OnBadFile(t *testing.T) {
	InitI18n(realI18n(t), "en")
	files := []string{
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "pin.html"),
		filepath.Join("web", "ui", "partials", "nav.html"),
	}
	h := RenderWith(files, FuncsFor("en"))("base", map[string]any{
		"title": "Change PIN", "theme": "", "menuItems": nil, "errKey": "",
	})
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/pin", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("RenderWith = %d: %s", w.Code, w.Body.String())
	}

	bad := RenderWith([]string{"web/ui/pages/no-such.html"}, template.FuncMap{})("base", nil)
	w = httptest.NewRecorder()
	bad(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("RenderWith missing file = %d; want 500", w.Code)
	}
}

// The status-bar update chip is the thing a user actually clicks (ut-docs#152
// field report: a Windows user clicked it expecting an in-place update, like
// the self-update button below it, and got a browser download page instead).
// Both branches render as the same "sb-item sb-update" pill with the same up
// arrow, distinguished only by their text -- so when self-update isn't
// supported, that text must say plainly that clicking downloads something,
// not just "available".
func TestBaseLayoutUpdateChipSaysDownloadWhenSelfUpdateUnsupported(t *testing.T) {
	InitI18n(realI18n(t), "en")
	funcs := FuncsFor("en")
	funcs["updateavailable"] = func() bool { return true }
	funcs["canselfupdate"] = func() bool { return false }
	funcs["latestversion"] = func() string { return "9.9.9" }
	r, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "pin.html"),
		funcs,
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	w := httptest.NewRecorder()
	data := map[string]any{"title": "Change PIN", "theme": "", "menuItems": nil, "errKey": ""}
	if err := r.Render(w, "base", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	idx := strings.Index(body, `class="sb-item sb-update"`)
	if idx == -1 {
		t.Fatalf("expected the status-bar update chip to render, got %.500s", body)
	}
	// The chip itself, not the rest of the page -- base.html's own inline
	// script text ("Downloading update…", for the self-apply button's
	// in-progress state) also contains the substring "Download", so
	// asserting against the whole body would false-pass even without the
	// chip's own text saying so.
	end := strings.Index(body[idx:], "</a>")
	if end == -1 {
		t.Fatalf("expected the update-unavailable chip to be a link (<a>...</a>), got %.500s", body[idx:])
	}
	chip := body[idx : idx+end]
	if !strings.Contains(chip, `href="https://www.universaltill.com/download"`) {
		t.Fatalf("expected the status-bar update chip to link to the download page, got %q", chip)
	}
	if !strings.Contains(chip, "Download") {
		t.Fatalf("status-bar update chip must say Download when self-update isn't supported, not just \"available\" (ut-docs#152), got %q", chip)
	}
	if strings.Contains(body, `id="sb-update-btn"`) {
		t.Fatalf("expected no in-app self-apply button when canselfupdate is false, got %.500s", body)
	}
}

func TestNewMuxDispatchesRegisteredRoutes(t *testing.T) {
	mux := NewMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/ping", nil))
	if w.Code != http.StatusTeapot {
		t.Fatalf("mux dispatch = %d; want 418", w.Code)
	}
}

func TestJSONHandlerSuccessAndError(t *testing.T) {
	type in struct {
		Name string `json:"name"`
	}
	ok := JSON(func(i in) (map[string]string, error) {
		return map[string]string{"hello": i.Name}, nil
	})
	w := httptest.NewRecorder()
	ok(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"till"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("JSON success status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["hello"] != "till" {
		t.Fatalf("body = %v; want hello=till", out)
	}

	fail := JSON(func(i in) (map[string]string, error) {
		return nil, errors.New("boom")
	})
	w = httptest.NewRecorder()
	fail(w, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("JSON error status = %d; want 400", w.Code)
	}
	var errOut map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &errOut); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errOut["error"] != "boom" {
		t.Fatalf("error body = %v; want error=boom", errOut)
	}
}

func TestToJSON(t *testing.T) {
	got := toJSON(map[string]int{"a": 1})
	if string(got) != `{"a":1}` {
		t.Fatalf("toJSON = %s", got)
	}
}

func TestMinorUnits(t *testing.T) {
	cases := []struct {
		in   any
		want int64
		ok   bool
	}{
		{moneypkg.FromMinor(1234), 1234, true},
		{int64(99), 99, true},
		{int(7), 7, true},
		{int32(8), 8, true},
		{"nope", 0, false},
		{1.5, 0, false},
	}
	for _, c := range cases {
		got, ok := minorUnits(c.in)
		if got != c.want || ok != c.ok {
			t.Fatalf("minorUnits(%v) = %d,%v; want %d,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestInitUIScaleClampsAndScalesRootPx(t *testing.T) {
	defer InitUIScale(1.0)
	InitUIScale(1.3)
	if got := uiScalePx(); got != "20.8" {
		t.Fatalf("uiScalePx(1.3) = %q; want 20.8", got)
	}
	InitUIScale(3.0) // out of range resets to 1.0
	if got := uiScalePx(); got != "16" {
		t.Fatalf("uiScalePx(out-of-range) = %q; want 16", got)
	}
	InitUIScale(0.4) // below range resets to 1.0
	if got := uiScalePx(); got != "16" {
		t.Fatalf("uiScalePx(below-range) = %q; want 16", got)
	}
}

func TestInitOSKModeValidatesInput(t *testing.T) {
	defer InitOSKMode("auto")
	InitOSKMode("on")
	if got := oskModeVal(); got != "on" {
		t.Fatalf("oskModeVal = %q; want on", got)
	}
	InitOSKMode("bogus")
	if got := oskModeVal(); got != "auto" {
		t.Fatalf("oskModeVal after invalid mode = %q; want auto", got)
	}
}

func TestInitIdleLockConvertsMinutesAndClampsNegative(t *testing.T) {
	defer InitIdleLock(0)
	InitIdleLock(5)
	if got := idleLockSecs.Load(); got != 300 {
		t.Fatalf("idleLockSecs = %d; want 300", got)
	}
	InitIdleLock(-3)
	if got := idleLockSecs.Load(); got != 0 {
		t.Fatalf("idleLockSecs negative = %d; want 0", got)
	}
}

func TestInitKioskExposedToTemplates(t *testing.T) {
	InitKiosk(true)
	defer InitKiosk(false)
	kioskFn := FuncsFor("en")["kiosk"].(func() bool)
	if !kioskFn() {
		t.Fatal("kiosk template func = false after InitKiosk(true)")
	}
	InitKiosk(false)
	if kioskFn() {
		t.Fatal("kiosk template func = true after InitKiosk(false)")
	}
}

func TestCurrenciesRegistryIsUsable(t *testing.T) {
	all := Currencies()
	if len(all) == 0 {
		t.Fatal("Currencies() is empty")
	}
	seenGBP := false
	for _, c := range all {
		if c.Code == "" || c.Display == "" || c.Name == "" {
			t.Fatalf("currency with empty field: %+v", c)
		}
		if c.Code == "GBP" {
			seenGBP = true
		}
	}
	if !seenGBP {
		t.Fatal("GBP missing from the currency registry")
	}
}

func TestImgVersionOnlyStampsPublicURLs(t *testing.T) {
	if got := imgVersion("/public/items/a.png"); !strings.HasPrefix(got, "/public/items/a.png?v=") {
		t.Fatalf("imgVersion(public) = %q; want ?v= suffix", got)
	}
	if got := imgVersion("/ui/logo.png"); got != "/ui/logo.png" {
		t.Fatalf("imgVersion(non-public) = %q; want unchanged", got)
	}
	if got := imgVersion("public/items/a.png"); got != "public/items/a.png" {
		t.Fatalf("imgVersion(no leading slash) = %q; want unchanged", got)
	}
}

func TestIsRTL(t *testing.T) {
	cases := map[string]bool{
		"fa": true, "fa-IR": true, "ar_SA": true, "he": true, "ur": true,
		"en": false, "en-GB": false, "tr": false, "": false,
	}
	for locale, want := range cases {
		if got := IsRTL(locale); got != want {
			t.Fatalf("IsRTL(%q) = %v; want %v", locale, got, want)
		}
	}
}
