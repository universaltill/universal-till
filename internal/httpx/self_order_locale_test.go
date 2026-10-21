package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ut-docs#4200: a kiosk customer's language pick holds for the self-order
// routes only, and only while it names an installed language.
func TestRequestLocale_SelfOrderCookie(t *testing.T) {
	InitI18n(realI18n(t), "en")
	defer InitI18n(nil, "en")

	cases := []struct {
		name, path, cookie, query, want string
	}{
		{"start screen", "/self-order", "fa", "", "fa"},
		{"shop page", "/self-order/shop", "tr", "", "tr"},
		{"basket API", "/api/self-order/cart", "ar", "", "ar"},
		{"till screen ignores it", "/sale", "fa", "", "en"},
		{"lookalike path ignores it", "/self-orderx", "fa", "", "en"},
		{"lookalike API path ignores it", "/api/self-orders", "fa", "", "en"},
		{"not installed", "/self-order/shop", "xx", "", "en"},
		{"empty", "/self-order/shop", "", "", "en"},
		{"?lang= still wins", "/self-order/shop", "fa", "tr", "tr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := tc.path
			if tc.query != "" {
				url += "?lang=" + tc.query
			}
			r := httptest.NewRequest("GET", url, nil)
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: SelfOrderLocaleCookie, Value: tc.cookie})
			}
			if got := RequestLocale(r); got != tc.want {
				t.Fatalf("RequestLocale(%s, cookie %q) = %q, want %q", url, tc.cookie, got, tc.want)
			}
		})
	}
}

// The pick outranks the till's own per-browser ut_lang preference on the
// self-order routes, and leaves it alone everywhere else.
func TestRequestLocale_SelfOrderCookieBeatsUtLang(t *testing.T) {
	InitI18n(realI18n(t), "en")
	defer InitI18n(nil, "en")

	for path, want := range map[string]string{"/self-order/shop": "fa", "/sale": "tr"} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: "ut_lang", Value: LocaleOverrideValue("tr")})
		r.AddCookie(&http.Cookie{Name: SelfOrderLocaleCookie, Value: "fa"})
		if got := RequestLocale(r); got != want {
			t.Fatalf("RequestLocale(%s) = %q, want %q", path, got, want)
		}
	}
}

func TestSetAndClearSelfOrderLocale(t *testing.T) {
	w := httptest.NewRecorder()
	WriteSelfOrderLocale(w, "fa")
	c := w.Result().Cookies()
	if len(c) != 1 || c[0].Name != SelfOrderLocaleCookie || c[0].Value != "fa" {
		t.Fatalf("set cookie = %+v", c)
	}
	// A browser-session cookie (no Max-Age/Expires), HttpOnly, path "/" so
	// /api/self-order/* receives it too.
	if c[0].MaxAge != 0 || !c[0].Expires.IsZero() || !c[0].HttpOnly || c[0].Path != "/" || c[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie attributes = %+v", c[0])
	}

	w = httptest.NewRecorder()
	ClearSelfOrderLocale(w)
	c = w.Result().Cookies()
	if len(c) != 1 || c[0].Name != SelfOrderLocaleCookie || c[0].MaxAge >= 0 {
		t.Fatalf("clear cookie = %+v", c)
	}
}
