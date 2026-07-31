package httpx

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

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
