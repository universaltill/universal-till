package httpx

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	moneypkg "github.com/universaltill/universal-till/internal/money"
)

// chdirTemp switches the process CWD to a fresh empty directory for the
// duration of the test and restores it after — simulates a packaged
// install launched from a working directory that has no web/ subtree
// alongside it (a shortcut, a systemd unit with a different
// WorkingDirectory, an installer that doesn't chdir first, ...).
func chdirTemp(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
}

// TestRenderPartialWorksFromAnyWorkingDirectory guards the real bug found
// while walking through a from-scratch install as a brand-new shop owner
// would: RenderPartial used to read web/ui/... straight off disk relative
// to the CWD, so launching the binary from anywhere other than the
// repo/install root (which package managers and service definitions don't
// guarantee) crashed every page render — including the first-boot setup
// wizard itself. Templates are embedded now; this must work with zero
// filesystem context at all.
func TestRenderPartialWorksFromAnyWorkingDirectory(t *testing.T) {
	chdirTemp(t)
	InitI18n(nil, "en")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/setup", nil)
	RenderPartial("ui/pages/setup.html", map[string]any{"countries": nil, "errKey": ""})(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 rendering the setup wizard from an unrelated CWD, got %d: %s", w.Code, w.Body.String())
	}
}

// TestRenderWorksFromAnyWorkingDirectory covers the full-page path (layout
// + page + partials), the same fix applied to a different call site.
func TestRenderWorksFromAnyWorkingDirectory(t *testing.T) {
	chdirTemp(t)
	InitI18n(nil, "en")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/pin", nil)
	Render("ui/pages/pin.html", map[string]any{
		"title":     "Change PIN",
		"theme":     "",
		"menuItems": nil,
		"errKey":    "",
	})(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 rendering a full page from an unrelated CWD, got %d: %s", w.Code, w.Body.String())
	}
}

func TestResolveLocaleQueryParamPrecedence(t *testing.T) {
	InitI18n(nil, "en")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/?lang=fr", nil)
	r.AddCookie(&http.Cookie{Name: "ut_lang", Value: LocaleOverrideValue("en")})

	locale := ResolveLocale(w, r)
	if locale != "fr" {
		t.Fatalf("expected locale 'fr', got %q", locale)
	}
	res := w.Result()
	found := false
	for _, c := range res.Cookies() {
		if c.Name == "ut_lang" {
			found = true
			// Not a bare "fr", since ut-docs#2135: the cookie also records
			// what the choice was made against, so a later shop-level
			// language change can tell this preference apart from a stale
			// one. See LocaleOverride.
			if want := LocaleOverrideValue("fr"); c.Value != want {
				t.Fatalf("cookie value = %q; want %q", c.Value, want)
			}
		}
	}
	if !found {
		t.Fatalf("ut_lang cookie not set")
	}
}

func TestResolveLocaleCookieFallback(t *testing.T) {
	InitI18n(nil, "en")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	// Built through LocaleOverrideValue rather than written bare, since
	// ut-docs#2135 — an override is honoured only while what it was
	// recorded against still stands. A bare value is a pre-#2135 cookie and
	// is deliberately ignored; that case has its own test in
	// locale_override_test.go.
	r.AddCookie(&http.Cookie{Name: "ut_lang", Value: LocaleOverrideValue("fa")})

	locale := ResolveLocale(w, r)
	if locale != "fa" {
		t.Fatalf("expected locale 'fa', got %q", locale)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatalf("unexpected cookies set: %v", w.Result().Cookies())
	}
}

func TestFuncsForExposesMoneyAndI18n(t *testing.T) {
	InitCurrency("EUR")
	InitI18n(realI18n(t), "en")

	// money is locale-bound now: an en locale keeps Latin digits…
	funcs := FuncsFor("en")
	moneyFn, ok := funcs["money"].(func(any) string)
	if !ok {
		t.Fatalf("money helper not found")
	}
	if got := moneyFn(int64(12345)); got != "€123.45" {
		t.Fatalf("money helper returned %q", got)
	}
	if got := moneyFn(moneypkg.FromMinor(12345)); got != "€123.45" {
		t.Fatalf("money helper (Money) returned %q", got)
	}
	// …and a fa locale renders the same amount with Persian digits.
	faMoney := FuncsFor("fa")["money"].(func(any) string)
	if got := faMoney(int64(12345)); got != "€۱۲۳٫۴۵" {
		t.Fatalf("fa money helper returned %q", got)
	}
}

