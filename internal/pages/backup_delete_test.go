package pages

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
)

// seedBackupFile writes a fake snapshot aged ageHours and returns its name.
func seedBackupFile(t *testing.T, dbPath string, ageHours int) string {
	t.Helper()
	dir, err := db.BackupDir(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Duration(ageHours) * time.Hour)
	name := "unitill-pos-" + at.UTC().Format("20060102-150405") + ".db"
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return name
}

// seededManagerID is the manager newBackupTestDeps seeded: the audit log's
// actor column is a foreign key, so a real user id is needed to see the row.
func seededManagerID(t *testing.T, d *sql.DB) string {
	t.Helper()
	var id string
	if err := d.QueryRow(`SELECT id FROM users WHERE role = 'manager' LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func backupExists(dbPath, name string) bool {
	dir, _ := db.BackupDir(dbPath)
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func postBackupForm(mux *http.ServeMux, path string, form url.Values, u *auth.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if u != nil {
		req = auth.WithUser(req, *u)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// ut-docs#3092 (reopened): the backups list had no delete button, and the
// server must refuse a cashier even if the button is forged.
func TestBackupDelete_ServerRefusesCashier_ManagerDeletes_AuditsOnce(t *testing.T) {
	mux, dp, dbPath := newBackupTestDeps(t)
	older := seedBackupFile(t, dbPath, 72)
	newest := seedBackupFile(t, dbPath, 1)
	cashier := auth.User{ID: "u1", Role: "cashier"}
	manager := auth.User{ID: seededManagerID(t, dp.Db), Role: "manager"}

	rec := postBackupForm(mux, "/api/backup/delete", url.Values{"name": {older}}, &cashier)
	if rec.Code != http.StatusForbidden || !backupExists(dbPath, older) {
		t.Fatalf("cashier = %d, file kept=%v; want 403 and the file kept", rec.Code, backupExists(dbPath, older))
	}
	rec = postBackupForm(mux, "/api/backup/delete", url.Values{"name": {older}}, nil)
	if rec.Code != http.StatusForbidden || !backupExists(dbPath, older) {
		t.Fatalf("no session = %d, want 403 and the file kept", rec.Code)
	}

	rec = postBackupForm(mux, "/api/backup/delete", url.Values{"name": {older}}, &manager)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager = %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-UT-Response") != "ok" {
		t.Fatalf("the backups card refreshes on X-UT-Response: ok, got %q", rec.Header().Get("X-UT-Response"))
	}
	if backupExists(dbPath, older) {
		t.Fatal("manager delete left the file")
	}
	if !backupExists(dbPath, newest) {
		t.Fatal("the newest backup disappeared")
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='backup_deleted'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("backup_deleted audit rows = %d (%v), want 1", n, err)
	}
}

func TestBackupDelete_RefusesNewestInvalidAndMissing(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, dbPath := newBackupTestDeps(t)
	seedBackupFile(t, dbPath, 72)
	newest := seedBackupFile(t, dbPath, 1)

	rec := postBackupForm(mux, "/api/backup/delete", url.Values{"name": {newest}}, nil)
	if rec.Code != http.StatusConflict || !backupExists(dbPath, newest) {
		t.Fatalf("newest = %d (kept=%v), want 409 and kept", rec.Code, backupExists(dbPath, newest))
	}
	for _, bad := range []string{"../x.db", "pre-restore-20200101-000000.db", "restore-pending.db", ""} {
		if rec := postBackupForm(mux, "/api/backup/delete", url.Values{"name": {bad}}, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("name %q = %d, want 400", bad, rec.Code)
		}
	}
	if rec := postBackupForm(mux, "/api/backup/delete", url.Values{"name": {"unitill-pos-29990101-000000.db"}}, nil); rec.Code != http.StatusNotFound {
		t.Errorf("missing = %d, want 404", rec.Code)
	}
}

func TestBackupRetentionSetting(t *testing.T) {
	mux, dp, _ := newBackupTestDeps(t)
	cashier := auth.User{ID: "u1", Role: "cashier"}
	manager := auth.User{ID: seededManagerID(t, dp.Db), Role: "manager"}

	if rec := postBackupForm(mux, "/api/backup/retention", url.Values{"days": {"30"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d, want 403", rec.Code)
	}
	if got := data.BackupRetentionDays(t.Context(), dp.Db); got != 7 {
		t.Fatalf("refused save changed the setting to %d", got)
	}
	for _, bad := range []string{"0", "-1", "abc", "", "366"} {
		if rec := postBackupForm(mux, "/api/backup/retention", url.Values{"days": {bad}}, &manager); rec.Code != http.StatusBadRequest {
			t.Errorf("days %q = %d, want 400", bad, rec.Code)
		}
	}
	if rec := postBackupForm(mux, "/api/backup/retention", url.Values{"days": {"30"}}, &manager); rec.Code != http.StatusOK {
		t.Fatalf("manager = %d: %s", rec.Code, rec.Body.String())
	}
	if got := data.BackupRetentionDays(t.Context(), dp.Db); got != 30 {
		t.Fatalf("retention = %d, want 30", got)
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='backup_retention_changed'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows = %d (%v), want 1", n, err)
	}
}

// "Back up now" prunes backups older than the retention days, never the new one.
func TestBackupNow_PrunesBackupsOlderThanRetention(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, dbPath := newBackupTestDeps(t)
	var old, young []string
	for i := 1; i <= 14; i++ {
		n := seedBackupFile(t, dbPath, i*24+1)
		if i < 7 {
			young = append(young, n)
		} else {
			old = append(old, n)
		}
	}
	if rec := postBackupForm(mux, "/api/backup/now", url.Values{}, nil); rec.Code != http.StatusOK {
		t.Fatalf("backup now = %d", rec.Code)
	}
	for _, n := range old {
		if backupExists(dbPath, n) {
			t.Errorf("%s (older than 7 days) was kept", n)
		}
	}
	for _, n := range young {
		if !backupExists(dbPath, n) {
			t.Errorf("%s (within 7 days) was removed", n)
		}
	}
	list, _ := db.ListBackups(dbPath)
	if len(list) != 7 { // 6 young + the new one
		t.Fatalf("%d backups left, want 7", len(list))
	}
}

// The Backups card: Delete on every row but the newest. (The whole page is
// manager-only; a cashier gets 403 on /settings, and on the endpoint above.)
func TestSettingsPage_BackupRowsHaveDeleteButtonExceptNewest(t *testing.T) {
	dp := newMigratedSyncDeps(t, "settings-backup-delete.db")
	dp.AuthSvc = auth.NewService(dp.Db)
	// The backups folder sits next to the DB file: keep it in a temp dir,
	// not in the repo root the page tests run from.
	dp.Cfg.DBPath = filepath.Join(t.TempDir(), "unitill-pos.db")
	initPagesI18n(t)
	mux := http.NewServeMux()
	registerSettings(mux, dp)
	older := seedBackupFile(t, dp.Cfg.DBPath, 72)
	newest := seedBackupFile(t, dp.Cfg.DBPath, 1)

	get := func(u auth.User) string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}
	body := get(mgrUser)
	if !strings.Contains(body, `data-testid="backup-delete-`+older+`"`) {
		t.Errorf("no Delete button on the older backup row")
	}
	if strings.Contains(body, `data-testid="backup-delete-`+newest+`"`) {
		t.Errorf("the newest backup row has a Delete button")
	}
	// "Newest" sits in the file-name cell, not the action cell: there it
	// wrapped beside the buttons on the 1280x800 tablet and broke the row.
	if !strings.Contains(body, `data-testid="backup-newest-`+newest+`"`) {
		t.Errorf("the newest row does not say it is the newest")
	}
	if !strings.Contains(body, `hx-confirm="`) || !strings.Contains(body, httpx.T("en", "settings.backup.delete_confirm")[:20]) {
		t.Errorf("Delete must ask for confirmation")
	}
	if !strings.Contains(body, `name="days"`) || !strings.Contains(body, fmt.Sprintf(`value="%d"`, data.DefaultBackupRetentionDays)) {
		t.Errorf("no retention-days input with the default")
	}
}

