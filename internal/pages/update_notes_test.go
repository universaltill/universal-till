package pages

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/releasenotes"
	"github.com/universaltill/universal-till/internal/updates"
)

// ut-docs#3940: GET /api/update/notes renders the incoming version's release
// notes (or "unavailable") into Settings → Software update before Install.

type notesStub struct {
	currentCalls, fetchCalls int
}

func stubUpdateNotes(t *testing.T, st updates.Status, notes []*releasenotes.Note, err error, downloadLink bool) *notesStub {
	t.Helper()
	s := &notesStub{}
	oc, of, od := updateNotesCurrent, updateIncomingNotes, updateNotesDownloadLink
	t.Cleanup(func() { updateNotesCurrent, updateIncomingNotes, updateNotesDownloadLink = oc, of, od })
	updateNotesCurrent = func() updates.Status { s.currentCalls++; return st }
	updateIncomingNotes = func(context.Context, updates.Status, string, string) ([]*releasenotes.Note, error) {
		s.fetchCalls++
		return notes, err
	}
	updateNotesDownloadLink = func() bool { return downloadLink }
	return s
}

func getUpdateNotes(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	registerUpdateAPI(mux, newEODTestDeps(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

var offered = updates.Status{Available: true, Latest: "1.2.0", URL: "https://github.com/universaltill/universal-till/releases/tag/v1.2.0", NotesURL: "https://github.com/universaltill/universal-till/releases/download/v1.2.0/release-notes.json"}

func TestUpdateNotes_CashierRefusedBeforeAnyNetwork(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	s := stubUpdateNotes(t, offered, nil, nil, true)
	for name, req := range map[string]*http.Request{
		"cashier":    auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/update/notes", nil), auth.User{ID: "c1", Role: "cashier"}),
		"no session": httptest.NewRequest(http.MethodGet, "/api/update/notes", nil),
	} {
		rec := getUpdateNotes(t, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: code %d, want 403", name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "release-note") {
			t.Errorf("%s: notes leaked: %q", name, rec.Body.String())
		}
	}
	if s.fetchCalls != 0 || s.currentCalls != 0 {
		t.Fatalf("refused request reached the update state (%d) / notes fetch (%d), want 0/0", s.currentCalls, s.fetchCalls)
	}
}

func TestUpdateNotes_ManagerRolesAllowed(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	for _, role := range []string{"manager", "admin"} {
		stubUpdateNotes(t, updates.Status{}, nil, nil, false)
		rec := getUpdateNotes(t, auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/update/notes", nil), auth.User{ID: "m1", Role: role}))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: code %d, want 200", role, rec.Code)
		}
	}
}

func TestUpdateNotes_EmptyWhenNoUpdate(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	s := stubUpdateNotes(t, updates.Status{Latest: "1.0.0"}, nil, nil, true)
	rec := getUpdateNotes(t, httptest.NewRequest(http.MethodGet, "/api/update/notes", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("no update: code %d body %q, want empty 200", rec.Code, rec.Body.String())
	}
	if s.fetchCalls != 0 {
		t.Fatal("no update: must not fetch notes")
	}
}

func TestUpdateNotes_RendersNotesNewestFirst(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	notes := []*releasenotes.Note{
		{Version: "v1.2.0", Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Locale: "en", Translated: true, HTML: template.HTML("<h5>New</h5><ul><li>Twelve.</li></ul>")},
		{Version: "v1.1.0", Date: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Locale: "en", Translated: false, HTML: template.HTML("<ul><li>Eleven.</li></ul>")},
	}
	stubUpdateNotes(t, offered, notes, nil, true)
	req := httptest.NewRequest(http.MethodGet, "/api/update/notes", nil)
	rec := getUpdateNotes(t, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	i, j := strings.Index(body, `data-version="v1.2.0"`), strings.Index(body, `data-version="v1.1.0"`)
	if i < 0 || j < 0 || i > j {
		t.Fatalf("want v1.2.0 then v1.1.0 in incoming-release-note sections: %s", body)
	}
	if strings.Count(body, `data-testid="incoming-release-note"`) != 2 {
		t.Fatalf("want two incoming-release-note sections: %s", body)
	}
	if !strings.Contains(body, "<li>Twelve.</li>") {
		t.Fatalf("note HTML must render unescaped: %s", body)
	}
	if strings.Count(body, "release-note-fallback") != 1 {
		t.Fatalf("exactly the untranslated note carries the English-only line: %s", body)
	}
	if strings.Contains(body, "incoming-notes-unavailable") {
		t.Fatalf("unavailable state must not show with notes: %s", body)
	}
}

func TestUpdateNotes_UnavailableFallback(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	for _, link := range []bool{true, false} {
		stubUpdateNotes(t, offered, nil, errors.New("HTTP 404"), link)
		rec := getUpdateNotes(t, httptest.NewRequest(http.MethodGet, "/api/update/notes", nil))
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("unavailable is not an error response: code %d", rec.Code)
		}
		if !strings.Contains(body, `data-testid="incoming-notes-unavailable"`) ||
			!strings.Contains(body, template.HTMLEscapeString(httpx.T("en", "settings.update.notes_unavailable"))) {
			t.Fatalf("want the unavailable text: %s", body)
		}
		hasLink := strings.Contains(body, `href="`+offered.URL+`"`)
		if hasLink != link {
			t.Fatalf("release-page link shown = %v, want %v (only where a link is actionable): %s", hasLink, link, body)
		}
		if link && (!strings.Contains(body, `target="_blank"`) || !strings.Contains(body, `rel="noopener"`)) {
			t.Fatalf("release-page link must open outside the till: %s", body)
		}
	}
}

func TestUpdateNotes_ReleasePageLinkMustBeHTTPS(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	st := offered
	st.URL = "javascript:alert(1)"
	stubUpdateNotes(t, st, nil, errors.New("x"), true)
	body := getUpdateNotes(t, httptest.NewRequest(http.MethodGet, "/api/update/notes", nil)).Body.String()
	if strings.Contains(body, "javascript:") || strings.Contains(body, "<a ") {
		t.Fatalf("a non-https release URL must not become a link: %s", body)
	}
}

// The Software update card loads the notes ABOVE the install controls, for
// a manager only (ut-docs#3940).
func TestSettingsUpdateCard_IncomingNotesAboveInstallControls(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	body := renderSettingsAs(t, mux, mgrUser, true)
	notes := strings.Index(body, `id="incoming-release-notes"`)
	if notes < 0 {
		t.Fatal("manager with an update waiting: no #incoming-release-notes container")
	}
	if !strings.Contains(body, `hx-get="/api/update/notes"`) {
		t.Fatal("container must load GET /api/update/notes")
	}
	for _, ctl := range []string{`id="android-update-form"`, `hx-post="/api/update/check"`} {
		i := strings.Index(body, ctl)
		if i < 0 || i < notes {
			t.Errorf("%s must come after the incoming notes (notes at %d, control at %d)", ctl, notes, i)
		}
	}

	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), `id="incoming-release-notes"`) {
		t.Fatalf("cashier (code %d) must not get the incoming notes container", rec.Code)
	}
}

// The template gate itself: a viewer who may open Settings but lacks
// plugin_management (a custom role) gets no container either.
func TestSettingsUpdateCard_IncomingNotesNeedPluginManagement(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	httpx.InitRailVisibility(func(_ *http.Request, predicate string) bool { return predicate != "plugin_management" })
	t.Cleanup(func() { httpx.InitRailVisibility(nil) })
	body := renderSettingsAs(t, mux, mgrUser, false)
	if !strings.Contains(body, `id="settings-update"`) {
		t.Fatal("settings page did not render the Software update card")
	}
	if strings.Contains(body, `id="incoming-release-notes"`) {
		t.Fatal("a viewer without plugin_management must not get the incoming notes container")
	}
}