// ut-docs#1130: date/dateUTC accept both a time.Time and an RFC3339
// string, follow locale's date-order/separator convention via
// FormatDate, and an unparseable string degrades to itself (not "").
// dateUTC differs from date ONLY in skipping the Local() conversion —
// verified here by temporarily moving the process's local zone away from
// UTC, since in a UTC test runner (the common case) the two would be
// indistinguishable by output alone.
func TestFuncsForExposesDate(t *testing.T) {
	InitI18n(realI18n(t), "en")
	funcs := FuncsFor("de-DE")
	dateFn, ok := funcs["date"].(func(any) string)
	if !ok {
		t.Fatalf("date helper not found")
	}
	dateUTCFn, ok := funcs["dateUTC"].(func(string) string)
	if !ok {
		t.Fatalf("dateUTC helper not found")
	}

	orig := time.Local
	// UTC+3, no DST — a fixed offset keeps this deterministic regardless
	// of when the test runs.
	time.Local = time.FixedZone("UTC+3", 3*60*60)
	t.Cleanup(func() { time.Local = orig })

	// 23:30 UTC on the 5th is 02:30 local on the 6th — date (Local) must
	// cross the day boundary; dateUTC must not.
	const ts = "2026-09-05T23:30:00Z"
	if got := dateFn(ts); got != "06.09.2026" {
		t.Errorf("date(%s) = %q, want 06.09.2026 (local day)", ts, got)
	}
	if got := dateUTCFn(ts); got != "05.09.2026" {
		t.Errorf("dateUTC(%s) = %q, want 05.09.2026 (UTC day)", ts, got)
	}

	// Unparseable input degrades to itself, not "" (ut-docs#1130 review
	// finding — a "2006-01-02"-shaped or otherwise non-RFC3339 string must
	// stay visible, not silently vanish).
	if got := dateFn("2026-09-05"); got != "2026-09-05" {
		t.Errorf("date(non-RFC3339) = %q, want the raw string back", got)
	}
	if got := dateUTCFn("2026-09-05"); got != "2026-09-05" {
		t.Errorf("dateUTC(non-RFC3339) = %q, want the raw string back", got)
	}
}

// ut-docs#1632: datetime/datetimeUTC mirror date/dateUTC exactly (same
// accepted-value contract, same Local-vs-UTC split, same degrade-to-self
// on an unparseable string) plus a 24-hour clock via FormatDateTime.
func TestFuncsForExposesDateTime(t *testing.T) {
	InitI18n(realI18n(t), "en")
	funcs := FuncsFor("de-DE")
	dtFn, ok := funcs["datetime"].(func(any) string)
	if !ok {
		t.Fatalf("datetime helper not found")
	}
	dtUTCFn, ok := funcs["datetimeUTC"].(func(string) string)
	if !ok {
		t.Fatalf("datetimeUTC helper not found")
	}

	orig := time.Local
	time.Local = time.FixedZone("UTC+3", 3*60*60)
	t.Cleanup(func() { time.Local = orig })

	// 23:30 UTC on the 5th is 02:30 local on the 6th — datetime (Local)
	// must cross the day boundary; datetimeUTC must not.
	const ts = "2026-09-05T23:30:00Z"
	if got := dtFn(ts); got != "06.09.2026 02:30" {
		t.Errorf("datetime(%s) = %q, want 06.09.2026 02:30 (local)", ts, got)
	}
	if got := dtUTCFn(ts); got != "05.09.2026 23:30" {
		t.Errorf("datetimeUTC(%s) = %q, want 05.09.2026 23:30 (UTC)", ts, got)
	}

	if got := dtFn("2026-09-05"); got != "2026-09-05" {
		t.Errorf("datetime(non-RFC3339) = %q, want the raw string back", got)
	}
	if got := dtUTCFn("2026-09-05"); got != "2026-09-05" {
		t.Errorf("datetimeUTC(non-RFC3339) = %q, want the raw string back", got)
	}
}

