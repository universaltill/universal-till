package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3086: the ☰ Menu's language row lists only the languages the shop
// chose for staff, not every installed one.

func menuBody(t *testing.T, mux *http.ServeMux, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestMenuLanguageRow_OnlySelectedStaffLanguages(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newMenuPageTestDeps(t, []common.MenuItem{{Href: "/inventory", Label: "nav.inventory"}})
	if err := d.Settings.Set(t.Context(), common.KeyStaffLocales, "en,tr"); err != nil {
		t.Fatal(err)
	}
	body := menuBody(t, mux, "/menu")
	for _, code := range []string{"en", "tr"} {
		if !strings.Contains(body, `href="/menu?lang=`+code+`"`) {
			t.Errorf("selected language %q missing from the Menu row", code)
		}
	}
	for _, code := range []string{"ar", "fa"} {
		if strings.Contains(body, `href="/menu?lang=`+code+`"`) {
			t.Errorf("unselected language %q shown in the Menu row", code)
		}
	}
}

func TestMenuLanguageRow_HiddenWithOneLanguage(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newMenuPageTestDeps(t, []common.MenuItem{{Href: "/inventory", Label: "nav.inventory"}})
	// Unset on an English shop = default (en) + English = one language.
	body := menuBody(t, mux, "/menu")
	if strings.Contains(body, `data-testid="menu-lang"`) || strings.Contains(body, "/menu?lang=") {
		t.Fatalf("an English-only shop should show no language row")
	}
}

func TestMenuLanguageRow_UnlistedLangStillWorksButIsNotAdded(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newMenuPageTestDeps(t, []common.MenuItem{{Href: "/inventory", Label: "nav.inventory"}})
	if err := d.Settings.Set(t.Context(), common.KeyStaffLocales, "en,tr"); err != nil {
		t.Fatal(err)
	}
	body := menuBody(t, mux, "/menu?lang=fa")
	if !strings.Contains(body, `dir="rtl"`) {
		t.Errorf("?lang=fa should still switch this browser to Farsi")
	}
	if strings.Contains(body, `href="/menu?lang=fa"`) {
		t.Errorf("?lang=fa must not add Farsi to the Menu row")
	}
}

func TestStaffLanguagesSave(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	t.Cleanup(func() { httpx.SetDefaultLocale("en") })

	// Persisted in installed order, default language included.
	rec := postForm(mux, "/api/settings/staff-languages", url.Values{"staff_locales": {"tr", "en"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyStaffLocales); v != "en,tr" {
		t.Fatalf("stored %s = %q, want en,tr", common.KeyStaffLocales, v)
	}

	refused := func(name string, form url.Values) {
		t.Helper()
		rec := postForm(mux, "/api/settings/staff-languages", form, &mgrUser)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", name, rec.Code)
		}
		if v, _, _ := d.Settings.Get(t.Context(), common.KeyStaffLocales); v != "en,tr" {
			t.Errorf("%s: stored value changed to %q", name, v)
		}
	}
	refused("default language left out", url.Values{"staff_locales": {"tr"}})
	refused("nothing selected", url.Values{})
	refused("unknown language", url.Values{"staff_locales": {"en", "xx"}})

	// Regional/case variants match the installed code, as on read.
	if rec := postForm(mux, "/api/settings/staff-languages", url.Values{"staff_locales": {"EN", "tr-TR"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("variant save = %d", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyStaffLocales); v != "en,tr" {
		t.Fatalf("variant save stored %q, want en,tr", v)
	}

	// The default is whatever the shop's language is now.
	httpx.SetDefaultLocale("tr")
	refused("new default left out", url.Values{"staff_locales": {"en"}})
	if rec := postForm(mux, "/api/settings/staff-languages", url.Values{"staff_locales": {"tr"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("default-only save = %d", rec.Code)
	}
}

func TestStaffLanguagesSave_CashierNeedsElevation(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	rec := postForm(mux, "/api/settings/staff-languages", url.Values{"staff_locales": {"en", "tr"}}, &cashUser)
	if rec.Code == http.StatusNoContent {
		t.Fatalf("a cashier saved staff languages without elevation")
	}
	if v, ok, _ := d.Settings.Get(t.Context(), common.KeyStaffLocales); ok && v != "" {
		t.Fatalf("stored %q after a cashier's unelevated save", v)
	}
}

func TestSettingsPage_StaffLanguagesCard(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	rec := getAs(mux, "/settings", &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, code := range []string{"ar", "en", "fa", "tr"} {
		if !strings.Contains(body, `data-testid="staff-lang-`+code+`"`) {
			t.Errorf("card must offer every installed language; %q missing", code)
		}
	}
	if !strings.Contains(body, `type="hidden" name="staff_locales" value="en"`) {
		t.Errorf("default language must be carried as a hidden input")
	}
}
