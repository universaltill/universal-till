package pages

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// File-rows import entries (ADR-0164 §2, ut-docs#4526): an import entry
// declaring "source":"file" parses an uploaded file in the import sandbox
// and hands rows back through import_rows_submit; core applies them with
// catsync (mode=preview|commit), exactly as for an API-source entry.

const fileRowsPluginID = "com.test.filerows"

// fixtureExport is what the test guest (plugins/testdata/filerows_guest)
// parses: its header is the entry's file_headers claim.
const fixtureExport = "fixture_ref,fixture_title,fixture_price_minor\nr1,Tea,250\nr2,Scone,300\n"

// seedFileRowsPlugin installs an active plugin with one "source":"file"
// import entry for .csv files claimed by the fixture header.
func seedFileRowsPlugin(t *testing.T, db *sql.DB, grantWrite []string) {
	t.Helper()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	cfg, _ := json.Marshal(map[string]any{"entities": []string{"items"}, "source": "file",
		"file_formats": []string{".csv"}, "file_headers": []string{"fixture_ref", "Fixture_Title"}})
	mustExec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, min_pos_version, api_version, published_at) VALUES (?, '1.0.0', 'Fixture POS', 'desc', 'wasm', './plugin.wasm', 'url', 'sha', 'auth', 'site', '[]', '0.0.0', '1', datetime('now'))`, fileRowsPluginID)
	mustExec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES (?, 'Fixture POS', '1.0.0', './plugin.wasm', 'wasm', 1)`, fileRowsPluginID)
	mustExec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, is_active, sort_order, config_json)
	          VALUES (?, ?, 'export', 'Back office export', 'import', 1, 0, ?)`, fileRowsPluginID+"-e", fileRowsPluginID, string(cfg))
	mustExec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, 'events:receive', 1)`, fileRowsPluginID+"-p", fileRowsPluginID)
	mustExec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?, ?, 'import.requested.ask', 'import', 1)`, fileRowsPluginID+"-h", fileRowsPluginID)
	for _, e := range grantWrite {
		mustExec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, ?, 1)`, fileRowsPluginID+"-p-"+e, fileRowsPluginID, e+":write")
	}
}

var (
	fileRowsGuestOnce sync.Once
	fileRowsGuestPath string
	fileRowsGuestErr  error
)

// installFileRowsGuest builds plugins/testdata/filerows_guest (once per
// test binary) and runs it as fileRowsPluginID through the real runtime.
func installFileRowsGuest(t *testing.T, dp *common.Deps) {
	t.Helper()
	fileRowsGuestOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		dir, err := os.MkdirTemp("", "filerows-guest-")
		if err != nil {
			fileRowsGuestErr = err
			return
		}
		fileRowsGuestPath = filepath.Join(dir, "plugin.wasm")
		cmd := exec.Command("go", "build", "-o", fileRowsGuestPath, "../plugins/testdata/filerows_guest")
		cmd.Dir = filepath.Dir(file)
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
		if raw, err := cmd.CombinedOutput(); err != nil {
			fileRowsGuestErr = err
			fileRowsGuestPath = string(raw)
		}
	})
	if fileRowsGuestErr != nil {
		t.Fatalf("build filerows guest: %v\n%s", fileRowsGuestErr, fileRowsGuestPath)
	}
	raw, err := os.ReadFile(fileRowsGuestPath)
	if err != nil {
		t.Fatal(err)
	}
	baseDir := t.TempDir()
	modDir := filepath.Join(baseDir, fileRowsPluginID, "1.0.0")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "plugin.wasm"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	w := plugins.NewWasmRuntime(baseDir)
	t.Cleanup(func() {
		w.Close(context.Background())
		bus.ResetSubscribers()
	})
	w.Sync(context.Background(), dp.Db)
	bus.SetEventMode("import.requested.ask", plugins.Blocking)
}

// newFileRowsTestMux is the data API plus the import page, with the fixture
// plugin installed and running.
func newFileRowsTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	registerImport(mux, dp)
	seedFileRowsPlugin(t, dp.Db, []string{"items"})
	installFileRowsGuest(t, dp)
	return mux, dp
}

