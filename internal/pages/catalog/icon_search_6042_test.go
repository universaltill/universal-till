package catalog

// ut-docs#6042: the item editor's icon picker carries search text in the
// shop's languages (data-keywords), folded like internal/catimport.

import (
	"database/sql"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

var coffeeTileRe = regexp.MustCompile(`<button[^>]*data-icon="coffee"[^>]*>`)

// coffeeKeywordsRaw returns the coffee tile's data-keywords attribute exactly
// as emitted (escaped), failing if the tile is absent.
func coffeeKeywordsRaw(t *testing.T, body string) string {
	t.Helper()
	tag := coffeeTileRe.FindString(body)
	if tag == "" {
		t.Fatalf("no coffee tile in page")
	}
	m := regexp.MustCompile(`data-keywords="([^"]*)"`).FindStringSubmatch(tag)
	if m == nil {
		t.Fatalf("coffee tile has no data-keywords: %s", tag)
	}
	return m[1]
}

func shopSetup(t *testing.T, def, staff string, overrides map[string]map[string]string) (*http.ServeMux, *sql.DB) {
	t.Helper()
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
	mux, db := newCatalogMux(t)
	if staff != "" {
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
			t.Fatal(err)
		}
		if err := data.NewSettingsRepo(db).Set(t.Context(), common.KeyStaffLocales, staff); err != nil {
			t.Fatal(err)
		}
	}
	return mux, db
}

func hasKW(raw, w string) bool {
	for _, f := range strings.Fields(html.UnescapeString(raw)) {
		if f == w {
			return true
		}
	}
	return false
}

func TestCatalogIconPicker_SearchTextInShopLanguages(t *testing.T) {
	mux, _ := shopSetup(t, "en", "en,tr", nil)
	rec := get(t, mux, "/catalog?lang=en")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d", rec.Code)
	}
	if kw := coffeeKeywordsRaw(t, rec.Body.String()); !hasKW(kw, "kahve") || !hasKW(kw, "coffee") {
		t.Errorf("en+tr shop, UI en: coffee keywords lack kahve/coffee: %q", kw)
	}
}

func TestCatalogIconPicker_EnglishShopHasNoTurkish(t *testing.T) {
	mux, _ := shopSetup(t, "en", "", nil)
	rec := get(t, mux, "/catalog")
	if kw := coffeeKeywordsRaw(t, rec.Body.String()); hasKW(kw, "kahve") || !hasKW(kw, "latte") {
		t.Errorf("english shop keywords wrong: %q", kw)
	}
}

func TestCatalogIconPicker_ArabicShopEmitsCharactersNotEntities(t *testing.T) {
	mux, _ := shopSetup(t, "ar", "", nil)
	rec := get(t, mux, "/catalog?lang=en")
	kw := coffeeKeywordsRaw(t, rec.Body.String())
	if !strings.Contains(kw, "قهوه") {
		t.Errorf("ar shop: coffee keywords lack قهوه as characters: %q", kw)
	}
	if strings.Contains(kw, "&#") {
		t.Errorf("non-Latin text emitted as numeric entities: %q", kw)
	}
}

func TestCatalogIconPicker_ShopOverrideSearchableAndEscaped(t *testing.T) {
	mux, _ := shopSetup(t, "tr", "", map[string]map[string]string{"tr": {"catalog.builtin_icon.coffee": `Filtre "Kahve" <b>`}})
	rec := get(t, mux, "/catalog")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d", rec.Code)
	}
	kw := coffeeKeywordsRaw(t, rec.Body.String())
	if !hasKW(kw, "filtre") {
		t.Errorf("override not searchable: %q", kw)
	}
	if strings.ContainsAny(kw, `"<>`) {
		t.Errorf("attribute not escaped: %q", kw)
	}
}

// The catalog test DB has no settings table, so the staff-locales read
// fails: the page must still render (200) with the default language's labels.
func TestCatalogIconPicker_SettingsReadFailureStillRenders(t *testing.T) {
	mux, _ := shopSetup(t, "tr", "", nil)
	rec := get(t, mux, "/catalog")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d", rec.Code)
	}
	if kw := coffeeKeywordsRaw(t, rec.Body.String()); !hasKW(kw, "kahve") {
		t.Errorf("default language missing after settings failure: %q", kw)
	}
}
