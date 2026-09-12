package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#2183: index.html, admin.html and items.html each used to include
// {{ template "pos_alert" . }} directly in their own content block
// (ut-docs#2179). base.html now declares the shared #pos-alert element
// once, itself, for every page — so each of these three pages' own copy
// was removed. This pins that none of them regressed to rendering it
// TWICE (base.html's copy plus a leftover page-local one): a duplicate
// id="pos-alert" is invalid HTML, and getElementById's behaviour on a
// duplicate id is unspecified, which would silently break app.js's
// targeting on exactly the pages the original mechanism (ut-docs#213) was
// built for.
func TestPosAlert_ExactlyOnceOnIndexPage(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerIndex(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d: %s", rec.Code, rec.Body.String())
	}
	assertExactlyOnePosAlert(t, rec.Body.String())
}

func TestPosAlert_ExactlyOnceOnAdminPage(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newAdminPageTestDeps(t)
	rec := getAdmin(t, mux, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin = %d: %s", rec.Code, rec.Body.String())
	}
	assertExactlyOnePosAlert(t, rec.Body.String())
}

func TestPosAlert_ExactlyOnceOnItemsPage(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	registerItemsPage(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /items = %d: %s", rec.Code, rec.Body.String())
	}
	assertExactlyOnePosAlert(t, rec.Body.String())
}

func assertExactlyOnePosAlert(t *testing.T, body string) {
	t.Helper()
	if got := strings.Count(body, `id="pos-alert"`); got != 1 {
		t.Fatalf("want exactly 1 #pos-alert element (base.html's single copy, no leftover page-local one), got %d\n%.2000s", got, body)
	}
}
