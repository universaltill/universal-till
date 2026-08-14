package pages

import (
	"bytes"
	"fmt"
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
	// AuthSvc (ut-docs#713): /report-issue and its API are now
	// canPerform()-gated, which queries role_permissions for real via
	// AuthSvc.Can() — seedForPages already seeds it (manager/admin/
	// super_admin granted "issue_reporting").
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
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
	// super_admin broadens vs. isManagerOrAuthOff (manager/admin only,
	// per #555) — accepted, and pinned here so a regression to the old gate
	// wouldn't silently pass this test on manager alone.
	if rec := get(&auth.User{ID: "super-1", Role: "super_admin"}); rec.Code != http.StatusOK {
		t.Fatalf("super_admin = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// The 🐞 nav chip mirrors the session-chip convention (TestSessionChip):
// an empty 200 when the operator isn't allowed to report issues — so
// nothing appears in the nav — and real button markup when they are.
func TestBugReportChip(t *testing.T) {
	mux := newIssueReportTestMux(t)

	get := func(u *auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/ui/bugreport-chip", nil)
		if u != nil {
			req = auth.WithUser(req, *u)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := get(nil); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("no-session chip: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := get(&auth.User{ID: "c1", Role: "cashier"}); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("cashier chip: code=%d body=%q", rec.Code, rec.Body.String())
	}
	rec := get(&auth.User{ID: "m1", Role: "manager"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-testid="bugreport-toggle"`) {
		t.Fatalf("manager chip: code=%d body=%s", rec.Code, rec.Body.String())
	}
	// super_admin broadens vs. isManagerOrAuthOff (manager/admin only, per
	// #555) — pinned here so a regression of this handler's gate back to the
	// old one fails: without this case the chip's gate is the ONE
	// issue_reporting site no test distinguishes (review, ut-docs#713).
	if rec := get(&auth.User{ID: "s1", Role: "super_admin"}); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `data-testid="bugreport-toggle"`) {
		t.Fatalf("super_admin chip: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// /report-issue keeps working as a route (it's the /menu tile's target) but
// no longer carries its own copy of the capture UI: it renders the shared
// panel already open (server-side class, no dependency on client JS).
func TestReportIssuePage_RendersPanelOpen(t *testing.T) {
	withTempIssueReportDir(t)
	mux := newIssueReportTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/report-issue", nil)
	req = auth.WithUser(req, auth.User{ID: "mgr-1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /report-issue = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="bugreport-panel"`) {
		t.Fatal("expected the shared bug-report panel in the page")
	}
	if !strings.Contains(body, `class="bugreport-panel open"`) {
		t.Fatal("expected the panel to render already open on /report-issue")
	}
	// The capture UI lives in the panel now — the page must not duplicate it.
	if n := strings.Count(body, `id="ir-note"`); n != 1 {
		t.Fatalf("capture textarea appears %d times, want exactly 1 (panel only)", n)
	}
}

// Ordinary pages render the panel too, but closed: the "open" class is only
// stamped server-side when the handler passes openBugReportPanel (i.e. on
// /report-issue). Rendered through the real layout with the flag unset.
func TestBugReportPanel_ClosedByDefault(t *testing.T) {
	mux := newIssueReportTestMux(t) // initializes i18n + chdir to repo root
	_ = mux

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	httpx.Render("ui/pages/report_issue.html", map[string]any{
		"title": "x", "theme": "default",
	})(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("render = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="bugreport-panel"`) {
		t.Fatal("expected the panel markup on every staff page")
	}
	if strings.Contains(body, `class="bugreport-panel open"`) {
		t.Fatal("panel must render closed when openBugReportPanel isn't set")
	}
}

func multipartIssueReport(t *testing.T, note string, includeAudio, includeVideo bool) (*bytes.Buffer, string) {
	t.Helper()
	return multipartIssueReportWithImages(t, note, includeAudio, includeVideo, nil)
}

// multipartIssueReportWithImages is multipartIssueReport plus repeated
// "image" file parts — one per entry in images, matching how the browser
// sends multiple captured screenshots under the same field name.
func multipartIssueReportWithImages(t *testing.T, note string, includeAudio, includeVideo bool, images [][]byte) (*bytes.Buffer, string) {
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
	for i, img := range images {
		fw, err := w.CreateFormFile("image", fmt.Sprintf("image-%d.png", i))
		if err != nil {
			t.Fatalf("create image field: %v", err)
		}
		_, _ = fw.Write(img)
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

	// super_admin broadens vs. isManagerOrAuthOff (manager/admin only, per
	// #555) — pinned so a regression of THIS handler's gate back to the old
	// one fails here rather than passing on the cashier case alone (review,
	// ut-docs#713).
	body, ctype = multipartIssueReport(t, "printer jammed", true, false)
	req = httptest.NewRequest(http.MethodPost, "/api/issue-reports", body)
	req.Header.Set("Content-Type", ctype)
	req = auth.WithUser(req, auth.User{ID: "super-1", Role: "super_admin"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("super_admin POST = %d, want 200: %s", rec.Code, rec.Body.String())
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

// The operator's UI locale at capture time (ut-docs#397) must land on the
// saved bundle, resolved exactly the way every page render resolves it
// (httpx.ResolveLocale: ?lang= query, then the ut_lang cookie, then the
// configured default) — here via the ut_lang cookie, the steady-state an
// operator who picked a language is actually in.
func TestIssueReportAPI_CapturesOperatorLocale(t *testing.T) {
	withTempIssueReportDir(t)
	mux := newIssueReportTestMux(t)

	body, ctype := multipartIssueReport(t, "", true, false)
	req := httptest.NewRequest(http.MethodPost, "/api/issue-reports", body)
	req.Header.Set("Content-Type", ctype)
	req.AddCookie(&http.Cookie{Name: "ut_lang", Value: "fa"})
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
	if bundles[0].Meta.Locale != "fa" {
		t.Errorf("Meta.Locale = %q, want %q (from the ut_lang cookie)", bundles[0].Meta.Locale, "fa")
	}
}

// A locale outside the shipped set (a hand-edited cookie, a stale value from
// a locale this build no longer ships) must not reach the saved bundle
// as-is — it goes on to Whisper's language param downstream (ut-docs#397),
// and an unrecognized code there risks a permanently-failing transcription
// rather than a harmless template-lookup miss. Falls back to "" (the same
// "unknown, auto-detect" value an unset locale already carries), not to the
// raw unchecked string.
func TestIssueReportAPI_UnknownLocaleFallsBackToEmpty(t *testing.T) {
	withTempIssueReportDir(t)
	mux := newIssueReportTestMux(t)

	body, ctype := multipartIssueReport(t, "", true, false)
	req := httptest.NewRequest(http.MethodPost, "/api/issue-reports", body)
	req.Header.Set("Content-Type", ctype)
	req.AddCookie(&http.Cookie{Name: "ut_lang", Value: "klingon"})
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
	if bundles[0].Meta.Locale != "" {
		t.Errorf("Meta.Locale = %q, want %q (unrecognized locale must fall back to auto-detect, not pass through)", bundles[0].Meta.Locale, "")
	}
}

// With no lang query and no ut_lang cookie the handler still saves — the
// bundle just carries the resolved default ("en", set by newIssueReportTestMux's
// httpx.InitI18n), never an error.
func TestIssueReportAPI_LocaleDefaultsWhenUnset(t *testing.T) {
	withTempIssueReportDir(t)
	mux := newIssueReportTestMux(t)

	body, ctype := multipartIssueReport(t, "typed note, default locale", false, false)
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
	if bundles[0].Meta.Locale != "en" {
		t.Errorf("Meta.Locale = %q, want %q (the configured default)", bundles[0].Meta.Locale, "en")
	}
}

// Multiple captured screenshots (ut-docs#347) must all be saved, in order,
// on the resulting bundle.
func TestIssueReportAPI_SavesMultipleImages(t *testing.T) {
	withTempIssueReportDir(t)
	mux := newIssueReportTestMux(t)

	images := [][]byte{[]byte("fake-png-0"), []byte("fake-png-1"), []byte("fake-png-2")}
	body, ctype := multipartIssueReportWithImages(t, "till froze on tender", false, false, images)
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
	if len(bundles[0].ImagePaths) != 3 {
		t.Fatalf("expected 3 ImagePaths, got %d: %v", len(bundles[0].ImagePaths), bundles[0].ImagePaths)
	}
}

// A report with no screenshots at all (note-only, exactly today's behaviour)
// must be entirely unaffected by the new image handling.
func TestIssueReportAPI_ZeroImagesUnaffected(t *testing.T) {
	withTempIssueReportDir(t)
	mux := newIssueReportTestMux(t)

	body, ctype := multipartIssueReportWithImages(t, "note only, no images", false, false, nil)
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
	if len(bundles[0].ImagePaths) != 0 {
		t.Errorf("ImagePaths = %v, want empty", bundles[0].ImagePaths)
	}
}

// One oversized screenshot among otherwise-valid ones must reject the whole
// request with 400 and must not leave a partial bundle behind.
//
// Be precise about what this actually exercises: the 400 comes from
// http.MaxBytesReader tripping inside ParseMultipartForm, NOT from the
// per-image readCappedOrReject check — the body cap and the per-part cap are
// both issueReportMaxBytes, so no single part can ever exceed the cap without
// the whole body having exceeded it first. The per-image check is therefore
// unreachable defensive code (exactly as the pre-existing audio/video checks
// are), and this test would pass even with the image handling removed. It is
// a guard on the outcome the operator sees — reject, save nothing — not on
// the image code path.
func TestIssueReportAPI_OversizedImageRejectsAndSavesNothing(t *testing.T) {
	withTempIssueReportDir(t)
	mux := newIssueReportTestMux(t)

	oversized := bytes.Repeat([]byte("x"), issueReportMaxBytes+1)
	images := [][]byte{[]byte("fake-png-0"), oversized}
	body, ctype := multipartIssueReportWithImages(t, "till froze on tender", false, false, images)
	req := httptest.NewRequest(http.MethodPost, "/api/issue-reports", body)
	req.Header.Set("Content-Type", ctype)
	req = auth.WithUser(req, auth.User{ID: "mgr-1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized image = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	bundles, err := issuereport.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 0 {
		t.Fatalf("expected 0 saved bundles after a rejected oversized image, got %d", len(bundles))
	}
}