// Saving a smaller number must not delete anything by itself (a typo of 3 for
// 30 would wipe history with one tap); it takes effect at the next backup.
func TestBackupRetentionSave_DoesNotPruneByItself(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, dbPath := newBackupTestDeps(t)
	old := seedBackupFile(t, dbPath, 20*24)
	seedBackupFile(t, dbPath, 1)
	if rec := postBackupForm(mux, "/api/backup/retention", url.Values{"days": {"3"}}, nil); rec.Code != http.StatusOK {
		t.Fatalf("save = %d", rec.Code)
	}
	if !backupExists(dbPath, old) {
		t.Fatal("saving the setting deleted a backup")
	}
	if rec := postBackupForm(mux, "/api/backup/now", url.Values{}, nil); rec.Code != http.StatusOK {
		t.Fatalf("backup now = %d", rec.Code)
	}
	if backupExists(dbPath, old) {
		t.Fatal("the next backup did not prune by the new setting")
	}
}

// An uploaded file is the newest by mtime, but the Newest marker (and the
// missing Delete button) belong to the till's own newest snapshot.
func TestListBackupsForUI_NewestIsTheTillsOwnSnapshot(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	_, dp, dbPath := newBackupTestDeps(t)
	own := seedBackupFile(t, dbPath, 48)
	dir, _ := db.BackupDir(dbPath)
	up := "unitill-pos-20260101-000000-file1.db"
	if err := os.WriteFile(filepath.Join(dir, up), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, r := range listBackupsForUI(dp, "en") {
		if want := r.Name == own; r.Newest != want {
			t.Errorf("%s Newest=%v, want %v", r.Name, r.Newest, want)
		}
	}
}
