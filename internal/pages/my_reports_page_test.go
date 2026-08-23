package pages

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// withTempIssueReportsPendingDir points issuereport.PendingDir at a fresh
// temp dir for the test's duration — same convention as cloudsync's and
// issuereport's own withTempPendingDir — so a still-pending bundle
// (ut-docs#637) can be arranged without touching this repo's real
// ./data/issue-reports/pending.
func withTempIssueReportsPendingDir(t *testing.T) {
	t.Helper()
	orig := issuereport.PendingDir
	issuereport.PendingDir = t.TempDir()
	t.Cleanup(func() { issuereport.PendingDir = orig })
}

// seedIssueReportsSent creates the retained-reports table, column-identical
// to internal/db/migrations/032_issue_reports_sent.sql — same fixture
// convention as seedForPages' other tables (a drifted copy here would test a
// schema production doesn't have).
func seedIssueReportsSent(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE issue_reports_sent (
		id TEXT PRIMARY KEY,
		note TEXT NOT NULL DEFAULT '',
		captured_at TEXT NOT NULL,
		had_audio INTEGER NOT NULL DEFAULT 0,
		had_video INTEGER NOT NULL DEFAULT 0,
		image_count INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'sent',
		github_issue_url TEXT NOT NULL DEFAULT '',
		last_synced_at TEXT
	)`); err != nil {
		t.Fatalf("create issue_reports_sent: %v", err)
	}
}

func newMyReportsTestMux(t *testing.T) (*http.ServeMux, *sql.DB) {
	t.Helper()
	chdirRoot(t)
	// ut-docs#637 review: registerMyReportsPage now also reads
	// issuereport.Pending() (local disk), so every test built on this
	// helper needs its own isolated PendingDir — otherwise a test run from
	// a checkout that has ever captured a real bug report locally would
	// pick up ./data/issue-reports/pending's actual contents.
	withTempIssueReportsPendingDir(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	seedIssueReportsSent(t, db)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Settings: settings.NewStore(db),
		AuthSvc:  auth.NewService(db),
	}
	mux := http.NewServeMux()
	registerMyReportsPage(mux, dp)
	return mux, db
}

func getMyReports(t *testing.T, mux *http.ServeMux) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/my-reports", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestMyReportsPage_EmptyState(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newMyReportsTestMux(t)
	rec := getMyReports(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// The en.json issuereport.my_reports.empty string — translated, not a key.
	if !strings.Contains(rec.Body.String(), "No reports sent from this till yet") {
		t.Fatalf("expected the translated empty-state string, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "issuereport.my_reports.empty") {
		t.Fatalf("empty state rendered as a raw key: %s", rec.Body.String())
	}
}

func TestMyReportsPage_RowsWithTranslatedStatusesAndGithubLink(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, db := newMyReportsTestMux(t)
	seed := func(id, note, capturedAt, status, ghURL string, hadAudio, hadVideo, imageCount int) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO issue_reports_sent (id, note, captured_at, had_audio, had_video, image_count, status, github_issue_url) VALUES (?,?,?,?,?,?,?,?)`,
			id, note, capturedAt, hadAudio, hadVideo, imageCount, status, ghURL); err != nil {
			t.Fatal(err)
		}
	}
	seed("rep-filed", "printer jammed", "2026-08-07T10:00:00Z", "filed", "https://github.com/universaltill/ut-docs/issues/999", 1, 0, 2)
	seed("rep-sent", "screen froze", "2026-08-06T10:00:00Z", "sent", "", 0, 1, 0)

	rec := getMyReports(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Statuses come out as the en.json translations, not raw enum values or
	// dotted keys.
	if !strings.Contains(body, "Filed on GitHub") {
		t.Fatalf("expected the translated 'filed' status, got: %s", body)
	}
	if !strings.Contains(body, "Sent, awaiting review") {
		t.Fatalf("expected the translated 'sent' status, got: %s", body)
	}
	if strings.Contains(body, "issuereport.status.") {
		t.Fatalf("a status rendered as a raw dotted key: %s", body)
	}
	// The GitHub link only for the row that has one.
	if !strings.Contains(body, `href="https://github.com/universaltill/ut-docs/issues/999"`) {
		t.Fatalf("expected the GitHub issue link, got: %s", body)
	}
	// Attachment summary: note text and translated labels.
	if !strings.Contains(body, "printer jammed") {
		t.Fatalf("expected the note text, got: %s", body)
	}
	if !strings.Contains(body, "Voice note") || !strings.Contains(body, "Screen recording") {
		t.Fatalf("expected translated attachment labels, got: %s", body)
	}
	// Newest-captured first.
	if strings.Index(body, "rep-filed") > strings.Index(body, "rep-sent") {
		t.Fatalf("expected newest-first ordering, got: %s", body)
	}
}

