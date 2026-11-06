package pages

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/diskspace"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/storageusage"
)

// ut-docs#3122: Settings → Storage, the read-only card and its lazily
// loaded fragment.

func storageWrite(t *testing.T, root, rel string, n int) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newStorageMux points the fragment at a temp data dir and a pinned disk.
func newStorageMux(t *testing.T, usage diskspace.Usage, probeErr error) (*http.ServeMux, string, *common.Deps) {
	t.Helper()
	t.Setenv("UT_AUTH", "on")
	root := t.TempDir()
	origLayout, origProbe := storageLayoutFor, diskSpaceChipProbe
	storageLayoutFor = func(*common.Deps) storageusage.Layout {
		return storageusage.Layout{
			DBPath: filepath.Join(root, "pos.db"), DataDir: root,
			BackupsDir:    filepath.Join(root, "backups"),
			LogsDir:       filepath.Join(root, "logs"),
			BugReportsDir: filepath.Join(root, "issue-reports", "pending"),
			UpdatesDir:    filepath.Join(root, "updates"),
		}
	}
	diskSpaceChipProbe = func(string) (diskspace.Usage, error) { return usage, probeErr }
	t.Cleanup(func() { storageLayoutFor, diskSpaceChipProbe = origLayout, origProbe })
	mux, _, d := newFullAuthDeps(t)
	return mux, root, d
}

func seedStorage(t *testing.T, root string) {
	storageWrite(t, root, "pos.db", 1000)
	storageWrite(t, root, "pos.db-wal", 200)
	storageWrite(t, root, "pos.db-shm", 30)
	storageWrite(t, root, "backups/unitill-pos-A.db", 500)
	storageWrite(t, root, "backups/unitill-pos-B.db", 500)
	storageWrite(t, root, "backups/pre-restore-X.db", 700)
	storageWrite(t, root, "logs/till.log", 300)
	storageWrite(t, root, "issue-reports/pending/r1/x", 40)
	storageWrite(t, root, "updates/attempt-1/setup.exe", 900)
	storageWrite(t, root, "photos/a.jpg", 4096)
}

var roomyDisk = diskspace.Usage{Free: 40 << 30, Total: 64 << 30}

func storageRowHTML(body, testid string) string {
	re := regexp.MustCompile(`(?s)<tr[^>]*data-testid="` + regexp.QuoteMeta(testid) + `"[^>]*>(.*?)</tr>`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return m[1]
}

func TestSettingsStorage_CashierGets403(t *testing.T) {
	mux, root, _ := newStorageMux(t, roomyDisk, nil)
	seedStorage(t, root)
	for name, u := range map[string]bool{"cashier": true, "no session": false} {
		var rec = getAs(mux, "/ui/settings/storage", nil)
		if u {
			rec = getAs(mux, "/ui/settings/storage", &cashUser)
		}
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: code=%d, want 403", name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "storage-row-") {
			t.Fatalf("%s: 403 still carries sizes", name)
		}
	}
}

