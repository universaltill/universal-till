package pages

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
)

// selfOrderLang is the guest's current kiosk language pick, "" for none.
func (g *selfOrderGuest) selfOrderLang() string {
	for _, c := range g.jar.Cookies(g.cookieURL("/self-order")) {
		if c.Name == httpx.SelfOrderLocaleCookie {
			return c.Value
		}
	}
	return ""
}

// ut-docs#4200: the start screen lists every installed language by its own
// name, and one tap starts the order in it.
func TestSelfOrder_LanguagePicker_StartsOrderInPickedLanguage(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	g := newSelfOrderGuest(t, mux)

	start := g.get("/self-order")
	if start.Code != http.StatusOK {
		t.Fatalf("GET /self-order = %d", start.Code)
	}
	body := start.Body.String()
	for _, loc := range httpx.AvailableLocales() {
		link := `href="/self-order/language?code=` + loc + `" lang="` + loc + `"`
		i := strings.Index(body, link)
		if i < 0 {
			t.Fatalf("start screen missing %s: %s", link, body)
		}
		rest := body[i:]
		if name := ">" + httpx.NativeLanguageName(loc) + "</a>"; !strings.HasPrefix(rest[strings.Index(rest, ">"):], name) {
			t.Fatalf("%s's button should read its native name %q: %.200s", loc, name, rest)
		}
	}
	if !strings.Contains(body, `href="/self-order/language?code=en" lang="en" aria-current="true"`) {
		t.Fatalf("the shop's own language should be marked current: %s", body)
	}

	pick := g.get("/self-order/language?code=fa")
	if pick.Code != http.StatusSeeOther || pick.Header().Get("Location") != "/self-order/shop" {
		t.Fatalf("pick = %d → %q, want 303 → /self-order/shop", pick.Code, pick.Header().Get("Location"))
	}
	if got := g.selfOrderLang(); got != "fa" {
		t.Fatalf("pick cookie = %q, want fa", got)
	}

	shop := g.get("/self-order/shop")
	if shop.Code != http.StatusOK {
		t.Fatalf("GET /self-order/shop = %d", shop.Code)
	}
	if !strings.Contains(shop.Body.String(), `<html lang="fa" dir="rtl"`) {
		t.Fatalf("shop page not in the picked language: %.300s", shop.Body.String())
	}
	// The basket API (a different path prefix) follows the pick too.
	cart := g.get("/api/self-order/cart")
	want := httpx.T("fa", "selforder.order_type.takeaway")
	if want == httpx.T("en", "selforder.order_type.takeaway") {
		t.Fatalf("fixture: fa.json must translate selforder.order_type.takeaway")
	}
	if cart.Code != http.StatusOK || !strings.Contains(cart.Body.String(), want) {
		t.Fatalf("cart partial not in fa (want %q): %d %s", want, cart.Code, cart.Body.String())
	}

	// The walk-up start screen — where the idle timeout, the confirmation
	// screen's auto-return and Back all land — ends the pick.
	again := g.get("/self-order")
	if g.selfOrderLang() != "" {
		t.Fatalf("walk-up start screen kept the pick")
	}
	if !strings.Contains(again.Body.String(), `<html lang="en"`) {
		t.Fatalf("start screen after reset not in the shop's language: %.200s", again.Body.String())
	}
}

// A code that isn't an installed language is never stored.
func TestSelfOrder_LanguagePicker_RefusesUnknownCode(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	g := newSelfOrderGuest(t, mux)

	for _, code := range []string{"xx", "", "fa|en|0", "../en"} {
		rec := g.get("/self-order/language?code=" + code)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/self-order" {
			t.Fatalf("code %q = %d → %q, want 303 → /self-order", code, rec.Code, rec.Header().Get("Location"))
		}
		if got := g.selfOrderLang(); got != "" {
			t.Fatalf("code %q stored %q", code, got)
		}
	}
}

// A guest on their own phone (table QR) keeps their pick when the idle
// reset brings them back to /self-order?table=<id>.
func TestSelfOrder_LanguagePicker_TableLandingKeepsPick(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	tableID := createSelfOrderTable(t, dp, "T7", 100)
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	g := newSelfOrderGuest(t, mux)

	g.get("/self-order?table=" + tableID)
	g.get("/self-order/language?code=tr")
	rec := g.get("/self-order?table=" + tableID)
	if got := g.selfOrderLang(); got != "tr" {
		t.Fatalf("table landing dropped the pick: %q", got)
	}
	if !strings.Contains(rec.Body.String(), `<html lang="tr"`) {
		t.Fatalf("table landing not in the picked language: %.200s", rec.Body.String())
	}
}

// One installed language: nothing to pick, so no picker.
func TestSelfOrder_LanguagePicker_HiddenWithOneLanguage(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	dir := t.TempDir()
	en, err := os.ReadFile(filepath.Join("web", "locales", "en.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "en.json"), en, 0o600); err != nil {
		t.Fatal(err)
	}
	only, err := config.NewI18n(dir, "en")
	if err != nil {
		t.Fatal(err)
	}
	httpx.InitI18n(only, "en")

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	rec := newSelfOrderGuest(t, mux).get("/self-order")
	if strings.Contains(rec.Body.String(), "/self-order/language") {
		t.Fatalf("picker shown with one language: %s", rec.Body.String())
	}
}

// The shop's language is often a regional tag (en-GB) while the installed
// codes are bare (en): the matching language is still the current one —
// found driving the e2e till, whose shop locale is regional.
func TestSelfOrderLanguages_MarksRegionalShopLocale(t *testing.T) {
	setupSelfOrderShopDeps(t)
	for current, want := range map[string]string{"en-GB": "en", "en": "en", "fa-IR": "fa", "de-DE": ""} {
		var got []string
		for _, l := range selfOrderLanguages(current) {
			if l.Current {
				got = append(got, l.Code)
			}
		}
		if (want == "" && len(got) != 0) || (want != "" && (len(got) != 1 || got[0] != want)) {
			t.Fatalf("selfOrderLanguages(%q) current = %v, want %q", current, got, want)
		}
	}
}

// review of #4200: a ?table= that binds nothing (unknown or disabled table)
// is the walk-up path — the basket resets, so the pick must go too.
func TestSelfOrder_LanguagePicker_UnboundTableLandingClearsPick(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	g := newSelfOrderGuest(t, mux)

	g.get("/self-order/language?code=fa")
	g.get("/self-order?table=no-such-table")
	if got := g.selfOrderLang(); got != "" {
		t.Fatalf("unbound table landing kept the pick %q", got)
	}
}

// review of #4200: picking the shop's own language (shop en-GB, button en)
// drops the pick rather than storing the bare tag, so money and number
// formatting keep the shop's regional tag.
func TestSelfOrder_LanguagePicker_ShopLanguageClearsPick(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	httpx.SetDefaultLocale("en-GB")
	t.Cleanup(func() { httpx.SetDefaultLocale("en") })
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	g := newSelfOrderGuest(t, mux)

	g.get("/self-order/language?code=fa")
	rec := g.get("/self-order/language?code=en")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/self-order/shop" {
		t.Fatalf("pick = %d → %q, want 303 → /self-order/shop", rec.Code, rec.Header().Get("Location"))
	}
	if got := g.selfOrderLang(); got != "" {
		t.Fatalf("shop language stored as a pick: %q", got)
	}
}