// A status value the till doesn't recognize (a newer cloud than this till)
// must render the translated "unknown" string — NOT a raw dotted key like
// "issuereport.status.something-new", which is what the T helper's
// unknown-key fallback would produce if the key were built unguarded.
func TestMyReportsPage_UnknownStatusRendersUnknownTranslation(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, db := newMyReportsTestMux(t)
	if _, err := db.Exec(`INSERT INTO issue_reports_sent (id, note, captured_at, status) VALUES ('rep-x', 'note', '2026-08-07T10:00:00Z', 'some-future-status')`); err != nil {
		t.Fatal(err)
	}
	rec := getMyReports(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "issuereport.status.") || strings.Contains(body, "some-future-status") {
		t.Fatalf("unknown status leaked through untranslated: %s", body)
	}
	if !strings.Contains(body, "Status unknown") {
		t.Fatalf("expected the translated unknown-status string, got: %s", body)
	}
}

// The page is manager-gated, same as the capture panel that links to it —
// reports can carry a manager's free-text notes.
func TestMyReportsPage_ManagerGate(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, _ := newMyReportsTestMux(t)
	rec := getMyReports(t, mux)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Positive counterpart to TestMyReportsPage_ManagerGate: a REAL session
// (ut-docs#709 — canPerform()/Auth.Can(), not the no-session short-circuit)
// for manager/admin/super_admin must reach the page; cashier stays denied.
// super_admin is #554/#555's noted broadening vs. the old isManagerOrAuthOff
// gate — accepted and inert, since nothing today creates that role.
func TestMyReportsPage_RealSessionGatesByRole(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	for role, wantCode := range map[string]int{
		"cashier": http.StatusForbidden, "manager": http.StatusOK,
		"admin": http.StatusOK, "super_admin": http.StatusOK,
	} {
		t.Run(role, func(t *testing.T) {
			mux, _ := newMyReportsTestMux(t)
			req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/my-reports", nil), auth.User{ID: "u1", Role: role})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != wantCode {
				t.Fatalf("role=%s: expected %d, got %d: %s", role, wantCode, rec.Code, rec.Body.String())
			}
		})
	}
}

