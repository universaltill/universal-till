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

// The Help page renders the expandable feature guide: one <details> per
// feature, each with its explanation and usage steps, fully translated
// (a raw locale key in the output means a missing translation).
func TestHelpPageRendersFeatureGuide(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Settings: settings.NewStore(db),
	}

	mux := http.NewServeMux()
	registerHelp(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/help", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /help: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if got := strings.Count(body, "<details class=\"feature\">"); got != len(helpFeatureIDs) {
		t.Fatalf("feature accordions = %d, want %d", got, len(helpFeatureIDs))
	}
	// A few translated strings prove the keys resolve end-to-end (en default).
	for _, want := range []string{"Selling &amp; checkout", "Plugin store", "Claim this store", "Backups"} {
		if !strings.Contains(body, want) {
			t.Fatalf("help page missing %q", want)
		}
	}
	// Every feature key must resolve — the translator falls back to the raw
	// key when a locale entry is missing, which would leak into the page.
	if strings.Contains(body, "help.feat.") {
		i := strings.Index(body, "help.feat.")
		t.Fatalf("unresolved locale key on the help page near: %s", body[i:i+40])
	}
}
