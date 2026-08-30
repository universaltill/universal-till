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

// ut-docs#1125: the setup wizard's / settings' / staff menu page's (/menu)
// language pickers must show a native name ("العربية", "فارسی", "Türkçe"), never a
// bare locale code — a non-technical shop owner doesn't know "fa" is Persian.
func TestNativeLanguageNameRendersCoreLocales(t *testing.T) {
	cases := map[string]string{
		"ar": "العربية",
		"en": "English",
		"fa": "فارسی",
		"tr": "Türkçe",
	}
	for code, want := range cases {
		if got := NativeLanguageName(code); got != want {
			t.Errorf("NativeLanguageName(%q) = %q, want %q", code, got, want)
		}
	}
}

// An unparseable code (never expected from AvailableLocales in practice, but
// the func must not panic on untrusted input) falls back to the raw string
// rather than an empty label.
func TestNativeLanguageNameFallsBackOnUnknownCode(t *testing.T) {
	if got := NativeLanguageName("not-a-real-locale-code!!"); got != "not-a-real-locale-code!!" {
		t.Errorf("NativeLanguageName(garbage) = %q, want the input echoed back", got)
	}
}

func TestFuncsForRegistersNativeLocaleName(t *testing.T) {
	funcs := FuncsFor("en")
	fn, ok := funcs["nativelocalename"].(func(string) string)
	if !ok {
		t.Fatalf("funcs[\"nativelocalename\"] missing or wrong type: %v", funcs["nativelocalename"])
	}
	if got := fn("de"); got != "Deutsch" {
		t.Errorf(`nativelocalename("de") = %q, want "Deutsch"`, got)
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

// TestDefaultLocaleTemplateFuncNormalizesToLanguagePrefix (ut-docs#861, found
// by the Tester step's own driven-run visual check, not written test-first):
// a fresh till nobody has ever touched Settings' Language card on carries
// the full BCP-47 UT_DEFAULT_LOCALE tag ("en-US") as its default, but the
// picker's own options (AvailableLocales()) are always bare shipped-locale
// codes ("en", "ar", ...). An unnormalized "defaultlocale" template func
// would compare "en-US" against "en" and never match, so a real screenshot
// of a never-configured till's Settings page showed NO option selected in
// the new Language picker (looks broken, not just cosmetically off) — this
// guards the fix (prefix-matching, same rule IsRTL already applies).
func TestDefaultLocaleTemplateFuncNormalizesToLanguagePrefix(t *testing.T) {
	InitI18n(nil, "en-US")
	funcs := FuncsFor("en")
	fn, ok := funcs["defaultlocale"].(func() string)
	if !ok {
		t.Fatal("FuncsFor is missing a defaultlocale template func of type func() string")
	}
	if got := fn(); got != "en" {
		t.Fatalf(`defaultlocale() with DefaultLocale="en-US" = %q, want "en" (bare prefix, matches AvailableLocales())`, got)
	}
	InitI18n(nil, "en") // restore the value other tests assume
}

// TestSetDefaultLocale (ut-docs#861): a shop's default locale must be
// changeable live, without a redeploy or another InitI18n call swapping the
// translator itself — SetDefaultLocale only needs to move the atomic
// fallback DefaultLocale()/ResolveLocale() read, since config.I18n already
// loads every shipped locale's strings at boot.
func TestSetDefaultLocale(t *testing.T) {
	InitI18n(nil, "en")
	if got := DefaultLocale(); got != "en" {
		t.Fatalf("DefaultLocale before SetDefaultLocale = %q; want en", got)
	}
	SetDefaultLocale("de")
	if got := DefaultLocale(); got != "de" {
		t.Fatalf("DefaultLocale after SetDefaultLocale(de) = %q; want de", got)
	}
	// Empty value is a no-op-shaped guard, not a reset to "en" — callers
	// (settings_page.go) are expected to skip the call entirely on an
	// empty/invalid submission, but SetDefaultLocale itself must not let an
	// accidental empty string silently blank the till's configured default.
	SetDefaultLocale("")
	if got := DefaultLocale(); got != "de" {
		t.Fatalf("DefaultLocale after SetDefaultLocale(\"\") = %q; want unchanged de", got)
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
		filepath.Join("web", "ui", "partials", "bugreport_panel.html"),
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
	// windows/darwin case: a website link is actionable there (a windowed
	// desktop OS with a browser), unlike the unix-kiosk case below.
	funcs["updatedownloadlink"] = func() bool { return true }
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
	if !strings.Contains(chip, `target="_blank"`) {
		t.Fatalf("expected the kept download link to open in a new context (target=_blank) — a plain same-window navigation is a dead end in the WebView2 desktop shell, which has no NewWindowRequested/back affordance, got %q", chip)
	}
	if strings.Contains(body, `id="sb-update-btn"`) {
		t.Fatalf("expected no in-app self-apply button when canselfupdate is false, got %.500s", body)
	}
}

// A unix kiosk (updatedownloadlink false — GOOS not windows/darwin) is
// fullscreen with no browser chrome: the same website link that's actionable
// on Windows/macOS is a dead end there (ut-docs#147 field report reproduced
// again outside Settings, ut-docs#159). The chip must fall back to plain
// text — no <a>, no href — mirroring what internal/pages/update_api.go's
// updateUnavailableHTML already does for the Settings page.
func TestBaseLayoutUpdateChipHasNoLinkWhenDownloadLinkNotActionable(t *testing.T) {
	InitI18n(realI18n(t), "en")
	funcs := FuncsFor("en")
	funcs["updateavailable"] = func() bool { return true }
	funcs["canselfupdate"] = func() bool { return false }
	funcs["updatedownloadlink"] = func() bool { return false }
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
		t.Fatalf("expected the status-bar update chip to still render (as plain text), got %.500s", body)
	}
	end := strings.Index(body[idx:], "</span>")
	if end == -1 {
		t.Fatalf("expected the kiosk-dead-end chip to be a plain <span>, not a link, got %.500s", body[idx:])
	}
	chip := body[idx : idx+end]
	if strings.Contains(chip, "<a ") || strings.Contains(chip, "href=") {
		t.Fatalf("expected no clickable link in the status bar when a download link isn't actionable on this platform (ut-docs#159 kiosk dead-end), got %q", chip)
	}
	// Parity with internal/pages/update_api.go's updateUnavailableHTML,
	// which already tells the Settings page *why* nothing is clickable —
	// an inert chip with no explanation looks identical to the actionable
	// ones and offers no next step.
	if !strings.Contains(chip, "available for this install") { // apostrophe is HTML-escaped
		t.Fatalf("expected the no-link chip to explain why (settings.update.unavailable_here), got %q", chip)
	}
	if strings.Contains(body, `id="sb-update-btn"`) {
		t.Fatalf("expected no in-app self-apply button either when canselfupdate is false, got %.500s", body)
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

// jsonVals is the shared, escape-safe helper for building an hx-vals JSON
// literal from a template (ut-docs#19): several partials (catalog_variants,
// suggestions, self_order_grid, basket) interpolated raw field values
// directly into a hand-written hx-vals='{"k":"{{ .V }}"}' literal, the same
// pattern that produced invalid JSON for any quoted name in buttons_admin.html
// (fixed there by internal/ui.SearchResult.AddVals, marshaling server-side).
// Unlike toJSON (template.JS, deliberately unescaped for <script> contexts),
// jsonVals returns a plain string so html/template still HTML-escapes it for
// the attribute-value context it's meant for — required because a literal
// apostrophe in the marshaled JSON would otherwise break out of hx-vals='...'
// (single-quoted), not just double-quote-breaking a hardcoded literal.
func TestJSONVals_QuotesBackslashesAndApostrophesSurvive(t *testing.T) {
	out, err := jsonVals("barcode", `weird"name\here'quote`, "qty", 3)
	if err != nil {
		t.Fatalf("jsonVals: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("jsonVals output is not valid JSON: %v (%s)", err, out)
	}
	if got["barcode"] != `weird"name\here'quote` {
		t.Errorf("barcode round-trip = %v", got["barcode"])
	}
	if got["qty"] != float64(3) {
		t.Errorf("qty round-trip = %v", got["qty"])
	}
}

func TestJSONVals_OddArgCountErrors(t *testing.T) {
	if _, err := jsonVals("barcode", "123", "orphan"); err == nil {
		t.Error("expected an error for an odd number of arguments")
	}
}

func TestJSONVals_NonStringKeyErrors(t *testing.T) {
	if _, err := jsonVals(1, "123"); err == nil {
		t.Error("expected an error for a non-string key")
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

// uiScaleCSS exposes the raw clamped multiplier (not pre-multiplied by 16px)
// so the sale screen's fluid, viewport-responsive root size (ut-docs#161,
// app.css's --fluid-fs) can be scaled further by the operator's manual
// Settings > UI scale choice via `calc(var(--ui-scale) * var(--fluid-fs))`,
// instead of the fixed 16px baseline uiScalePx bakes in.
func TestUIScaleCSSClampsToRawMultiplier(t *testing.T) {
	defer InitUIScale(1.0)
	InitUIScale(1.3)
	if got := uiScaleCSS(); got != "1.3" {
		t.Fatalf("uiScaleCSS(1.3) = %q; want 1.3", got)
	}
	InitUIScale(3.0) // out of range resets to 1.0
	if got := uiScaleCSS(); got != "1" {
		t.Fatalf("uiScaleCSS(out-of-range) = %q; want 1", got)
	}
	InitUIScale(0.4) // below range resets to 1.0
	if got := uiScaleCSS(); got != "1" {
		t.Fatalf("uiScaleCSS(below-range) = %q; want 1", got)
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
