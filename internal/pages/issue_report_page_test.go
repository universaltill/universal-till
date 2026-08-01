package pages

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

func withTempIssueReportDir(t *testing.T) {
	t.Helper()
	orig := issuereport.PendingDir
	issuereport.PendingDir = t.TempDir()
	t.Cleanup(func() { issuereport.PendingDir = orig })
}

func newIssueReportTestMux(t *testing.T) *http.ServeMux {
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
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerIssueReportPage(mux, dp)
	return mux
}

func TestReportIssuePage_ManagerOnly(t *testing.T) {
	withTempIssueReportDir(t)
	mux := newIssueReportTestMux(t)

	get := func(u *auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/report-issue", nil)
		if u != nil {
			req = auth.WithUser(req, *u)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := get(nil); rec.Code != http.StatusForbidden {
		t.Fatalf("no session = %d, want 403", rec.Code)
	}
	if rec := get(&auth.User{ID: "cashier-1", Role: "cashier"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d, want 403", rec.Code)
	}
	rec := get(&auth.User{ID: "mgr-1", Role: "manager"})
	if rec.Code != http.StatusOK {
		t.Fatalf("manager = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func multipartIssueReport(t *testing.T, note string, includeAudio, includeVideo bool) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	if note != "" {
		_ = w.WriteField("note", note)
	}
	if includeAudio {
		fw, err := w.CreateFormFile("audio", "audio.webm")
		if err != nil {
			t.Fatalf("create audio field: %v", err)
		}
		_, _ = fw.Write([]byte("fake-audio-bytes"))
	}
	if includeVideo {
		fw, err := w.CreateFormFile("video", "video.webm")
		if err != nil {
			t.Fatalf("create video field: %v", err)
		}
		_, _ = fw.Write([]byte("fake-video-bytes"))
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body, w.FormDataContentType()
}

// io.LimitReader alone would silently truncate an oversized recording into
// a corrupted file while the till still reports success — readCappedOrReject
// must reject instead once the source exceeds the limit.
func TestReadCappedOrReject(t *testing.T) {
	within, err := readCappedOrReject(strings.NewReader("12345"), 5)
	if err != nil {
		t.Fatalf("exactly-at-limit read should succeed: %v", err)
	}
	if string(within) != "12345" {
		t.Fatalf("got %q, want %q", within, "12345")
	}

	if _, err := readCappedOrReject(strings.NewReader("123456"), 5); err == nil {
		t.Fatal("expected an error when the source exceeds the limit")
	}
}

func TestIssueReportAPI_ManagerOnly(t *testing.T) {
	withTempIssueReportDir(t)
	mux := newIssueReportTestMux(t)

	body, ctype := multipartIssueReport(t, "printer jammed", true, false)
	req := httptest.NewRequest(http.MethodPost, "/api/issue-reports", body)
	req.Header.Set("Content-Type", ctype)
	req = auth.WithUser(req, auth.User{ID: "cashier-1", Role: "cashier"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier POST = %d, want 403", rec.Code)
	}
}

func TestIssueReportAPI_RequiresNoteOrAudio(t *testing.T) {
	withTempIssueReportDir(t)
	mux := newIssueReportTestMux(t)

	body, ctype := multipartIssueReport(t, "", false, false)
	req := httptest.NewRequest(http.MethodPost, "/api/issue-reports", body)
	req.Header.Set("Content-Type", ctype)
	req = auth.WithUser(req, auth.User{ID: "mgr-1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing note and audio = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestIssueReportAPI_AcceptsNoteWithoutAudio(t *testing.T) {
	withTempIssueReportDir(t)
	mux := newIssueReportTestMux(t)

	body, ctype := multipartIssueReport(t, "printer jammed, typed only", false, false)
	req := httptest.NewRequest(http.MethodPost, "/api/issue-reports", body)
	req.Header.Set("Content-Type", ctype)
	req = auth.WithUser(req, auth.User{ID: "mgr-1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("note-only save = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	bundles, err := issuereport.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 saved bundle, got %d", len(bundles))
	}
	if bundles[0].AudioPath != "" {
		t.Errorf("AudioPath = %q, want empty (note-only report)", bundles[0].AudioPath)
	}
}

// A note wrapped in whitespace (trailing newline from some client, stray
// leading space) must be trimmed before it's stored and before it decides
// whether a description was actually provided — otherwise "   " would pass
// the required-description check as non-empty while saving useless
// whitespace as the report's note.
func TestIssueReportAPI_TrimsNoteBeforeStoring(t *testing.T) {
	withTempIssueReportDir(t)
	mux := newIssueReportTestMux(t)

	body, ctype := multipartIssueReport(t, "  printer jammed, typed only  \n", false, false)
	req := httptest.NewRequest(http.MethodPost, "/api/issue-reports", body)
	req.Header.Set("Content-Type", ctype)
	req = auth.WithUser(req, auth.User{ID: "mgr-1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	bundles, err := issuereport.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 saved bundle, got %d", len(bundles))
	}
	if bundles[0].Meta.Note != "printer jammed, typed only" {
		t.Errorf("Meta.Note = %q, want trimmed", bundles[0].Meta.Note)
	}

	whitespaceOnly, ctype2 := multipartIssueReport(t, "   \n\t  ", false, false)
	req2 := httptest.NewRequest(http.MethodPost, "/api/issue-reports", whitespaceOnly)
	req2.Header.Set("Content-Type", ctype2)
	req2 = auth.WithUser(req2, auth.User{ID: "mgr-1", Role: "manager"})
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("whitespace-only note = %d, want 400: %s", rec2.Code, rec2.Body.String())
	}
}

func TestIssueReportAPI_SavesBundleLocally(t *testing.T) {
	withTempIssueReportDir(t)
	mux := newIssueReportTestMux(t)

	body, ctype := multipartIssueReport(t, "till froze on tender", true, true)
	req := httptest.NewRequest(http.MethodPost, "/api/issue-reports", body)
	req.Header.Set("Content-Type", ctype)
	req = auth.WithUser(req, auth.User{ID: "mgr-1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id"`) {
		t.Fatalf("expected a report id in the response, got: %s", rec.Body.String())
	}

	bundles, err := issuereport.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 saved bundle, got %d", len(bundles))
	}
	if bundles[0].Meta.Note != "till froze on tender" {
		t.Errorf("Note = %q, want %q", bundles[0].Meta.Note, "till froze on tender")
	}
	if bundles[0].VideoPath == "" {
		t.Error("expected VideoPath to be set — a screen recording was included")
	}
}