// postMultipart posts content as "file" (named fileName) plus fields.
func postMultipart(t *testing.T, mux *http.ServeMux, path, fileName, content string, fields map[string]string, user *auth.User) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The JSON API: preview writes nothing, commit creates and links under the
// plugin's id (core-stamped), a re-run is all unchanged, one audit entry
// per commit carrying the file's SHA-256 and never its contents.
func TestFileRowsImport_JSONAPI_PreviewThenCommit(t *testing.T) {
	mux, dp := newFileRowsTestMux(t)
	itemsBefore := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`)
	fields := func(mode string) map[string]string { return map[string]string{"entry_key": "export", "mode": mode} }

	pre := decodeAPIImport(t, postMultipart(t, mux, "/api/data/import", "export.csv", fixtureExport, fields("preview"), nil))
	if pre.Mode != "preview" || pre.Counts.New != 2 || len(pre.Samples) != 2 {
		t.Fatalf("preview %+v", pre)
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`); n != itemsBefore {
		t.Fatalf("preview wrote items: %d -> %d", itemsBefore, n)
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM import_source_links`); n != 0 {
		t.Fatal("preview wrote links")
	}

	c := decodeAPIImport(t, postMultipart(t, mux, "/api/data/import", "export.csv", fixtureExport, fields("commit"), nil))
	if c.Counts.New != 2 {
		t.Fatalf("commit %+v", c)
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM import_source_links WHERE source = ?`, importLinkSource(fileRowsPluginID, "export")); n != 2 {
		t.Fatalf("links = %d, want 2 under the per-entry source", n)
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items i JOIN import_source_links l ON l.local_id = i.id WHERE l.external_id = 'r1' AND i.name = 'Tea' AND i.base_price = 250`); n != 1 {
		t.Fatal("row r1 not applied as Tea at 250")
	}
	again := decodeAPIImport(t, postMultipart(t, mux, "/api/data/import", "export.csv", fixtureExport, fields("commit"), nil))
	if again.Counts.New != 0 || again.Counts.Updated != 0 || again.Counts.Unchanged != 2 {
		t.Fatalf("re-run %+v, want all unchanged", again)
	}
	var payload string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'import_sync' ORDER BY created_at DESC, rowid DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{fileRowsPluginID, `"file_sha256":"`, `"kind":"file"`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("audit payload %s lacks %s", payload, want)
		}
	}
	if strings.Contains(payload, "Scone") || strings.Contains(payload, "export.csv") {
		t.Fatalf("audit payload carries file contents or name: %s", payload)
	}
}

