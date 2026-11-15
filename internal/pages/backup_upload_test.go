package pages

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/settings"
)

// uploadBackupBody is a multipart body with one "file" part.
func uploadBackupBody(t *testing.T, name string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

// anotherTillsBackup is a backup file written on another till of a shop:
// a sale on 2026-10-20 and the shop name.
func anotherTillsBackup(t *testing.T, storeID string) []byte {
	t.Helper()
	p := filepath.Join(t.TempDir(), "unitill-pos.db")
	d, err := db.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO settings (key, value) VALUES ('store.name', 'Test 5070 Bakery') ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		`INSERT INTO settings (key, value) VALUES ('marketplace.store_id', '` + storeID + `') ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		`INSERT INTO sales (id, receipt_no, status, subtotal, total, created_at) VALUES ('s1', 'R-1', 'completed', 250, 250, '2026-10-20 17:45:00')`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := db.Snapshot(d.DB, p)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	raw, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// pinBackupUploadCap makes the size cap independent of the host's free disk
// space (a nearly full developer disk would answer 507 for every upload).
func pinBackupUploadCap(t *testing.T, n int64) {
	t.Helper()
	orig := backupUploadCap
	backupUploadCap = func(string) int64 { return n }
	t.Cleanup(func() { backupUploadCap = orig })
}

func postBackupUpload(mux *http.ServeMux, body *bytes.Buffer, ct string, user *auth.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/backup/upload", body)
	req.Header.Set("Content-Type", ct)
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// ut-docs#5070 (BINDING cashier-is-sale-only): the upload is refused on the
// server for a cashier, not only hidden.
func TestBackupUpload_RefusedForCashier(t *testing.T) {
	pinBackupUploadCap(t, 1<<30)
	t.Setenv("UT_AUTH", "")
	mux, dp, dbPath := newBackupTestDeps(t)
	dp.AuthSvc = auth.NewService(dp.Db)
	body, ct := uploadBackupBody(t, "unitill-pos-20261020-180000.db", anotherTillsBackup(t, "store-a"))
	rec := postBackupUpload(mux, body, ct, &auth.User{ID: "u1", Role: "cashier"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier upload = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if list, _ := db.ListBackups(dbPath); len(list) != 0 {
		t.Fatalf("a cashier's upload was kept: %v", list)
	}
}

// A manager uploads another till's backup: it is checked, kept as a
// restorable backup, and the answer shows what is in it, the cut-off, and
// a Restore button for exactly that file (which then asks for the PIN).
func TestBackupUpload_ManagerGetsTheCheckedSummary(t *testing.T) {
	pinBackupUploadCap(t, 1<<30)
	t.Setenv("UT_AUTH", "off")
	mux, dp, dbPath := newBackupTestDeps(t)
	body, ct := uploadBackupBody(t, "unitill-pos-20261020-180000.db", anotherTillsBackup(t, "store-a"))
	rec := postBackupUpload(mux, body, ct, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", rec.Code, rec.Body.String())
	}
	list, err := db.ListBackups(dbPath)
	if err != nil || len(list) != 1 || !db.IsUploadedBackupName(list[0].Name) {
		t.Fatalf("uploaded backup not kept: %v %v", list, err)
	}
	out := rec.Body.String()
	for _, want := range []string{
		"Test 5070 Bakery",
		"Sales made after that are not in this backup",
		`hx-post="/api/backup/restore"`,
		`name="name" value="` + list[0].Name + `"`,
		"Restore this backup",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q: %s", want, out)
		}
	}
	if db.PendingRestore(dbPath) {
		t.Fatal("an upload alone must never stage a restore")
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'backup_uploaded'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit backup_uploaded = %d %v", n, err)
	}
}

func TestBackupUpload_Refusals(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	cases := []struct {
		name     string
		content  func(t *testing.T) []byte
		liveShop string
		capBytes int64
		want     int
		wantText string
	}{
		{"not a backup", func(*testing.T) []byte { return []byte("a,b,c\n1,2,3\n") }, "", 0, http.StatusBadRequest, "not a Universal Till backup"},
		{"another shop", func(t *testing.T) []byte { return anotherTillsBackup(t, "store-a") }, "store-b", 0, http.StatusConflict, "belongs to another shop"},
		{"too large", func(t *testing.T) []byte { return anotherTillsBackup(t, "store-a") }, "", 1024, http.StatusRequestEntityTooLarge, "too large"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mux, dp, dbPath := newBackupTestDeps(t)
			if c.liveShop != "" {
				dp.Settings = settings.NewStore(dp.Db)
				if err := dp.Settings.Set(t.Context(), "marketplace.store_id", c.liveShop); err != nil {
					t.Fatal(err)
				}
			}
			if c.capBytes > 0 {
				pinBackupUploadCap(t, c.capBytes)
			} else {
				pinBackupUploadCap(t, 1<<30)
			}
			body, ct := uploadBackupBody(t, "file.db", c.content(t))
			rec := postBackupUpload(mux, body, ct, nil)
			if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.wantText) {
				t.Fatalf("= %d %q, want %d containing %q", rec.Code, rec.Body.String(), c.want, c.wantText)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
				t.Errorf("error must be an HTML fragment htmx swaps in place, got %q", ct)
			}
			if list, _ := db.ListBackups(dbPath); len(list) != 0 {
				t.Errorf("refused upload kept: %v", list)
			}
		})
	}
}

// ut-docs#5070: a restore first backs up the current data, so the
// replaced data can be got back from the list like any backup.
func TestRestoreBackup_TakesASafetyBackupFirst(t *testing.T) {
	pinBackupUploadCap(t, 1<<30)
	t.Setenv("UT_AUTH", "off")
	mux, dp, dbPath := newBackupTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO settings (key, value) VALUES ('marker.live', 'current-data')`); err != nil {
		t.Fatal(err)
	}
	body, ct := uploadBackupBody(t, "x.db", anotherTillsBackup(t, "store-a"))
	if rec := postBackupUpload(mux, body, ct, nil); rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	list, _ := db.ListBackups(dbPath)
	name := list[0].Name

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backup/restore", strings.NewReader("name="+name+"&override_pin="+dataAPITestManagerPIN))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !db.PendingRestore(dbPath) {
		t.Fatalf("restore = %d %s", rec.Code, rec.Body.String())
	}
	list, _ = db.ListBackups(dbPath)
	var safety string
	for _, b := range list {
		if !db.IsUploadedBackupName(b.Name) {
			safety = b.Name
		}
	}
	if safety == "" {
		t.Fatalf("no safety backup of the current data: %v", list)
	}
	dir, _ := db.BackupDir(dbPath)
	sd, err := db.OpenReadOnly(filepath.Join(dir, safety))
	if err != nil {
		t.Fatal(err)
	}
	defer sd.Close()
	var v string
	if err := sd.QueryRow(`SELECT value FROM settings WHERE key = 'marker.live'`).Scan(&v); err != nil || v != "current-data" {
		t.Fatalf("safety backup does not hold the current data: %q %v", v, err)
	}
	var payload string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'restore_staged'`).Scan(&payload); err != nil || !strings.Contains(payload, safety) {
		t.Fatalf("restore_staged audit must name the safety backup: %q %v", payload, err)
	}
}

// No room above the low-disk floor: the upload is refused before reading.
func TestBackupUpload_NoSpace(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	pinBackupUploadCap(t, 0)
	mux, _, dbPath := newBackupTestDeps(t)
	body, ct := uploadBackupBody(t, "x.db", []byte("anything"))
	rec := postBackupUpload(mux, body, ct, nil)
	if rec.Code != http.StatusInsufficientStorage || !strings.Contains(rec.Body.String(), "not enough free space") {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	if list, _ := db.ListBackups(dbPath); len(list) != 0 {
		t.Fatalf("kept: %v", list)
	}
}
