package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#3710: the sale-screen guided tour.

func newTourTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	cfg := &config.Config{Theme: "default"}
	store := settings.NewStore(db)
	d := &common.Deps{Cfg: cfg, Db: db, State: common.LoadState(t.Context(), store, cfg),
		Menu: []common.MenuItem{}, Settings: store, AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerIndex(mux, d)
	return mux, d
}

func tourSetting(t *testing.T, d *common.Deps, userID string) string {
	t.Helper()
	v, _, err := d.Settings.Get(context.Background(), data.TourDoneSettingsKey(userID))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTourDone_WritesOnlyTheCallersKey(t *testing.T) {
	mux, d := newTourTestMux(t)
	// A user_id in the body must be ignored: the key is always the session's.
	rec := postFormHtmx(mux, "/api/tour/done", url.Values{"user_id": {"m1"}}, &cashUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/tour/done = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":null`) {
		t.Errorf("want the {data, error} envelope, got %s", rec.Body.String())
	}
	if tourSetting(t, d, cashUser.ID) == "" {
		t.Errorf("caller's key %q not written", data.TourDoneSettingsKey(cashUser.ID))
	}
	if got := tourSetting(t, d, mgrUser.ID); got != "" {
		t.Errorf("another user's key was written: %q", got)
	}
	all, err := d.Settings.GetByPrefix(context.Background(), "tour.")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("want exactly one tour.* key, got %v", all)
	}
}

func TestTourDone_UnauthenticatedRefused(t *testing.T) {
	mux, d := newTourTestMux(t)
	rec := postFormHtmx(mux, "/api/tour/done", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous POST = %d, want 401", rec.Code)
	}
	all, err := d.Settings.GetByPrefix(context.Background(), "tour.")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("a refused call wrote %v", all)
	}
}

func TestTourDone_GetNotAllowed(t *testing.T) {
	mux, _ := newTourTestMux(t)
	rec := getWithUser(mux, "/api/tour/done", &cashUser)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/tour/done must not mark the tour done (prefetch/link preview), got %d", rec.Code)
	}
}

// tourStartAttr is what index.html renders on the steps element.
func tourStarts(t *testing.T, body string) bool {
	t.Helper()
	if !strings.Contains(body, `id="ut-tour-steps"`) {
		t.Fatalf("sale screen has no tour steps element")
	}
	switch {
	case strings.Contains(body, `data-start="1"`):
		return true
	case strings.Contains(body, `data-start="0"`):
		return false
	}
	t.Fatalf("tour steps element has no data-start flag")
	return false
}

func TestIndex_TourAutostartsOnlyWhileTheUsersKeyIsUnset(t *testing.T) {
	mux, d := newTourTestMux(t)

	rec := getWithUser(mux, "/", &cashUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	if !tourStarts(t, rec.Body.String()) {
		t.Error("a user who never finished the tour must get it on /")
	}

	if rec := postFormHtmx(mux, "/api/tour/done", nil, &cashUser); rec.Code != http.StatusOK {
		t.Fatalf("done = %d", rec.Code)
	}
	if tourStarts(t, getWithUser(mux, "/", &cashUser).Body.String()) {
		t.Error("the tour started again after the user finished it")
	}
	// Per user: another operator on the same till still gets it.
	if !tourStarts(t, getWithUser(mux, "/", &mgrUser).Body.String()) {
		t.Error("finishing the tour for one user hid it for another")
	}
	// No session (UT_AUTH=off): no per-user key to keep, so no auto-start.
	if tourStarts(t, getWithUser(mux, "/", nil).Body.String()) {
		t.Error("auto-start without a signed-in user")
	}
	_ = d
}

func TestIndex_TourQueryForcesStart(t *testing.T) {
	mux, _ := newTourTestMux(t)
	if rec := postFormHtmx(mux, "/api/tour/done", nil, &cashUser); rec.Code != http.StatusOK {
		t.Fatalf("done = %d", rec.Code)
	}
	if !tourStarts(t, getWithUser(mux, "/?tour=1", &cashUser).Body.String()) {
		t.Error("/?tour=1 must restart the tour for a user who finished it")
	}
	if !tourStarts(t, getWithUser(mux, "/?tour=1", nil).Body.String()) {
		t.Error("/?tour=1 must start the tour even without a session")
	}
}

func TestIndex_NoTourInSelfOrderMode(t *testing.T) {
	mux, d := newTourTestMux(t)
	if err := d.Settings.Set(context.Background(), "display.mode", "self_order"); err != nil {
		t.Fatal(err)
	}
	rec := getWithUser(mux, "/?tour=1", &cashUser)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/self-order" {
		t.Fatalf("self-order kiosk: want 303 → /self-order, got %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	if strings.Contains(rec.Body.String(), "ut-tour-steps") {
		t.Error("the tour rendered on a self-order kiosk")
	}
}

// The help landing page offers a way back into the tour (AC 3).
func TestHelpIndex_OffersTakeTheTour(t *testing.T) {
	mux, _ := newTourTestMux(t)
	registerHelp(mux, &common.Deps{Cfg: &config.Config{Theme: "default"}, Menu: []common.MenuItem{}})
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/help", nil), cashUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /help = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `data-testid="help-take-tour"`) || !strings.Contains(rec.Body.String(), `tour=1`) {
		t.Error("help landing page has no Take the tour link to /?tour=1")
	}
}

// The steps element must be valid JSON whatever the strings contain (they are
// html/template-escaped JSON strings), with every step titled — in an RTL
// locale too.
func TestIndex_TourStepsAreValidJSON(t *testing.T) {
	mux, _ := newTourTestMux(t)
	for _, lang := range []string{"en", "fa"} {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/?lang="+lang, nil), cashUser)
		req.AddCookie(&http.Cookie{Name: "lang", Value: lang})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		body := rec.Body.String()
		start := strings.Index(body, `id="ut-tour-steps"`)
		if start < 0 {
			t.Fatalf("%s: no tour steps element", lang)
		}
		open := strings.Index(body[start:], ">") + start + 1
		end := strings.Index(body[open:], "</script>") + open
		var cfg struct {
			Labels map[string]string `json:"labels"`
			Steps  []struct {
				ID      string   `json:"id"`
				Targets []string `json:"targets"`
				Title   string   `json:"title"`
				Body    string   `json:"body"`
			} `json:"steps"`
		}
		if err := json.Unmarshal([]byte(body[open:end]), &cfg); err != nil {
			t.Fatalf("%s: tour steps are not valid JSON: %v\n%s", lang, err, body[open:end])
		}
		for _, k := range []string{"next", "back", "done", "skip", "counter"} {
			if cfg.Labels[k] == "" || strings.HasPrefix(cfg.Labels[k], "tour.") {
				t.Errorf("%s: label %q untranslated: %q", lang, k, cfg.Labels[k])
			}
		}
		if lang == "fa" && cfg.Labels["next"] == "Next" {
			t.Errorf("fa: labels rendered in English")
		}
		if len(cfg.Steps) < 3 || cfg.Steps[0].ID != "intro" || cfg.Steps[len(cfg.Steps)-1].ID != "end" {
			t.Fatalf("%s: want intro … end steps, got %+v", lang, cfg.Steps)
		}
		for _, s := range cfg.Steps {
			if s.Title == "" || s.Body == "" || strings.HasPrefix(s.Title, "tour.") {
				t.Errorf("%s: step %q has an untranslated title/body", lang, s.ID)
			}
		}
	}
}
