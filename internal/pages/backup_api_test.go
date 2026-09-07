package pages

import (
	"encoding/json"
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
		{http.MethodPost, "/api/backup/restart-now"},
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

// ut-docs#924: GET /api/backup/download/{name} used to leak the raw
// filesystem error via http.Error(w, err.Error(), ...) when BackupDir
// couldn't create the backups directory. Force a real failure (a regular
// file sitting where the "backups" directory needs to be created) and
// assert the localized fallback shows, never the raw error text.
func TestDownloadBackup_BackupDirErrorIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, dbPath := newBackupTestDeps(t)

	blockedDir := filepath.Join(filepath.Dir(dbPath), "backups")
	if err := os.WriteFile(blockedDir, []byte("x"), 0o644); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/backup/download/unitill-pos-99999999-999999.db", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("download with blocked backups dir = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Could not download the backup") {
		t.Fatalf("download error body = %q, want the localized download-failed message", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "not a directory") {
		t.Fatalf("download error body leaked raw filesystem error text: %q", rec.Body.String())
	}
}

// ut-docs#924: POST /api/backup/restore used to leak the raw "backup not
// found" error via http.Error(w, err.Error(), ...) when StageRestore
// couldn't find the named snapshot. Assert the localized fallback shows.
func TestRestoreBackup_MissingSnapshotIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newBackupTestDeps(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backup/restore",
		strings.NewReader("name=unitill-pos-99999999-999999.db&confirm=RESTORE"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("restore of a missing snapshot = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Could not prepare the restore") {
		t.Fatalf("restore error body = %q, want the localized stage-failed message", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "backup not found") {
		t.Fatalf("restore error body leaked raw error text: %q", rec.Body.String())
	}
}

// --- POST /api/backup/restart-now (ut-docs#1613): the restore-staged
// screen's restart trigger, mirroring pairing_restart_test.go's coverage of
// pairingRestartHandler (ut-docs#1550) exactly — same seams, same refuse-
// unless-staged guard, same envelope. A real call would syscall.Exec the
// test binary mid-run, so backupRestartFn is stubbed throughout. ---

func stubBackupRestart(t *testing.T) *int {
	t.Helper()
	calls := 0
	old := backupRestartFn
	backupRestartFn = func() { calls++ }
	t.Cleanup(func() { backupRestartFn = old })
	return &calls
}

func stubBackupRestartSupported(t *testing.T, v bool) {
	t.Helper()
	old := backupRestartSupported
	backupRestartSupported = func() bool { return v }
	t.Cleanup(func() { backupRestartSupported = old })
}

func stubBackupRestorePending(t *testing.T, v bool) {
	t.Helper()
	old := backupRestorePending
	backupRestorePending = func(string) bool { return v }
	t.Cleanup(func() { backupRestorePending = old })
}

func TestBackupRestartNow_SchedulesRestartAndAnswersEnvelope(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	calls := stubBackupRestart(t)
	stubBackupRestorePending(t, true)
	mux, _, _ := newBackupTestDeps(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backup/restart-now", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if *calls != 1 {
		t.Fatalf("expected exactly one restart to be scheduled, got %d", *calls)
	}
	var out struct {
		Data struct {
			Restarting bool `json:"restarting"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not the JSON envelope: %v: %s", err, rec.Body.String())
	}
	if !out.Data.Restarting || out.Error != nil {
		t.Fatalf("want {data:{restarting:true}, error:null}, got %s", rec.Body.String())
	}
}

// Review finding on ut-docs#1550 applies identically here: without a staged
// restore, this route would be an unconditional restart of a configured,
// possibly-in-use till that anyone who can reach it could fire for no
// reason. StageRestore runs strictly before this route can do anything
// useful, so refusing when nothing is staged is always safe.
func TestBackupRestartNow_RefusesWhenNothingIsStaged(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	calls := stubBackupRestart(t)
	stubBackupRestorePending(t, false)
	mux, _, _ := newBackupTestDeps(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backup/restart-now", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("no staged restore: expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if *calls != 0 {
		t.Fatalf("a refused request must never schedule a restart (got %d calls)", *calls)
	}
	var out struct {
		Data  any    `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not the JSON envelope: %v: %s", err, rec.Body.String())
	}
	if out.Data != nil || out.Error == "" {
		t.Fatalf("want {data:null, error:\"…\"}, got %s", rec.Body.String())
	}
}

// The manager gate applies to this route exactly like every other backup
// route in this file (real session, not just the no-session 403 case
// TestBackupAPI_AllEndpointsRequireManager already covers).
func TestBackupRestartNow_RealSessionGatesByRole(t *testing.T) {
	calls := stubBackupRestart(t)
	stubBackupRestorePending(t, true)
	mux, dp, _ := newBackupTestDeps(t)
	dp.AuthSvc = auth.NewService(dp.Db)

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/backup/restart-now", nil), auth.User{ID: "u1", Role: "cashier"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || *calls != 0 {
		t.Fatalf("cashier = %d (calls %d), want 403 and no restart", rec.Code, *calls)
	}

	req = auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/backup/restart-now", nil), auth.User{ID: "u1", Role: "manager"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || *calls != 1 {
		t.Fatalf("manager = %d (calls %d), want 200 and one restart", rec.Code, *calls)
	}
}

// --- The restore-staged render itself (web/ui/partials/backup_restore_staged.html). ---

// Where an in-place restart is possible (every non-Windows build), a
// successful restore gives the operator a real "Restart now" button
// instead of the old dead-end "restart the till to finish" text — same
// class of fix as ut-docs#1550's pairing screen.
func TestRestoreBackup_SuccessRendersRestartButtonWhenSupported(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	stubBackupRestartSupported(t, true)
	mux, _, dbPath := newBackupTestDeps(t)
	name := createRealSnapshot(t, dbPath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backup/restore", strings.NewReader("name="+name+"&confirm=RESTORE"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-post="/api/backup/restart-now"`) {
		t.Fatalf("want a restart trigger posting to /api/backup/restart-now, got: %s", body)
	}
	if !strings.Contains(body, httpx.T("en", "settings.backup.restart_now")) {
		t.Fatalf("want the visible Restart now button, got: %s", body)
	}
	if !strings.Contains(body, "/healthz") || !strings.Contains(body, "/login") {
		t.Fatalf("want the healthz poll that lands on /login once the till is back: %s", body)
	}
	if strings.Contains(body, httpx.T("en", "tills.pairing.close_and_reopen")) {
		t.Fatalf("the Windows close-and-reopen instruction must not show where restart works: %s", body)
	}
}

// Where it isn't (Windows, ut-docs#1614 tracks a native restart there), the
// screen must not show a button that does nothing.
func TestRestoreBackup_SuccessShowsCloseAndReopenWhenUnsupported(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	stubBackupRestartSupported(t, false)
	mux, _, dbPath := newBackupTestDeps(t)
	name := createRealSnapshot(t, dbPath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backup/restore", strings.NewReader("name="+name+"&confirm=RESTORE"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, httpx.T("en", "tills.pairing.close_and_reopen")) {
		t.Fatalf("want the close-and-reopen instruction, got: %s", body)
	}
	for _, forbidden := range []string{"restart-now", "/healthz", httpx.T("en", "settings.backup.restart_now")} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unsupported platform must not render %q: %s", forbidden, body)
		}
	}
}
