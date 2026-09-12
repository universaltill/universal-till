package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ut-docs#2135: a `ut_lang` cookie set once — by clicking through /setup in
// English, or by any ?lang= link — used to pin a till to that language for a
// YEAR, silently overriding the shop's configured locale on every page, with
// no control anywhere in the UI able to clear it. A German pilot shop ran an
// English sale screen for weeks because of it, and it was first misdiagnosed
// as a missing translation.
//
// The fix records, inside the cookie, the two things an override is only
// valid relative to: the shop default at the time, and the shop's locale
// GENERATION at the time. These tests pin that contract.

func localeState(t *testing.T, def string, gen int64) {
	t.Helper()
	InitI18n(nil, def)
	SetLocaleGeneration(gen)
	t.Cleanup(func() { InitI18n(nil, "en"); SetLocaleGeneration(0) })
}

func TestLocaleOverrideHonouredWhileNothingHasMoved(t *testing.T) {
	localeState(t, "de", 3)
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "ut_lang", Value: LocaleOverrideValue("en")})

	if got := RequestLocale(r); got != "en" {
		t.Fatalf("RequestLocale = %q, want %q — a manager's own per-browser "+
			"language choice must still work while nothing shop-level has "+
			"changed under it", got, "en")
	}
}

func TestLocaleOverrideIgnoredOnceShopDefaultChanged(t *testing.T) {
	// Recorded while the shop was still English; the shop is now German.
	localeState(t, "en", 3)
	value := LocaleOverrideValue("en")
	localeState(t, "de", 3)

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "ut_lang", Value: value})
	if got := RequestLocale(r); got != "de" {
		t.Fatalf("RequestLocale = %q, want %q — an override recorded against a "+
			"superseded shop default must not outlive it (ut-docs#2135)", got, "de")
	}
}

// The generation is what makes Settings → Language authoritative for the whole
// shop. Re-applying the language the shop is ALREADY set to leaves the default
// unmoved, so without this the override would stand — and that is exactly the
// state a confused operator is in. It is also what lets a manager fix a till
// from their own phone, which a Set-Cookie on the responding request cannot do.
func TestLocaleOverrideIgnoredOnceTheShopRestatesItsLanguage(t *testing.T) {
	localeState(t, "de", 3)
	value := LocaleOverrideValue("en")

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "ut_lang", Value: value})
	if got := RequestLocale(r); got != "en" {
		t.Fatalf("precondition: RequestLocale = %q, want en", got)
	}

	SetLocaleGeneration(4) // the shop explicitly applied its language again
	if got := RequestLocale(r); got != "de" {
		t.Fatalf("RequestLocale = %q, want %q — an explicit shop-level choice "+
			"must retire overrides on EVERY browser, not only the one it was "+
			"made from (ut-docs#2135)", got, "de")
	}
}

// The pilot tablet's actual state: a bare pre-fix cookie, no recorded context,
// shop configured for German, sale screen rendering English. Such a cookie
// carries no evidence of what it was chosen against, so it cannot be trusted
// over the shop's explicit setting — the till heals itself on upgrade rather
// than waiting for an operator to find a control that did not work.
func TestLegacyLocaleOverrideIsIgnored(t *testing.T) {
	localeState(t, "de", 0)
	for _, value := range []string{"en", "en|de", "en|de|", "|de|0", "en|de|notanumber"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: "ut_lang", Value: value})
		if got := RequestLocale(r); got != "de" {
			t.Errorf("ut_lang=%q: RequestLocale = %q, want %q — a malformed or "+
				"pre-ut-docs#2135 cookie must not override the shop setting",
				value, got, "de")
		}
	}
}

// A ?lang= value carrying the separator must not be able to forge the fields
// that decide whether the override is still live.
func TestLocaleOverrideCannotBeForgedThroughTheQueryParam(t *testing.T) {
	localeState(t, "de", 7)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/?lang=en|de|7", nil)
	ResolveLocale(w, r)

	forged := cookieNamed(t, w, "ut_lang")
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.AddCookie(&http.Cookie{Name: "ut_lang", Value: forged.Value})
	if got := RequestLocale(r2); got != "de" {
		t.Fatalf("RequestLocale = %q, want %q — a ?lang= value containing the "+
			"separator must not survive as a valid override", got, "de")
	}
}

// Pins the whole precedence chain so a future change cannot silently
// reintroduce cookie-over-shop-default (acceptance criterion 5 on #2135).
func TestRequestLocalePrecedenceIsPinned(t *testing.T) {
	localeState(t, "de", 1)

	// 1. ?lang= beats everything, including a still-valid override.
	r := httptest.NewRequest("GET", "/?lang=tr", nil)
	r.AddCookie(&http.Cookie{Name: "ut_lang", Value: LocaleOverrideValue("en")})
	if got := RequestLocale(r); got != "tr" {
		t.Fatalf("?lang= precedence: got %q, want %q", got, "tr")
	}

	// 2. A valid override beats the shop default.
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "ut_lang", Value: LocaleOverrideValue("en")})
	if got := RequestLocale(r); got != "en" {
		t.Fatalf("valid override precedence: got %q, want %q", got, "en")
	}

	// 3. The shop default beats the compiled-in fallback.
	r = httptest.NewRequest("GET", "/", nil)
	if got := RequestLocale(r); got != "de" {
		t.Fatalf("shop default precedence: got %q, want %q", got, "de")
	}

	// 4. With nothing configured at all, "en".
	localeState(t, "", 1)
	r = httptest.NewRequest("GET", "/", nil)
	if got := RequestLocale(r); got != "en" {
		t.Fatalf("fallback: got %q, want %q", got, "en")
	}
}

func TestResolveLocaleRecordsTheShopContextInTheCookie(t *testing.T) {
	localeState(t, "de", 5)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/?lang=en", nil)

	if got := ResolveLocale(w, r); got != "en" {
		t.Fatalf("ResolveLocale = %q, want %q", got, "en")
	}
	c := cookieNamed(t, w, "ut_lang")
	if c.Value != "en|de|5" {
		t.Fatalf("ut_lang = %q, want %q — the cookie must record the shop "+
			"default AND generation it was chosen against, or it cannot be "+
			"told apart from a stale one later", c.Value, "en|de|5")
	}
	if c.MaxAge <= 0 {
		t.Fatalf("ut_lang MaxAge = %d, want a positive lifetime", c.MaxAge)
	}
}

func cookieNamed(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q cookie was set; got %v", name, w.Result().Cookies())
	return nil
}
