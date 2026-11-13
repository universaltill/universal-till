package pages

// ut-docs#6042: the category dialog's icon picker carries search text in
// the shop's languages (data-keywords).

import (
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func categoriesCoffeeKeywords(t *testing.T, def, staff, path string, overrides map[string]map[string]string) string {
	t.Helper()
	mux, d := newCategoriesTestMux(t)
	restore := httpx.SnapshotStateForTests()
	t.Cleanup(restore)
	i, err := config.NewI18n("web/locales", "en")
	if err != nil {
		t.Fatal(err)
	}
	if overrides != nil {
		i.SetShopOverrides(overrides)
	}
	httpx.InitI18n(i, def)
	if staff != "" {
		if err := d.Settings.Set(t.Context(), common.KeyStaffLocales, staff); err != nil {
			t.Fatal(err)
		}
	}
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, auth.WithUser(httptest.NewRequest(http.MethodGet, path, nil), manager))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, rec.Code)
	}
	tag := regexp.MustCompile(`<button[^>]*data-icon="coffee"[^>]*>`).FindString(rec.Body.String())
	if tag == "" {
		t.Fatal("no coffee tile")
	}
	m := regexp.MustCompile(`data-keywords="([^"]*)"`).FindStringSubmatch(tag)
	if m == nil {
		t.Fatalf("no data-keywords: %s", tag)
	}
	return m[1]
}

func kwHas(raw, w string) bool {
	for _, f := range strings.Fields(html.UnescapeString(raw)) {
		if f == w {
			return true
		}
	}
	return false
}

func TestCategoriesIconPicker_TurkishShopUIEnglish(t *testing.T) {
	kw := categoriesCoffeeKeywords(t, "tr", "", "/categories?lang=en", nil)
	if !kwHas(kw, "kahve") || !kwHas(kw, "coffee") {
		t.Errorf("tr shop, UI en: %q", kw)
	}
}

func TestCategoriesIconPicker_ArabicShopCharacters(t *testing.T) {
	kw := categoriesCoffeeKeywords(t, "ar", "", "/categories?lang=en", nil)
	if !strings.Contains(kw, "قهوه") || strings.Contains(kw, "&#") {
		t.Errorf("ar shop: %q", kw)
	}
}

func TestCategoriesIconPicker_EnglishShopNoTurkishAndOverrideEscaped(t *testing.T) {
	if kw := categoriesCoffeeKeywords(t, "en", "", "/categories", nil); kwHas(kw, "kahve") {
		t.Errorf("english shop leaked kahve: %q", kw)
	}
	kw := categoriesCoffeeKeywords(t, "en", "", "/categories", map[string]map[string]string{"en": {"catalog.builtin_icon.coffee": `Filtre "Kahve" <b>`}})
	if !kwHas(kw, "filtre") || strings.ContainsAny(kw, `"<>`) {
		t.Errorf("override: %q", kw)
	}
}
