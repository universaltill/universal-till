package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#5071: nothing copies a backup off the till, so a manager is
// reminded — a status chip, never a modal — until a backup was downloaded
// in the last 7 days. A cashier never sees it.

const backupCopyChipLabel = "No backup copied off this till in 7 days"

func newBackupCopyChipMux(t *testing.T, now time.Time) (*http.ServeMux, *settings.Store, string) {
	t.Helper()
	mux, _, d := newFullAuthDeps(t)
	d.Cfg.DBPath = filepath.Join(t.TempDir(), "unitill-pos.db")
	orig := backupCopyChipNow
	backupCopyChipNow = func() time.Time { return now }
	t.Cleanup(func() { backupCopyChipNow = orig })
	registerBackupCopyChip(mux, d)
	registerBackupAPI(mux, d)
	return mux, d.Settings, d.Cfg.DBPath
}

func TestBackupCopyChip_FreshTillShowsNothingYet(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	mux, store, _ := newBackupCopyChipMux(t, now)
	if body := getAs(mux, "/ui/backup-copy-chip", &mgrUser).Body.String(); strings.TrimSpace(body) != "" {
		t.Fatalf("a till set up today must not nag: %q", body)
	}
	// The reminder clock starts at the first check, once.
	since, _, _ := store.Get(context.Background(), backupCopySinceKey)
	if since == "" {
		t.Fatal("reminder start not recorded")
	}
}

func TestBackupCopyChip_ManagerSeesItAfterSevenDaysWithoutACopy(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	mux, store, _ := newBackupCopyChipMux(t, now)
	if err := store.Set(context.Background(), backupCopySinceKey, now.Add(-8*24*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	body := getAs(mux, "/ui/backup-copy-chip", &mgrUser).Body.String()
	if !strings.Contains(body, backupCopyChipLabel) || !strings.Contains(body, `href="/settings#settings-backup"`) || !strings.Contains(body, `data-testid="sb-backup-copy"`) {
		t.Fatalf("manager chip missing: %s", body)
	}
	for _, modal := range []string{"<dialog", "showModal", `role="alertdialog"`} {
		if strings.Contains(body, modal) {
			t.Fatalf("chip carries %q — must never be a modal", modal)
		}
	}
	if body := getAs(mux, "/ui/backup-copy-chip", &cashUser).Body.String(); strings.TrimSpace(body) != "" {
		t.Fatalf("cashier sees the backup chip: %q", body)
	}
}

// Downloading a backup records the copy and clears the chip; a copy older
// than 7 days brings it back.
func TestBackupCopyChip_ADownloadClearsIt(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	mux, store, dbPath := newBackupCopyChipMux(t, now)
	ctx := context.Background()
	if err := store.Set(ctx, backupCopySinceKey, now.Add(-30*24*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	live, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := db.Snapshot(live.DB, dbPath)
	live.Close()
	if err != nil {
		t.Fatal(err)
	}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/backup/download/"+filepath.Base(snap), nil), mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d", rec.Code)
	}
	if body := getAs(mux, "/ui/backup-copy-chip", &mgrUser).Body.String(); strings.TrimSpace(body) != "" {
		t.Fatalf("chip still shown after a download: %q", body)
	}
	last, _, _ := store.Get(ctx, backupCopiedAtKey)
	if last == "" {
		t.Fatal("download not recorded")
	}
	if err := store.Set(ctx, backupCopiedAtKey, now.Add(-8*24*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if body := getAs(mux, "/ui/backup-copy-chip", &mgrUser).Body.String(); !strings.Contains(body, backupCopyChipLabel) {
		t.Fatalf("an 8-day-old copy must bring the chip back: %q", body)
	}
}

// The reminder start is written once; a later poll keeps it.
func TestBackupCopyChip_StartIsWrittenOnce(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	mux, store, _ := newBackupCopyChipMux(t, now)
	getAs(mux, "/ui/backup-copy-chip", &mgrUser)
	first, _, _ := store.Get(context.Background(), backupCopySinceKey)
	backupCopyChipNow = func() time.Time { return now.Add(3 * 24 * time.Hour) }
	getAs(mux, "/ui/backup-copy-chip", &mgrUser)
	if again, _, _ := store.Get(context.Background(), backupCopySinceKey); again != first {
		t.Fatalf("reminder start moved: %q -> %q", first, again)
	}
	backupCopyChipNow = func() time.Time { return now.Add(8 * 24 * time.Hour) }
	if body := getAs(mux, "/ui/backup-copy-chip", &mgrUser).Body.String(); !strings.Contains(body, backupCopyChipLabel) {
		t.Fatal("chip not shown 8 days after the first check")
	}
}

// A start in the future (a clock that stepped back) does not silence the
// reminder for good, and an anonymous caller writes nothing and sees nothing.
func TestBackupCopyChip_FutureStartAndAnonymous(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	mux, store, _ := newBackupCopyChipMux(t, now)
	if body := getAs(mux, "/ui/backup-copy-chip", nil).Body.String(); strings.TrimSpace(body) != "" {
		t.Fatalf("anonymous caller sees the chip: %q", body)
	}
	if v, _, _ := store.Get(context.Background(), backupCopySinceKey); v != "" {
		t.Fatalf("anonymous poll wrote the reminder start: %q", v)
	}
	if err := store.Set(context.Background(), backupCopySinceKey, now.Add(365*24*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if body := getAs(mux, "/ui/backup-copy-chip", &mgrUser).Body.String(); strings.TrimSpace(body) != "" {
		t.Fatalf("future start counted as overdue: %q", body)
	}
	// The future start was reset to now, so 8 days on the chip shows —
	// not a year from now.
	backupCopyChipNow = func() time.Time { return now.Add(8 * 24 * time.Hour) }
	if body := getAs(mux, "/ui/backup-copy-chip", &mgrUser).Body.String(); !strings.Contains(body, backupCopyChipLabel) {
		t.Fatal("a future reminder start silenced the chip")
	}
}

// Save to Downloads records the copy too; a HEAD probe of the download
// route does not.
func TestBackupCopyChip_SaveCopyRecordsAndHeadDoesNot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	mux, store, dbPath := newBackupCopyChipMux(t, now)
	live, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := db.Snapshot(live.DB, dbPath)
	live.Close()
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(snap)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, auth.WithUser(httptest.NewRequest(http.MethodHead, "/api/backup/download/"+name, nil), mgrUser))
	if v, _, _ := store.Get(context.Background(), backupCopiedAtKey); v != "" {
		t.Fatalf("a HEAD probe recorded a copy: %q", v)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/backup/save-copy/"+name, nil), mgrUser))
	if rec.Code != http.StatusOK {
		t.Fatalf("save-copy = %d %s", rec.Code, rec.Body.String())
	}
	if v, _, _ := store.Get(context.Background(), backupCopiedAtKey); v == "" {
		t.Fatal("save-copy did not record the copy")
	}
}
