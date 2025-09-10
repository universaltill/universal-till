package httpx

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/common"
)

func TestResolveLocaleQueryParamPrecedence(t *testing.T) {
	InitI18n(nil, "en")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/?lang=fr", nil)
	r.AddCookie(&http.Cookie{Name: "ut_lang", Value: "en"})

	locale := ResolveLocale(w, r)
	if locale != "fr" {
		t.Fatalf("expected locale 'fr', got %q", locale)
	}
	res := w.Result()
	found := false
	for _, c := range res.Cookies() {
		if c.Name == "ut_lang" {
			found = true
			if c.Value != "fr" {
				t.Fatalf("cookie value = %q; want 'fr'", c.Value)
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
	r.AddCookie(&http.Cookie{Name: "ut_lang", Value: "fa"})

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
	locales := filepath.Join("..", "..", "web", "locales")
	i18n, err := common.NewI18n(locales, "en")
	if err != nil {
		t.Fatalf("NewI18n: %v", err)
	}
	InitI18n(i18n, "en")

	funcs := FuncsFor("fa")

	moneyFn, ok := funcs["money"].(func(int64) string)
	if !ok {
		t.Fatalf("money helper not found")
	}
	if got := moneyFn(12345); got != "€123.45" {
		t.Fatalf("money helper returned %q", got)
	}

	tFn, ok := funcs["T"].(func(string) string)
	if !ok {
		t.Fatalf("T helper not found")
	}
	if got := tFn("app.name"); got != "صندوق فروش همگانی" {
		t.Fatalf("translation = %q", got)
	}
}
