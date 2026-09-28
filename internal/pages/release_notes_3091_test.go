package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/releasenotes"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#3091: the newest embedded note's version — the tests pin behaviour
// against whatever release is newest, not a hardcoded tag that ages.
func newestNoteVersion(t *testing.T) string {
	t.Helper()
	// A build with no note of its own gets the newest notes first.
	ns := releasenotes.Builtin().Recent("en", "dev", 1)
	if len(ns) == 0 {
		t.Fatal("no embedded release notes")
	}
	return strings.TrimPrefix(ns[0].Version, "v")
}

func withRunningVersion(t *testing.T, v string) {
	t.Helper()
	old := buildinfo.Version
	buildinfo.Version = v
	t.Cleanup(func() { buildinfo.Version = old; releasenotes.SetNoticeVersion("") })
}

func getSetting3091(t *testing.T, s *settings.Store, k string) string {
	t.Helper()
	v, _, err := s.Get(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRecordRunningVersion_FreshInstallHasNoNotice(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	v := newestNoteVersion(t)
	withRunningVersion(t, v)
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

	recordRunningVersion(context.Background(), d.Settings, releasenotes.Builtin(), v, now)

	if got := getSetting3091(t, d.Settings, data.AppVersionSettingsKey); got != v {
		t.Errorf("app.version = %q, want %q", got, v)
	}
	if got := getSetting3091(t, d.Settings, data.AppVersionFirstRunAtSettingsKey); got != now.Format(time.RFC3339) {
		t.Errorf("first_run_at = %q", got)
	}
	if got := getSetting3091(t, d.Settings, data.ReleaseNotesSeenVersionSettingsKey); got != "v"+v {
		t.Errorf("fresh install must seed seen_version to the running version, got %q", got)
	}
	if releasenotes.NoticeVersion() != "" {
		t.Error("a fresh install must not announce an update on day one")
	}
}

func TestRecordRunningVersion_UpgradeAnnouncesOnce(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	ctx := context.Background()
	v := newestNoteVersion(t)
	withRunningVersion(t, v)
	_ = d.Settings.Set(ctx, "setup.completed", "true")
	_ = d.Settings.Set(ctx, data.AppVersionSettingsKey, "0.0.1")
	_ = d.Settings.Set(ctx, data.AppVersionFirstRunAtSettingsKey, "2026-01-01T00:00:00Z")
	first := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

	recordRunningVersion(ctx, d.Settings, releasenotes.Builtin(), v, first)
	if releasenotes.NoticeVersion() != "v"+v {
		t.Fatalf("after an update the notice must announce v%s, got %q", v, releasenotes.NoticeVersion())
	}
	if got := getSetting3091(t, d.Settings, data.AppVersionFirstRunAtSettingsKey); got != first.Format(time.RFC3339) {
		t.Errorf("first_run_at = %q, want the update's first start", got)
	}

	// A restart on the same version keeps the first-run time and, until
	// seen, the notice.
	releasenotes.SetNoticeVersion("")
	recordRunningVersion(ctx, d.Settings, releasenotes.Builtin(), v, first.Add(48*time.Hour))
	if got := getSetting3091(t, d.Settings, data.AppVersionFirstRunAtSettingsKey); got != first.Format(time.RFC3339) {
		t.Errorf("a restart moved first_run_at to %q", got)
	}
	if releasenotes.NoticeVersion() != "v"+v {
		t.Error("an unseen notice must survive a restart")
	}

	// Once seen, a restart doesn't bring it back.
	if err := markReleaseNotesSeen(ctx, d.Settings, v); err != nil {
		t.Fatal(err)
	}
	if releasenotes.NoticeVersion() != "" {
		t.Error("markReleaseNotesSeen must clear the notice")
	}
	recordRunningVersion(ctx, d.Settings, releasenotes.Builtin(), v, first.Add(72*time.Hour))
	if releasenotes.NoticeVersion() != "" {
		t.Error("a seen notice came back after a restart")
	}
}

func TestRecordRunningVersion_DevBuildNeverAnnounces(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	ctx := context.Background()
	withRunningVersion(t, "dev")
	_ = d.Settings.Set(ctx, "setup.completed", "true")
	_ = d.Settings.Set(ctx, data.AppVersionSettingsKey, "0.0.1")

	recordRunningVersion(ctx, d.Settings, releasenotes.Builtin(), "dev", time.Now())
	if releasenotes.NoticeVersion() != "" {
		t.Error("a build with no notes of its own must not show the chip")
	}
}

func TestReleaseNotesSeen_ManagerOnly(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	t.Setenv("UT_AUTH", "on")
	v := newestNoteVersion(t)
	withRunningVersion(t, v)
	releasenotes.SetNoticeVersion("v" + v)

	if rec := postFormHtmx(mux, "/api/release-notes/seen", nil, &cashUser); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier POST = %d, want 403", rec.Code)
	}
	if releasenotes.NoticeVersion() == "" {
		t.Fatal("a refused dismiss cleared the notice")
	}
	rec := postFormHtmx(mux, "/api/release-notes/seen", nil, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager POST = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "" {
		t.Errorf("the chip is swapped (outerHTML) with the empty response, got %q", rec.Body.String())
	}
	if got := getSetting3091(t, d.Settings, data.ReleaseNotesSeenVersionSettingsKey); got != "v"+v {
		t.Errorf("seen_version = %q", got)
	}
	if releasenotes.NoticeVersion() != "" {
		t.Error("dismiss did not clear the notice")
	}
}

func getPathAs3091(t *testing.T, mux *http.ServeMux, path string, u auth.User) *httptest.ResponseRecorder {
	t.Helper()
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, path, nil), u)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSettingsAbout_ShowsVersionAndNotes(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	t.Setenv("UT_AUTH", "on")
	v := newestNoteVersion(t)
	withRunningVersion(t, v)
	_ = d.Settings.Set(context.Background(), data.AppVersionFirstRunAtSettingsKey, "2026-09-28T10:00:00Z")

	rec := getPathAs3091(t, mux, "/settings", mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	i := strings.Index(body, `id="settings-about"`)
	if i < 0 {
		t.Fatal("no #settings-about card")
	}
	card := body[i:]
	if j := strings.Index(card, `id="settings-theme"`); j > 0 {
		card = card[:j]
	}
	note, _ := releasenotes.Builtin().Get("en", v)
	for _, want := range []string{
		"v" + v,
		`data-testid="release-note"`,
		`data-version="v` + v + `"`,
		"<h5",        // "## New/Improved/Fixed" under the version's h4
		"28/09/2026", // installed-on, en date order
	} {
		if !strings.Contains(card, want) {
			t.Errorf("About card lacks %q", want)
		}
	}
	// First line of the note's rendered list must be in the card verbatim.
	firstLi := string(note.HTML)
	if k := strings.Index(firstLi, "<li>"); k >= 0 {
		firstLi = firstLi[k : k+40]
		if !strings.Contains(card, firstLi) {
			t.Errorf("About card does not carry the embedded note (%q)", firstLi)
		}
	}
}

func TestSettingsAbout_WhatsNewLinkMarksSeen(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	t.Setenv("UT_AUTH", "on")
	v := newestNoteVersion(t)
	withRunningVersion(t, v)
	releasenotes.SetNoticeVersion("v" + v)

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/release-notes/seen?open=1", nil), mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/settings#settings-about" {
		t.Fatalf("open from chip: code %d, HX-Redirect %q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
	body := getPathAs3091(t, mux, "/settings#settings-about", mgrUser).Body.String()
	if releasenotes.NoticeVersion() != "" {
		t.Fatal("opening the notes from the chip must mark them seen")
	}
	if got := getSetting3091(t, d.Settings, data.ReleaseNotesSeenVersionSettingsKey); got != "v"+v {
		t.Errorf("seen_version = %q", got)
	}
	if strings.Contains(body, `data-testid="sb-release-notes"`) {
		t.Error("the page opened from the chip still shows the chip")
	}
}

func TestReleaseNotesChip_ManagerOnlyAndNeverOnKiosk(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	t.Setenv("UT_AUTH", "on")
	httpx.InitRailVisibility(menuPredicateChecker(d))
	t.Cleanup(func() { httpx.InitRailVisibility(nil); httpx.InitSelfOrderMode(false) })
	v := newestNoteVersion(t)
	withRunningVersion(t, v)
	releasenotes.SetNoticeVersion("v" + v)

	mgr := getPathAs3091(t, mux, "/settings", mgrUser).Body.String()
	if !strings.Contains(mgr, `data-testid="sb-release-notes"`) || !strings.Contains(mgr, `hx-post="/api/release-notes/seen"`) {
		t.Fatal("a manager does not see the after-update chip")
	}
	if !strings.Contains(mgr, `href="/settings#settings-about" hx-post="/api/release-notes/seen?open=1"`) {
		t.Error("the chip must link to Settings → About")
	}
	// Cashier (#3079): /settings is a 403, but the page still has the
	// status bar — without the chip.
	cash := getPathAs3091(t, mux, "/settings", cashUser).Body.String()
	if !strings.Contains(cash, `class="statusbar"`) {
		t.Fatal("cashier's 403 page has no status bar to check")
	}
	if strings.Contains(cash, `data-testid="sb-release-notes"`) {
		t.Error("a cashier must never see the release-notes chip")
	}
	// Self-order kiosk: never, whoever is signed in.
	httpx.InitSelfOrderMode(true)
	if strings.Contains(getPathAs3091(t, mux, "/settings", mgrUser).Body.String(), `data-testid="sb-release-notes"`) {
		t.Error("the chip must never show in self-order kiosk mode")
	}
}
