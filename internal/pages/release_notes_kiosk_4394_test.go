package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/releasenotes"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#4394: the after-update chip is not withheld by the till's display
// mode (a manager on a self-order till must see it), so the
// customer-facing kiosk must stay safe structurally: its templates never
// render the chip and never include base.html's status bar.
func TestReleaseNotesChip_CustomerKioskTemplatesNeverRenderIt(t *testing.T) {
	var files []string
	for _, pat := range []string{"web/ui/pages/self_order*.html", "web/ui/partials/self_order*.html"} {
		m, err := filepath.Glob(pat) // TestMain chdirs to the repo root
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) < 2 {
		t.Fatalf("found %d self-order templates, want >= 2 — has the kiosk moved? (%v)", len(files), files)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range []string{"releasenoticeversion", "sb-release-notes"} {
			if strings.Contains(string(b), needle) {
				t.Errorf("%s contains %q: the customer-facing kiosk must never render the release-notes chip (ut-docs#4394)", f, needle)
			}
		}
	}
}

// The rendered kiosk page, anonymous, with a notice pending and self-order
// mode on (scaffold copied from TestSelfOrderPage_ServesAnonymousRequest).
func TestReleaseNotesChip_NotOnRenderedSelfOrderPage(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	httpx.InitDisplayMode("self_order")
	t.Cleanup(func() { httpx.InitDisplayMode("") })
	releasenotes.SetNoticeVersion("v9.9.9")
	t.Cleanup(func() { releasenotes.SetNoticeVersion("") })

	cfg := &config.Config{Theme: "default", StoreName: "Task Runner Cafe"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	h := auth.Middleware(mux, auth.NewService(db))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sb-release-notes") {
		t.Error("the customer-facing /self-order page rendered the release-notes chip")
	}
}