func TestSettingsStorage_ManagerSeesSixRowsWithSizes(t *testing.T) {
	mux, root, _ := newStorageMux(t, roomyDisk, nil)
	seedStorage(t, root)
	rec := getAs(mux, "/ui/settings/storage", &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	body := rec.Body.String()
	want := map[string]int64{"database": 1230, "backups": 1700, "logs": 300, "bug_reports": 40, "updates": 900, "other": 4096}
	for cat, n := range want {
		row := storageRowHTML(body, "storage-row-"+cat)
		if row == "" {
			t.Fatalf("no storage-row-%s in %s", cat, body)
		}
		if exp := httpx.FormatBytes(n, "en"); !strings.Contains(row, "<bdi>"+exp+"</bdi>") {
			t.Errorf("row %s = %s, want size %q", cat, row, exp)
		}
	}
	if !strings.Contains(body, `data-testid="storage-disk"`) || !strings.Contains(body, "40.0 GB") || !strings.Contains(body, "64.0 GB") {
		t.Errorf("disk line missing or wrong: %s", body)
	}
	if strings.Contains(body, "storage-disk-low") {
		t.Errorf("roomy disk must not show the low-disk notice")
	}
	if strings.Contains(body, "storage-skipped") {
		t.Errorf("nothing was skipped, the note must be absent")
	}
}

func TestSettingsStorage_DiskLineHiddenWhenProbeUnsupported(t *testing.T) {
	mux, root, _ := newStorageMux(t, diskspace.Usage{}, diskspace.ErrUnsupported)
	seedStorage(t, root)
	rec := getAs(mux, "/ui/settings/storage", &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "storage-disk") {
		t.Errorf("unsupported probe must hide the disk line: %s", rec.Body.String())
	}
}

func TestSettingsStorage_LowDiskHint(t *testing.T) {
	mux, root, _ := newStorageMux(t, diskspace.Usage{Free: 100 << 20, Total: 16 << 30}, nil)
	seedStorage(t, root)
	body := getAs(mux, "/ui/settings/storage", &mgrUser).Body.String()
	if !strings.Contains(body, `data-testid="storage-disk-low"`) || !strings.Contains(body, `role="status"`) {
		t.Errorf("low disk must show the notice: %s", body)
	}
	if !strings.Contains(body, "notice-block-warn") || !strings.Contains(body, "row-warn-icon") {
		t.Errorf("notice must use notice-block-warn with the icon: %s", body)
	}
}

func TestSettingsStorage_EmptyDirsStill200(t *testing.T) {
	mux, _, _ := newStorageMux(t, roomyDisk, nil)
	rec := getAs(mux, "/ui/settings/storage", &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	for _, cat := range []string{"database", "backups", "logs", "bug_reports", "updates", "other"} {
		if row := storageRowHTML(rec.Body.String(), "storage-row-"+cat); !strings.Contains(row, "<bdi>0 B</bdi>") {
			t.Errorf("row %s = %q, want 0 B", cat, row)
		}
	}
}

func TestSettingsStorage_BackupRowShowsTheTillsDays(t *testing.T) {
	mux, _, d := newStorageMux(t, roomyDisk, nil)
	rec := getAs(mux, "/ui/settings/storage", &mgrUser)
	if row := storageRowHTML(rec.Body.String(), "retention-backups"); !strings.Contains(row, "last 7 days") {
		t.Fatalf("default: backups row = %q, want 7 days", row)
	}
	if _, err := d.Db.Exec(`INSERT INTO settings(key, value) VALUES (?, '21')`, data.BackupRetentionDaysSettingsKey); err != nil {
		t.Fatal(err)
	}
	row := storageRowHTML(getAs(mux, "/ui/settings/storage", &mgrUser).Body.String(), "retention-backups")
	if !strings.Contains(row, "last 21 days") || strings.Contains(row, "last 7 days") {
		t.Fatalf("backup.retention_days=21: backups row = %q", row)
	}
}

func TestSettingsStorage_PreRestoreRowShowsCountryFloor(t *testing.T) {
	mux, _, d := newStorageMux(t, roomyDisk, nil)
	row := storageRowHTML(getAs(mux, "/ui/settings/storage", &mgrUser).Body.String(), "retention-pre_restore_copies")
	if want := httpx.FormatDecimal(strconv.FormatInt(data.GlobalArchiveMinDays, 10), "en") + " days"; !strings.Contains(row, want) {
		t.Fatalf("no country: row = %q, want %q", row, want)
	}
	if _, err := d.Db.Exec(`UPDATE country_settings SET archive_min_days = 4000 WHERE code = 'GB'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO settings(key, value) VALUES ('store.country', 'GB')`); err != nil {
		t.Fatal(err)
	}
	row = storageRowHTML(getAs(mux, "/ui/settings/storage", &mgrUser).Body.String(), "retention-pre_restore_copies")
	if !strings.Contains(row, "4,000 days") {
		t.Fatalf("GB floor: row = %q, want 4,000 days", row)
	}
}

func TestSettingsStorage_SyncRowsCollapsed(t *testing.T) {
	mux, _, _ := newStorageMux(t, roomyDisk, nil)
	body := getAs(mux, "/ui/settings/storage", &mgrUser).Body.String()
	i := strings.Index(body, `data-testid="storage-sync-details"`)
	if i < 0 {
		t.Fatalf("no storage-sync-details: %s", body)
	}
	if strings.Contains(body[i:i+60], " open") {
		t.Errorf("sync details must start closed")
	}
	outside, inside := body[:i], body[i:]
	if n := strings.Count(outside, `data-testid="retention-`); n != 7 {
		t.Errorf("rules outside the details = %d, want 7", n)
	}
	if n := strings.Count(inside, `data-testid="retention-`); n != 5 {
		t.Errorf("rules inside the details = %d, want 5", n)
	}
	visible := strings.ToLower(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(body, " "))
	for _, bad := range []string{"database", "ledger", "tombstone", "spool", "batch"} {
		if strings.Contains(visible, bad) {
			t.Errorf("developer word %q reached the merchant", bad)
		}
	}
}

func TestSettingsStorage_NoModalNoScript(t *testing.T) {
	mux, root, _ := newStorageMux(t, diskspace.Usage{Free: 100 << 20, Total: 16 << 30}, nil)
	seedStorage(t, root)
	body := getAs(mux, "/ui/settings/storage", &mgrUser).Body.String()
	for _, bad := range []string{"showModal", "<dialog", "<script", "onclick", "onchange"} {
		if strings.Contains(body, bad) {
			t.Errorf("fragment carries %q", bad)
		}
	}
}

// A failed measure answers an html fragment (app.js swaps a non-2xx
// text/html fragment into the target) with a retry button, not a bare error.
func TestSettingsStorage_MeasureFailureShowsRetry(t *testing.T) {
	mux, _, _ := newStorageMux(t, roomyDisk, nil)
	orig := storageMeasure
	storageMeasure = func(context.Context, storageusage.Layout) (storageusage.Report, error) {
		return storageusage.Report{}, errors.New("boom")
	}
	t.Cleanup(func() { storageMeasure = orig })
	rec := getAs(mux, "/ui/settings/storage", &mgrUser)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"The sizes could not be measured just now.", `hx-get="/ui/settings/storage"`, `hx-target="#settings-storage-body"`, "Try again"} {
		if !strings.Contains(body, want) {
			t.Errorf("error fragment lacks %q: %s", want, body)
		}
	}
	if strings.Contains(body, "boom") || strings.Contains(body, "<html") {
		t.Errorf("error fragment leaks the cause or is a full page: %s", body)
	}
}

func TestSettingsStorage_ClientGoneWritesNothing(t *testing.T) {
	mux, _, _ := newStorageMux(t, roomyDisk, nil)
	orig := storageMeasure
	storageMeasure = func(context.Context, storageusage.Layout) (storageusage.Report, error) {
		return storageusage.Report{}, context.Canceled
	}
	t.Cleanup(func() { storageMeasure = orig })
	rec := getAs(mux, "/ui/settings/storage", &mgrUser)
	if strings.TrimSpace(rec.Body.String()) != "" {
		t.Errorf("a cancelled request must write nothing, got %s", rec.Body.String())
	}
}

func TestSettingsStorage_SkippedNote(t *testing.T) {
	mux, _, _ := newStorageMux(t, roomyDisk, nil)
	orig := storageMeasure
	storageMeasure = func(context.Context, storageusage.Layout) (storageusage.Report, error) {
		return storageusage.Report{Rows: []storageusage.Row{{Category: "database"}, {Category: "backups"}, {Category: "logs"}, {Category: "bug_reports"}, {Category: "updates"}, {Category: "other"}}, Skipped: 2}, nil
	}
	t.Cleanup(func() { storageMeasure = orig })
	body := getAs(mux, "/ui/settings/storage", &mgrUser).Body.String()
	if !strings.Contains(body, `data-testid="storage-skipped"`) || !strings.Contains(body, "may be a little low") {
		t.Errorf("skipped note missing: %s", body)
	}
}

func TestSettingsPage_StorageCardLoadsLazily(t *testing.T) {
	mux, _, _ := newStorageMux(t, roomyDisk, nil)
	body := renderSettingsAs(t, mux, mgrUser, false)
	for _, want := range []string{`id="settings-storage"`, `hx-get="/ui/settings/storage"`, `hx-trigger="intersect once"`, `id="settings-storage-body"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/settings lacks %q", want)
		}
	}
	if !regexp.MustCompile(`<li data-key="settings-storage" data-group="Backup &amp; updates" data-cat="backup">Storage</li>`).MatchString(body) {
		t.Errorf("sidebar index lacks the Storage row: %s", regexp.MustCompile(`(?s)id="settings-nav-index".*?</ul>`).FindString(body))
	}
	if strings.Contains(body, "storage-row-") {
		t.Errorf("/settings must not carry the measured sizes (they load lazily)")
	}
}