// The plugin runs sandboxed: with every permission granted, settings,
// storage and the network are still denied to it.
func TestFileRowsImport_PluginIsSandboxed(t *testing.T) {
	mux, dp := newFileRowsTestMux(t)
	for _, p := range []string{"storage", "net:probe.invalid", "events:publish", "device-info", "blob:own"} {
		if _, err := dp.Db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, ?, 1)`, "fr-"+p, fileRowsPluginID, p); err != nil {
			t.Fatal(err)
		}
	}
	res := decodeAPIImport(t, postMultipart(t, mux, "/api/data/import", "export.csv", fixtureExport+"x,PROBE,0\n",
		map[string]string{"entry_key": "export", "mode": "preview"}, nil))
	for _, fn := range []string{"settings_get", "storage_get", "http_request", "event_publish", "device_timezone_get", "blob_list"} {
		if !strings.Contains(res.Message, fn+"=-2") {
			t.Errorf("%s not denied: %s", fn, res.Message)
		}
	}
}

// mode is required; the file is capped at ImportFileRowsMaxBytes; the
// ADR-0161 row caps refuse the whole run; a legacy file entry is unchanged.
func TestFileRowsImport_JSONAPI_Bounds(t *testing.T) {
	mux, dp := newFileRowsTestMux(t)
	for _, mode := range []string{"", "apply"} {
		rec := postMultipart(t, mux, "/api/data/import", "export.csv", fixtureExport, map[string]string{"entry_key": "export", "mode": mode}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("mode %q: %d %s", mode, rec.Code, rec.Body.String())
		}
	}
	orig := fileRowsMaxBytes
	fileRowsMaxBytes = int64(len(fixtureExport)) - 1
	rec := postMultipart(t, mux, "/api/data/import", "export.csv", fixtureExport, map[string]string{"entry_key": "export", "mode": "preview"}, nil)
	fileRowsMaxBytes = orig
	if rec.Code != http.StatusBadRequest || !strings.Contains(exportErrorEnvelope(t, rec), "larger than") {
		t.Fatalf("over the file cap: %d %s", rec.Code, rec.Body.String())
	}
	itemsBefore := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`)
	rec = postMultipart(t, mux, "/api/data/import", "export.csv", fixtureExport+"x,GEN:20001,0\n", map[string]string{"entry_key": "export", "mode": "commit"}, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(exportErrorEnvelope(t, rec), "more rows than an import run allows") {
		t.Fatalf("over the row cap: %d %s", rec.Code, rec.Body.String())
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`); n != itemsBefore {
		t.Fatalf("items %d -> %d after a refused run", itemsBefore, n)
	}
}

var fileRowsStagedIDPattern = regexp.MustCompile(`"staged_id":"([0-9a-f]{32})"`)

// The import page: an uploaded file the plugin claims (extension + header
// row) gets the plugin's preview — nothing written — and Import commits
// the staged copy through the plugin. The staged copy is single-use.
func TestImportPage_ClaimedFileUsesPlugin(t *testing.T) {
	mux, dp := newFileRowsTestMux(t)
	itemsBefore := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`)

	rec := postMultipart(t, mux, "/api/import", "Export.CSV", fixtureExport, nil, nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `data-import-file-plugin="`+fileRowsPluginID+`"`) {
		t.Fatalf("claimed file: %d %s", rec.Code, body)
	}
	if !strings.Contains(body, `data-count="new">2<`) || !strings.Contains(body, "Fixture POS") {
		t.Fatalf("plugin preview lacks counts or the plugin name: %s", body)
	}
	if !strings.Contains(body, `data-import-use-standard="1"`) {
		t.Fatalf("no way back to the standard importer: %s", body)
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`); n != itemsBefore {
		t.Fatalf("preview wrote items")
	}
	m := fileRowsStagedIDPattern.FindStringSubmatch(strings.ReplaceAll(body, "&#34;", `"`))
	if m == nil {
		t.Fatalf("no staged_id on the Import button: %s", body)
	}
	commit := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/import/from-file", strings.NewReader(url.Values{
			"staged_id": {m[1]}, "plugin_id": {fileRowsPluginID}, "entry_key": {"export"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	rec = commit()
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-import-system-result="commit"`) {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body.String())
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`); n != itemsBefore+2 {
		t.Fatalf("items after commit = %d, want %d", n, itemsBefore+2)
	}
	rec = commit()
	if rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "data-import-system-error") {
		t.Fatalf("second commit of a consumed copy: %d %s", rec.Code, rec.Body.String())
	}
}

// Files the plugin does not claim keep the standard importer: another
// header, another extension, or the operator's own choice (importer=core).
func TestImportPage_UnclaimedFileUsesStandardImporter(t *testing.T) {
	mux, _ := newFileRowsTestMux(t)
	for name, c := range map[string]struct {
		file, content string
		fields        map[string]string
	}{
		"other header":    {"catalog.csv", importCSV, nil},
		"other extension": {"export.txt", fixtureExport, nil},
		"operator choice": {"export.csv", fixtureExport, map[string]string{"importer": "core"}},
	} {
		rec := postMultipart(t, mux, "/api/import", c.file, c.content, c.fields, nil)
		if strings.Contains(rec.Body.String(), "data-import-file-plugin") {
			t.Errorf("%s: the plugin was used: %s", name, rec.Body.String())
		}
	}
}