// ut-docs#2162: RenderContentFragment/RenderPartial set X-UT-Page-Title
// from the template data's "title" so a shared client-side htmx:afterSwap
// listener (web/public/app.js) can refresh document.title after an
// in-panel swap — base.html's own <title> only ever renders on a
// full-page response, never on a fragment. Percent-encoded: browsers'
// Fetch/XHR getResponseHeader() reads header values as Latin-1, not
// UTF-8, so a raw non-ASCII title (this product ships ar/fa/tr locales)
// would come back corrupted without encoding.
// TestRenderWithContentSetsPageTitleHeader guards a real gap found while
// testing ut-docs#2162's fix: /catalog — the /items rail's own DEFAULT
// section, the one every plain visit to /items actually lands on — does
// NOT go through RenderContentFragment at all. catalog/handlers.go builds
// its own file set (extra partials RenderContentFragment's fixed set
// doesn't include) and calls the lower-level httpx.RenderWith(...)("content",
// data) directly for its fragment-swap branch. Both must carry the header,
// or the single most-visited /items section would silently be the one
// exception to this whole fix.
func TestRenderWithContentSetsPageTitleHeader(t *testing.T) {
	chdirTemp(t)
	InitI18n(nil, "en")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/catalog", nil)
	render := RenderWith([]string{
		"web/ui/layouts/base.html",
		"web/ui/pages/setup.html",
	}, FuncsFor("en"))
	render("content", map[string]any{
		"title":     "Catalog",
		"countries": nil,
		"errKey":    "",
	})(w, r)

	if got := w.Header().Get("X-UT-Page-Title"); got != "Catalog" {
		t.Fatalf("X-UT-Page-Title = %q, want %q", got, "Catalog")
	}
}

// TestRenderWithBaseOmitsPageTitleHeader: the "base" (full standalone page)
// execution already gets its <title> from base.html itself — the header
// is specifically for the fragment path, so "base" must not also set it.
func TestRenderWithBaseOmitsPageTitleHeader(t *testing.T) {
	chdirTemp(t)
	InitI18n(nil, "en")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/catalog", nil)
	render := RenderWith([]string{
		"web/ui/layouts/base.html",
		"web/ui/pages/setup.html",
	}, FuncsFor("en"))
	render("base", map[string]any{
		"title":     "Catalog",
		"theme":     "",
		"menuItems": nil,
		"countries": nil,
		"errKey":    "",
	})(w, r)

	if got := w.Header().Get("X-UT-Page-Title"); got != "" {
		t.Fatalf("X-UT-Page-Title = %q, want unset on a base/full-page render", got)
	}
}

func TestRenderContentFragmentSetsPageTitleHeader(t *testing.T) {
	chdirTemp(t)
	InitI18n(nil, "en")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/inventory", nil)
	r.Header.Set("HX-Request", "true")
	RenderContentFragment("ui/pages/inventory.html", map[string]any{
		"title":       "Inventory",
		"theme":       "",
		"menuItems":   nil,
		"StockLevels": nil,
		"RunningOut":  nil,
		"Locations":   nil,
	})(w, r)

	if got := w.Header().Get("X-UT-Page-Title"); got != "Inventory" {
		t.Fatalf("X-UT-Page-Title = %q, want %q", got, "Inventory")
	}
}

// TestRenderPartialSetsPageTitleHeaderWhenPresent covers help_page.go's
// own call shape (a topic's real, locale-resolved title, not an English
// literal) and confirms non-ASCII survives round-trip percent-encoding.
//
// Decoded with url.PathUnescape, not url.QueryUnescape: the real client
// side (web/public/app.js) decodes with JavaScript's decodeURIComponent,
// which only unescapes "%XX" sequences and leaves a literal "+"
// untouched — the same contract url.PathUnescape has (and url.QueryUnescape
// does not: it turns "+" back into a space). Asserting through PathUnescape
// is what would have caught the "+"-for-space bug TestRenderPartialTitle
// WithSpaceRoundTripsForClientDecoding below pins directly.
func TestRenderPartialSetsPageTitleHeaderWhenPresent(t *testing.T) {
	chdirTemp(t)
	InitI18n(nil, "en")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/setup", nil)
	RenderPartial("ui/pages/setup.html", map[string]any{
		"title":     "کاتالوگ", // Farsi "Catalog" — exercises the encoding, not a real setup-page title
		"countries": nil,
		"errKey":    "",
	})(w, r)

	encoded := w.Header().Get("X-UT-Page-Title")
	if encoded == "" {
		t.Fatal("X-UT-Page-Title header not set")
	}
	decoded, err := url.PathUnescape(encoded)
	if err != nil {
		t.Fatalf("X-UT-Page-Title %q did not decode: %v", encoded, err)
	}
	if decoded != "کاتالوگ" {
		t.Fatalf("X-UT-Page-Title round-tripped to %q, want the original Farsi string", decoded)
	}
}

