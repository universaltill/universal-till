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
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// newBackupTestDeps opens a REAL on-disk (not in-memory) sqlite DB via the
// production internal/db package, since db.Snapshot/ListBackups/StageRestore
// all operate on the DB's file path on disk, not just the *sql.DB handle --
// unlike most other page tests in this package, seedForPages's in-memory
// fixture can't stand in here.
func newBackupTestDeps(t *testing.T) (*http.ServeMux, *common.Deps, string) {
	t.Helper()
	// t.TempDir() below is always absolute, so chdirRoot (needed to resolve
	// web/locales) doesn't interfere with the backup file paths.
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	dp := &common.Deps{
		Cfg: &config.Config{DBPath: dbPath},
		Db:  d.DB,
	}
	mux := http.NewServeMux()
	registerBackupAPI(mux, dp)
	return mux, dp, dbPath
}

func TestCopyBackupTo_CopiesBytesAndCreatesDestDir(t *testing.T) {
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "source.db")
	if err := os.WriteFile(src, []byte("fake sqlite bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	// dstDir deliberately doesn't exist yet -- copyBackupTo must create it.
	dstDir := filepath.Join(t.TempDir(), "nested", "downloads")

	dst, err := copyBackupTo(dstDir, src, "copy.db")
	if err != nil {
		t.Fatalf("copyBackupTo: %v", err)
	}
	if dst != filepath.Join(dstDir, "copy.db") {
		t.Fatalf("expected the returned destination path to match, got %q", dst)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(got) != "fake sqlite bytes" {
		t.Fatalf("expected the exact source bytes copied, got %q", got)
	}
}

// ut-docs#557: POST /api/backup/now moved off the flat deny() 403 onto
// checkOrElevate — a denied caller now gets the in-place elevation prompt
// (200, htmx-swappable) instead. download/save-copy/restore below are
// deliberately NOT wired to elevation yet (see the Dev report) and keep
// the old flat-403 deny() gate unchanged.
func TestBackupAPI_AllEndpointsRequireManager(t *testing.T) {
	mux, _, _ := newBackupTestDeps(t)
	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/backup/download/whatever.db"},
		{http.MethodPost, "/api/backup/save-copy/whatever.db"},
		{http.MethodPost, "/api/backup/restore"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: expected 403, got %d: %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/backup/now", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("POST /api/backup/now: expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "elevation-dialog") || !strings.Contains(rec.Body.String(), `name="override_pin"`) {
		t.Errorf("POST /api/backup/now: expected the elevation prompt dialog, got: %s", rec.Body.String())
	}
}

// ut-docs#557: a cashier session denied data_management gets past the gate
// with a valid manager approver PIN — the snapshot is created attributed to
// the approver, and the audit trail records both (dual attribution).
func TestBackupNow_ElevatesOnValidApproverPIN(t *testing.T) {
	mux, dp, dbPath := newBackupTestDeps(t)
	dp.AuthSvc = auth.NewService(dp.Db) // canPerform() needs it non-nil once a real session reaches it
	authRepo := data.NewAuthRepo(dp.Db)
	ctx := t.Context()

	mgrID, err := authRepo.CreateUser(ctx, "mgr-backup", "Backup Manager", "manager")
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	hash, err := auth.HashPIN("555222")
	if err != nil {
		t.Fatalf("hash pin: %v", err)
	}
	if err := authRepo.SetUserPIN(ctx, mgrID, hash); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	// blocked_actor_id carries a real FK to users(id) — the blocked session
	// user must exist as a real row, same as the approver.
	blockedID, err := authRepo.CreateUser(ctx, "blocked-cashier", "Blocked Cashier", "cashier")
	if err != nil {
		t.Fatalf("create cashier: %v", err)
	}

	form := strings.NewReader("override_pin=555222")
	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/backup/now", form), auth.User{ID: blockedID, Role: "cashier"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	list, err := db.ListBackups(dbPath)
	if err != nil {
		t.Fatalf("list backups: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly one real snapshot file on disk, got %d", len(list))
	}

	var actorID, blockedActorID string
	if err := dp.Db.QueryRow(`SELECT actor_id, blocked_actor_id FROM audit_log WHERE action='backup_created'`).
		Scan(&actorID, &blockedActorID); err != nil {
		t.Fatalf("expected a backup_created audit row: %v", err)
	}
	if actorID != mgrID {
		t.Fatalf("actor_id = %q, want the approver %q", actorID, mgrID)
	}
	if blockedActorID != blockedID {
		t.Fatalf("blocked_actor_id = %q, want the originally-blocked session user %q", blockedActorID, blockedID)
	}
}

func TestBackupNow_CreatesRealSnapshotAndAudits(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, dbPath := newBackupTestDeps(t)

	req := httptest.NewRequest(http.MethodPost, "/api/backup/now", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("expected HX-Refresh: true so the settings page reloads the new snapshot, got %q", rec.Header().Get("HX-Refresh"))
	}
	if !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("expected a success indicator, got: %s", rec.Body.String())
	}

	list, err := db.ListBackups(dbPath)
	if err != nil {
		t.Fatalf("list backups: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly one real snapshot file on disk, got %d", len(list))
	}

	var action string
	if err := dp.Db.QueryRow(`SELECT action FROM audit_log WHERE action='backup_created'`).Scan(&action); err != nil {
		t.Fatalf("expected a backup_created audit row: %v", err)
	}
}

func TestListBackupsForUI_FormatsRealSnapshots(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newBackupTestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/backup/now", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	rows := listBackupsForUI(dp)
	if len(rows) != 1 {
		t.Fatalf("expected one formatted backup row, got %d", len(rows))
	}
	if !strings.HasSuffix(rows[0].Name, ".db") {
		t.Fatalf("expected a .db snapshot name, got %q", rows[0].Name)
	}
	if !strings.HasSuffix(rows[0].Size, " MB") {
		t.Fatalf("expected the size formatted in MB, got %q", rows[0].Size)
	}
	if rows[0].Date == "" {
		t.Fatalf("expected a formatted date")
	}
}

func TestDownloadBackup_ValidatesNameAndServesRealFile(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, dbPath := newBackupTestDeps(t)

	// A path-traversal-shaped name must be rejected before ever touching
	// the filesystem -- ValidBackupName is the same guard db.StageRestore
	// relies on for the destructive restore path.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/backup/download/..%2F..%2Fetc%2Fpasswd", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a path-traversal-shaped name, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/backup/download/unitill-pos-20260101-000000.db", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a well-formed but non-existent backup, got %d: %s", rec.Code, rec.Body.String())
	}

	// Create a real snapshot, then download it for real.
	name := createRealSnapshot(t, dbPath)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/backup/download/"+name, nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Disposition") != `attachment; filename="`+name+`"` {
		t.Fatalf("expected a Content-Disposition attachment header, got %q", rec.Header().Get("Content-Disposition"))
	}
	if rec.Body.Len() == 0 {
		t.Fatalf("expected real backup file bytes in the response body")
	}
}