// ut-docs#637: a bundle that has never uploaded (no issue_reports_sent row
// at all yet) must still show up — as "pending", not invisible. Before this
// fix, the page only ever read issue_reports_sent, so a bundle that never
// reached the cloud simply never appeared anywhere on /my-reports.
func TestMyReportsPage_PendingBundleShowsAsPending(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newMyReportsTestMux(t)
	if _, err := issuereport.Save("still waiting to upload", "", []byte("a"), nil, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rec := getMyReports(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Saved here, waiting to send") {
		t.Fatalf("expected the translated pending status, got: %s", body)
	}
	if !strings.Contains(body, "still waiting to upload") {
		t.Fatalf("expected the note text, got: %s", body)
	}
	if strings.Contains(body, "Couldn&#39;t send") {
		t.Fatalf("a merely-pending bundle must not render as failing: %s", body)
	}
}

// After crossing issuereport.UploadFailingThreshold, a bundle presents as
// "failing" with its (translated, non-actionable) reason — the core
// ut-docs#637 acceptance criterion: "a till that can never upload surfaces
// a problem rather than retrying silently forever."
func TestMyReportsPage_FailingBundleShowsReasonAfterThreshold(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newMyReportsTestMux(t)
	id, err := issuereport.Save("cloud unreachable for a while", "", []byte("a"), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	for i := 0; i < issuereport.UploadFailingThreshold; i++ {
		if _, err := issuereport.RecordUploadFailure(id, issuereport.UploadFailReasonOther); err != nil {
			t.Fatalf("RecordUploadFailure #%d: %v", i, err)
		}
	}

	rec := getMyReports(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Couldn&#39;t send") {
		t.Fatalf("expected the translated failing status, got: %s", body)
	}
	if !strings.Contains(body, "Couldn&#39;t reach the cloud after several tries") {
		t.Fatalf("expected the translated 'other' failure reason, got: %s", body)
	}
}

// A bundle failing below the threshold stays "pending" — a single blip (or
// a few, on a shop that's briefly offline) is normal and must not alarm
// anyone.
func TestMyReportsPage_BelowThresholdStaysPending(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newMyReportsTestMux(t)
	id, err := issuereport.Save("just offline for a bit", "", []byte("a"), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	for i := 0; i < issuereport.UploadFailingThreshold-1; i++ {
		if _, err := issuereport.RecordUploadFailure(id, issuereport.UploadFailReasonOther); err != nil {
			t.Fatalf("RecordUploadFailure #%d: %v", i, err)
		}
	}

	rec := getMyReports(t, mux)
	body := rec.Body.String()
	if strings.Contains(body, "Couldn&#39;t send") {
		t.Fatalf("a bundle below UploadFailingThreshold must not render as failing: %s", body)
	}
	if !strings.Contains(body, "Saved here, waiting to send") {
		t.Fatalf("expected the translated pending status, got: %s", body)
	}
}

// The "not registered" reason is exempt from the threshold: it can't
// self-resolve by waiting, so a single recorded failure is enough to flag
// it — unlike a generic network blip.
func TestMyReportsPage_NotRegisteredFailsImmediately(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newMyReportsTestMux(t)
	id, err := issuereport.Save("till was never enrolled", "", []byte("a"), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := issuereport.RecordUploadFailure(id, issuereport.UploadFailReasonNotRegistered); err != nil {
		t.Fatalf("RecordUploadFailure: %v", err)
	}

	rec := getMyReports(t, mux)
	body := rec.Body.String()
	if !strings.Contains(body, "Couldn&#39;t send") {
		t.Fatalf("expected the translated failing status after a single not-registered failure, got: %s", body)
	}
	if !strings.Contains(body, "finish enrolling it") {
		t.Fatalf("expected the translated not-registered reason, got: %s", body)
	}
}

// Sent (uploaded) and still-pending rows are read from two different
// sources and built in two separate passes — they must still merge into one
// newest-captured-first list, not two blocks.
func TestMyReportsPage_SentAndPendingRowsSortedTogether(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, db := newMyReportsTestMux(t)
	if _, err := db.Exec(`INSERT INTO issue_reports_sent (id, note, captured_at, status) VALUES ('rep-old', 'oldest, already sent', '2026-08-01T10:00:00Z', 'sent')`); err != nil {
		t.Fatal(err)
	}
	if _, err := issuereport.Save("newest, still pending", "", []byte("a"), nil, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rec := getMyReports(t, mux)
	body := rec.Body.String()
	oldIdx := strings.Index(body, "oldest, already sent")
	newIdx := strings.Index(body, "newest, still pending")
	if oldIdx == -1 || newIdx == -1 {
		t.Fatalf("both rows must render: %s", body)
	}
	if newIdx > oldIdx {
		t.Fatalf("expected the newer pending row before the older sent row, got: %s", body)
	}
}

// ut-docs#642: a till with more than rowLimit sent reports plus an old,
// long-failing pending bundle must not have the failing row silently
// truncated away by a naive newest-captured-first cap — that's exactly the
// case where the operator most needs to see it. The pending/failing row is
// prioritized within the cap, at the cost of the single oldest sent row.
func TestMyReportsPage_FailingRowSurvivesCapOverOldestSentRow(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, db := newMyReportsTestMux(t)

	// 100 sent rows, all dated well after "now" so every one of them sorts
	// newer than the pending bundle created below — the scenario the fix
	// targets: a naive time-only truncation would keep all 100 of these and
	// drop the pending row instead, being the oldest overall.
	const sentCount = 100
	for i := 0; i < sentCount; i++ {
		capturedAt := fmt.Sprintf("2027-06-01T%02d:%02d:00Z", i/60, i%60)
		note := fmt.Sprintf("sent row %03d", i)
		if _, err := db.Exec(`INSERT INTO issue_reports_sent (id, note, captured_at, status) VALUES (?, ?, ?, 'sent')`,
			fmt.Sprintf("rep-sent-%03d", i), note, capturedAt); err != nil {
			t.Fatalf("seed sent row %d: %v", i, err)
		}
	}

	id, err := issuereport.Save("old bundle that keeps failing to upload", "", []byte("a"), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	for i := 0; i < issuereport.UploadFailingThreshold; i++ {
		if _, err := issuereport.RecordUploadFailure(id, issuereport.UploadFailReasonOther); err != nil {
			t.Fatalf("RecordUploadFailure #%d: %v", i, err)
		}
	}

	rec := getMyReports(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "old bundle that keeps failing to upload") {
		t.Fatalf("the failing pending row must survive the cap, not be truncated away: %s", body)
	}
	if !strings.Contains(body, "Couldn&#39;t send") {
		t.Fatalf("expected the translated failing status for the surviving row, got: %s", body)
	}
	// The single oldest sent row (i=0) is the one that must give way — every
	// other sent row (i=1..99) still fits comfortably within the cap.
	if strings.Contains(body, "sent row 000") {
		t.Fatalf("expected the oldest sent row to be dropped to make room for the failing row, got: %s", body)
	}
	if !strings.Contains(body, "sent row 099") {
		t.Fatalf("expected the newest sent row to still render: %s", body)
	}
}

// ut-docs#445: a store with more sent reports than rowLimit gets a notice
// naming exactly how many more exist beyond what's shown — a purely local
// SQLite count (CountSent), no network involved.
func TestMyReportsPage_MoreNotShownNoticeWhenOverLimit(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, db := newMyReportsTestMux(t)

	const rowLimit = 100
	const extra = 7
	for i := 0; i < rowLimit+extra; i++ {
		capturedAt := fmt.Sprintf("2027-06-01T%02d:%02d:00Z", i/60, i%60)
		if _, err := db.Exec(`INSERT INTO issue_reports_sent (id, note, captured_at, status) VALUES (?, ?, ?, 'sent')`,
			fmt.Sprintf("rep-sent-%03d", i), "n", capturedAt); err != nil {
			t.Fatalf("seed sent row %d: %v", i, err)
		}
	}

	rec := getMyReports(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// A tight combo (count immediately followed by "more report") so this
	// can't accidentally match a seeded row id like "rep-sent-007" — every
	// id string in this test happens to contain the digit '7' too.
	if !strings.Contains(body, fmt.Sprintf("%d more report", extra)) {
		t.Fatalf("expected the notice to name the %d not-shown reports, got: %s", extra, body)
	}
	if strings.Contains(body, "issuereport.my_reports.more_not_shown") {
		t.Fatalf("notice rendered as a raw key: %s", body)
	}
}

// At or below rowLimit, every sent row is already shown — no notice.
func TestMyReportsPage_NoMoreNotShownNoticeAtOrBelowLimit(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, db := newMyReportsTestMux(t)

	const rowLimit = 100
	for i := 0; i < rowLimit; i++ {
		capturedAt := fmt.Sprintf("2027-06-01T%02d:%02d:00Z", i/60, i%60)
		if _, err := db.Exec(`INSERT INTO issue_reports_sent (id, note, captured_at, status) VALUES (?, ?, ?, 'sent')`,
			fmt.Sprintf("rep-sent-%03d", i), "n", capturedAt); err != nil {
			t.Fatalf("seed sent row %d: %v", i, err)
		}
	}

	rec := getMyReports(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "not shown") {
		t.Fatalf("expected no more-not-shown notice at exactly rowLimit rows, got: %s", body)
	}
}
