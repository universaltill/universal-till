package httpx

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	config "github.com/universaltill/universal-till/internal/config"
	moneypkg "github.com/universaltill/universal-till/internal/money"
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
	i18n, err := config.NewI18n(locales, "en")
	if err != nil {
		t.Fatalf("NewI18n: %v", err)
	}
	InitI18n(i18n, "en")

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
