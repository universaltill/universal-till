package httpx

import (
	"net/http"
	"net/http/httptest"
	"os"
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
