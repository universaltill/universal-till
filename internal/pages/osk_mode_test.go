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

// The OSK mode reaches the page as body[data-osk]: default auto, forced on
// after the settings change (the field bug was "keyboard never shows" — on
// non-touch machines auto hides it by design, so "on" must truly force it).
func TestOSKModeReachesThePage(t *testing.T) {
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
	registerHelp(mux, dp) // any base-layout page carries data-osk
	registerSettings(mux, dp)

	get := func() string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/help", nil))
		return rec.Body.String()
	}
	httpx.InitOSKMode(state.OSKMode)
	if !strings.Contains(get(), `data-osk="auto"`) {
		t.Fatal("default OSK mode should be auto")
	}

	// Operator forces it ON via the settings endpoint.
	form := strings.NewReader("mode=on")
	req := httptest.NewRequest(http.MethodPost, "/api/settings/osk", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("set osk on: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(get(), `data-osk="on"`) {
		t.Fatal("OSK mode 'on' did not reach the page")
	}
}