func TestSaveCopy_InvalidNameAndRealCopyToDownloads(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	// saveBackupToDownloads resolves the destination via os.UserHomeDir(),
	// which honors $HOME -- redirect it into a test-controlled directory
	// so this never touches the real developer's Downloads folder.
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	mux, _, dbPath := newBackupTestDeps(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backup/save-copy/..%2F..%2Fetc%2Fpasswd", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a path-traversal-shaped name, got %d: %s", rec.Code, rec.Body.String())
	}

	name := createRealSnapshot(t, dbPath)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/backup/save-copy/"+name, nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	dst := filepath.Join(fakeHome, "Downloads", name)
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("expected the backup really copied to %s: %v", dst, err)
	}
}

func TestRestoreBackup_RequiresRestoreConfirmationCaseInsensitive(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, dbPath := newBackupTestDeps(t)
	name := createRealSnapshot(t, dbPath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backup/restore", strings.NewReader("name="+name+"&confirm=nope"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without the RESTORE confirmation, got %d: %s", rec.Code, rec.Body.String())
	}
	if db.PendingRestore(dbPath) {
		t.Fatalf("expected no restore staged after a rejected confirmation")
	}

	// Case-insensitive per strings.ToUpper in the handler.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/backup/restore", strings.NewReader("name="+name+"&confirm=restore"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a lowercase 'restore' confirmation, got %d: %s", rec.Code, rec.Body.String())
	}
	if !db.PendingRestore(dbPath) {
		t.Fatalf("expected the restore to actually be staged on disk")
	}
	var action string
	if err := dp.Db.QueryRow(`SELECT action FROM audit_log WHERE action='restore_staged'`).Scan(&action); err != nil {
		t.Fatalf("expected a restore_staged audit row for this destructive action: %v", err)
	}
}

// createRealSnapshot triggers a real POST /api/backup/now against a fresh
// mux bound to the same dbPath, returning the resulting snapshot's file name.
// Note: db.Snapshot dedups within the same wall-clock second and returns the
// EXISTING file rather than creating a new one -- calling this twice for the
// same dbPath inside one second would fail the +1 assertion below. No test
// in this file does that today.
func createRealSnapshot(t *testing.T, dbPath string) string {
	t.Helper()
	before, err := db.ListBackups(dbPath)
	if err != nil {
		t.Fatalf("list backups before: %v", err)
	}
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db for snapshot: %v", err)
	}
	defer d.Close()
	if _, err := db.Snapshot(d.DB, dbPath); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	after, err := db.ListBackups(dbPath)
	if err != nil {
		t.Fatalf("list backups after: %v", err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("expected exactly one new backup, had %d now have %d", len(before), len(after))
	}
	return after[0].Name
}