// The commit endpoint keeps the import page's gate: a cashier gets 403.
func TestImportPage_FromFileCashierForbidden(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp := newDataAPITestDeps(t)
	registerImport(mux, dp)
	req := httptest.NewRequest(http.MethodPost, "/api/import/from-file", strings.NewReader("staged_id=x&plugin_id=p&entry_key=e"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cashierID, err := data.NewAuthRepo(dp.Db).CreateUser(t.Context(), "file-import-cashier", "Cashier", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	req = auth.WithUser(req, auth.User{ID: cashierID, Role: "cashier"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier: %d %s", rec.Code, rec.Body.String())
	}
}

// A staged copy belongs to the importer that staged it: the standard
// importer's copy never commits through a plugin, and a plugin preview's
// copy never commits through the standard importer.
func TestImportPage_StagedCopyBoundToItsImporter(t *testing.T) {
	mux, dp := newFileRowsTestMux(t)
	itemsBefore := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`)

	// A file both importers read: the plugin's columns and a name/price.
	const dual = "fixture_ref,fixture_title,fixture_price_minor,name,price\nr1,Tea,250,Tea,2.50\n"
	rec := postMultipart(t, mux, "/api/import", "export.csv", dual, map[string]string{"importer": "core"}, nil)
	sm := stagedIDPattern.FindStringSubmatch(rec.Body.String())
	if sm == nil {
		t.Fatalf("standard preview staged nothing: %d %s", rec.Code, rec.Body.String())
	}
	stdID := sm[1]
	req := httptest.NewRequest(http.MethodPost, "/api/import/from-file", strings.NewReader(url.Values{
		"staged_id": {stdID}, "plugin_id": {fileRowsPluginID}, "entry_key": {"export"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "data-import-system-error") {
		t.Fatalf("standard copy through the plugin: %d %s", rec.Code, rec.Body.String())
	}

	body := postMultipart(t, mux, "/api/import", "export.csv", dual, nil, nil).Body.String()
	m := fileRowsStagedIDPattern.FindStringSubmatch(strings.ReplaceAll(body, "&#34;", `"`))
	if m == nil {
		t.Fatalf("no plugin staged id: %s", body)
	}
	rec = postMultipart(t, mux, "/api/import", "export.csv", dual, map[string]string{"commit": "1", "staged_id": m[1], "importer": "core"}, nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("plugin copy through the standard importer: %d %s", rec.Code, rec.Body.String())
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`); n != itemsBefore {
		t.Fatalf("items %d -> %d", itemsBefore, n)
	}
}

// A claimed file the plugin cannot read still offers the standard importer.
func TestImportPage_FailedPluginPreviewOffersStandardImporter(t *testing.T) {
	mux, _ := newFileRowsTestMux(t)
	// Claimed (the header holds both names) but the guest declines: its
	// first column must be fixture_ref.
	declined := "fixture_title,fixture_ref,fixture_price_minor\nTea,r1,250\n"
	rec := postMultipart(t, mux, "/api/import", "export.csv", declined, nil, nil)
	body := rec.Body.String()
	if !strings.Contains(body, "data-import-system-error") || !strings.Contains(body, `data-import-use-standard="1"`) {
		t.Fatalf("failed plugin preview: %s", body)
	}
}

// pluginStagedID is the staged id on a plugin preview's Import button.
func pluginStagedID(t *testing.T, body string) string {
	t.Helper()
	m := fileRowsStagedIDPattern.FindStringSubmatch(strings.ReplaceAll(body, "&#34;", `"`))
	if m == nil {
		t.Fatalf("no staged_id on the plugin preview: %s", body)
	}
	return m[1]
}

// postFromFile posts the plugin preview's Import for stagedID as user.
func postFromFile(t *testing.T, mux *http.ServeMux, stagedID string, user *auth.User) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/import/from-file", strings.NewReader(url.Values{
		"staged_id": {stagedID}, "plugin_id": {fileRowsPluginID}, "entry_key": {"export"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// A plugin preview's staged copy is its operator's: another operator's
// Import is refused and leaves the copy for the operator who previewed.
func TestImportPage_StagedCopyBoundToItsOperator(t *testing.T) {
	mux, dp := newFileRowsTestMux(t)
	itemsBefore := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`)
	alice, bob := &auth.User{ID: "op-alice", Role: "manager"}, &auth.User{ID: "op-bob", Role: "manager"}
	id := pluginStagedID(t, postMultipart(t, mux, "/api/import", "export.csv", fixtureExport, nil, alice).Body.String())

	if rec := postFromFile(t, mux, id, bob); rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "data-import-system-error") {
		t.Fatalf("another operator committed the copy: %d %s", rec.Code, rec.Body.String())
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`); n != itemsBefore {
		t.Fatalf("items %d -> %d after a refused commit", itemsBefore, n)
	}
	if rec := postFromFile(t, mux, id, alice); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-import-system-result="commit"`) {
		t.Fatalf("the previewing operator's commit: %d %s", rec.Code, rec.Body.String())
	}
}

// A double-tap: the second Import, refused while the first holds the
// entry's commit lock (409), does not consume the staged copy — the
// operator's retry still commits it.
func TestImportPage_FromFileBusyKeepsStagedCopy(t *testing.T) {
	mux, dp := newFileRowsTestMux(t)
	itemsBefore := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`)
	id := pluginStagedID(t, postMultipart(t, mux, "/api/import", "export.csv", fixtureExport, nil, nil).Body.String())

	key := apiImportCommitKey(fileRowsPluginID, "export")
	if !reserveImportCommit(key) {
		t.Fatal("commit lock already held")
	}
	rec := postFromFile(t, mux, id, nil)
	releaseImportCommit(key)
	if rec.Code != http.StatusConflict {
		t.Fatalf("commit while another runs: %d %s, want 409", rec.Code, rec.Body.String())
	}
	if rec := postFromFile(t, mux, id, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-import-system-result="commit"`) {
		t.Fatalf("retry after the 409: %d %s", rec.Code, rec.Body.String())
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`); n != itemsBefore+2 {
		t.Fatalf("items = %d, want %d", n, itemsBefore+2)
	}
}

// An upload over the plugin file cap is never offered to a plugin (its
// header row is not read): the standard importer takes it.
func TestImportPage_OverCapFileNotClaimed(t *testing.T) {
	mux, _ := newFileRowsTestMux(t)
	orig := fileRowsMaxBytes
	fileRowsMaxBytes = int64(len(fixtureExport)) - 1
	t.Cleanup(func() { fileRowsMaxBytes = orig })
	rec := postMultipart(t, mux, "/api/import", "export.csv", fixtureExport, nil, nil)
	if strings.Contains(rec.Body.String(), "data-import-file-plugin") {
		t.Fatalf("an over-cap file went to the plugin: %d %s", rec.Code, rec.Body.String())
	}
}

// A workbook is read as one only when named as one: a ZIP named .csv
// carrying the plugin's header row is not claimed.
func TestImportPage_ZipNamedCSVNotClaimed(t *testing.T) {
	mux, _ := newFileRowsTestMux(t)
	raw := buildXLSXForPagesTest(t, [][]string{{"fixture_ref", "fixture_title", "fixture_price_minor"}, {"r1", "Tea", "250"}})
	rec := postMultipart(t, mux, "/api/import", "export.csv", string(raw), nil, nil)
	if strings.Contains(rec.Body.String(), "data-import-file-plugin") {
		t.Fatalf("a ZIP named .csv was read as a workbook and claimed: %s", rec.Body.String())
	}
}

// Two installed plugins claiming one file: the till never picks one
// silently — the standard importer takes the file, and either plugin can
// still be chosen through the data API (entry_key).
func TestImportPage_AmbiguousClaimUsesStandardImporter(t *testing.T) {
	mux, dp := newFileRowsTestMux(t)
	const other = "com.test.filerows2"
	cfg, _ := json.Marshal(map[string]any{"entities": []string{"items"}, "source": "file",
		"file_formats": []string{".csv"}, "file_headers": []string{"fixture_ref", "fixture_title"}})
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, min_pos_version, api_version, published_at) VALUES (?, '1.0.0', 'Other POS', 'desc', 'wasm', './plugin.wasm', 'url', 'sha', 'auth', 'site', '[]', '0.0.0', '1', datetime('now'))`, []any{other}},
		{`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES (?, 'Other POS', '1.0.0', './plugin.wasm', 'wasm', 1)`, []any{other}},
		{`INSERT INTO plugin_entries (id, plugin_id, key, label, type, is_active, sort_order, config_json) VALUES (?, ?, 'export', 'Other export', 'import', 1, 0, ?)`, []any{other + "-e", other, string(cfg)}},
	} {
		if _, err := dp.Db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	rec := postMultipart(t, mux, "/api/import", "export.csv", fixtureExport, nil, nil)
	if strings.Contains(rec.Body.String(), "data-import-file-plugin") {
		t.Fatalf("one of two claiming plugins was picked: %s", rec.Body.String())
	}
}

// ut-docs#4472: a file-rows entry's ask carries the operator's UI language.
func TestFileRowsImport_PayloadCarriesLocale(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	registerImport(mux, dp)
	seedFileRowsPlugin(t, dp.Db, []string{"items"})
	captured := subscribeImportAsk(t, dp.Db, fileRowsPluginID, json.RawMessage(`{"ok":true}`))

	rec := postMultipart(t, mux, "/api/data/import?lang=tr-TR", "export.csv", fixtureExport,
		map[string]string{"entry_key": "export", "mode": "preview"}, nil)
	decodeAPIImport(t, rec)
	if !strings.Contains(string(captured.Payload), `"locale":"tr"`) {
		t.Fatalf("payload lacks locale tr: %s", captured.Payload)
	}
}