// TestRenderPartialTitleWithSpaceRoundTripsForClientDecoding: review finding
// for ut-docs#2162 — writePageTitleHeader originally used url.QueryEscape,
// which encodes a space as "+" (application/x-www-form-urlencoded
// convention). The client only ever decodes with JavaScript's
// decodeURIComponent (web/public/app.js's htmx:afterSwap listener), which
// does NOT turn "+" back into a space — so every multi-word title in this
// diff ("Country settings", "Fiscal register", "Tax codes", "Customization
// options", "Option sets", …) would have shown up in the browser tab with
// a literal "+" instead of a space. Several real handlers' "title" values
// contain a space (see e.g. country_settings_page.go, fiscal_register_page.go,
// tax_codes_page.go), so this is not a hypothetical edge case. Simulates
// the client decode with url.PathUnescape (see the previous test's comment
// for why that, not url.QueryUnescape, is the correct stand-in).
func TestRenderPartialTitleWithSpaceRoundTripsForClientDecoding(t *testing.T) {
	chdirTemp(t)
	InitI18n(nil, "en")

	const want = "Country settings"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/setup", nil)
	RenderPartial("ui/pages/setup.html", map[string]any{
		"title":     want,
		"countries": nil,
		"errKey":    "",
	})(w, r)

	encoded := w.Header().Get("X-UT-Page-Title")
	if strings.Contains(encoded, "+") {
		t.Fatalf("X-UT-Page-Title = %q: contains a literal %q, which the client's decodeURIComponent would NOT turn back into a space", encoded, "+")
	}
	decoded, err := url.PathUnescape(encoded)
	if err != nil {
		t.Fatalf("X-UT-Page-Title %q did not decode: %v", encoded, err)
	}
	if decoded != want {
		t.Fatalf("X-UT-Page-Title round-tripped to %q, want %q", decoded, want)
	}
}

// TestRenderPartialOmitsPageTitleHeaderWhenAbsent guards the no-op path:
// the great majority of RenderPartial's call sites pass data with no
// "title" key at all (dialog messages, OOB refreshes, small in-place
// fragments) and must not grow a stray header.
func TestRenderPartialOmitsPageTitleHeaderWhenAbsent(t *testing.T) {
	chdirTemp(t)
	InitI18n(nil, "en")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/x", nil)
	RenderPartial("ui/partials/record_dialog_msg.html", map[string]any{"msg": "ok"})(w, r)

	if got := w.Header().Get("X-UT-Page-Title"); got != "" {
		t.Fatalf("X-UT-Page-Title = %q, want unset", got)
	}
}

// ut-docs#1130: thousandssep/decimalsep expose the same grouping
// convention FormatMoney uses server-side, for window.utCurrency's
// client-side formatter (web/public/app.js) to match it.
func TestFuncsForExposesNumberSeparators(t *testing.T) {
	InitI18n(realI18n(t), "en")
	de := FuncsFor("de-DE")
	if got := de["thousandssep"].(func() string)(); got != "." {
		t.Errorf("de-DE thousandssep = %q, want \".\"", got)
	}
	if got := de["decimalsep"].(func() string)(); got != "," {
		t.Errorf("de-DE decimalsep = %q, want \",\"", got)
	}
	gb := FuncsFor("en-GB")
	if got := gb["thousandssep"].(func() string)(); got != "," {
		t.Errorf("en-GB thousandssep = %q, want \",\"", got)
	}
	if got := gb["decimalsep"].(func() string)(); got != "." {
		t.Errorf("en-GB decimalsep = %q, want \".\"", got)
	}
}
