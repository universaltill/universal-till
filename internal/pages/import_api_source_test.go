package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// API-source import entries (ADR-0161, ut-docs#4429): POST
// /api/data/import with no file, mode=preview|commit; the plugin submits
// rows through import_rows_submit and core applies them (internal/catsync).

const apiImportPluginID = "com.test.apiimport"

// seedAPIImportPlugin installs an active plugin with one "source":"api"
// import entry declaring entities, granted grantWrite.
func seedAPIImportPlugin(t *testing.T, db *sql.DB, pluginID string, entities, grantWrite []string) {
	t.Helper()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	cfg, _ := json.Marshal(map[string]any{"entities": entities, "source": "api"})
	mustExec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, min_pos_version, api_version, published_at) VALUES (?, '1.0.0', ?, 'desc', 'wasm', './plugin.wasm', 'url', 'sha', 'auth', 'site', '[]', '0.0.0', '1', datetime('now'))`, pluginID, pluginID)
	mustExec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES (?, ?, '1.0.0', './plugin.wasm', 'wasm', 1)`, pluginID, pluginID)
	mustExec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, is_active, sort_order, config_json)
	          VALUES (?, ?, 'api', 'From another till system', 'import', 1, 0, ?)`, pluginID+"-e", pluginID, string(cfg))
	mustExec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, 'events:receive', 1)`, pluginID+"-p", pluginID)
	mustExec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?, ?, 'import.requested.ask', 'import', 1)`, pluginID+"-h", pluginID)
	for _, e := range grantWrite {
		mustExec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, ?, 1)`, pluginID+"-p-"+e, pluginID, e+":write")
	}
}

func postAPIImport(t *testing.T, mux *http.ServeMux, fields url.Values, user *auth.User) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/data/import", strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type apiImportData struct {
	Message string `json:"message"`
	Mode    string `json:"mode"`
	Counts  struct {
		New, Updated, Unchanged, Issues int
	} `json:"counts"`
	Samples []struct {
		ExternalID string   `json:"external_id"`
		Action     string   `json:"action"`
		Changes    []string `json:"changes"`
		Issue      string   `json:"issue"`
	} `json:"samples"`
}

func decodeAPIImport(t *testing.T, rec *httptest.ResponseRecorder) apiImportData {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data  apiImportData `json:"data"`
		Error any           `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error != nil {
		t.Fatalf("envelope %s: %v", rec.Body.String(), err)
	}
	return env.Data
}

// A no-file request to an api entry reaches the plugin with mode and the
// resolved entities, and never a file handle.
func TestAPIImport_NoFileRequestReachesPlugin(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedAPIImportPlugin(t, dp.Db, apiImportPluginID, []string{"items", "stock"}, []string{"items"})
	captured := subscribeImportAsk(t, dp.Db, apiImportPluginID, json.RawMessage(`{"ok":true}`))

	res := decodeAPIImport(t, postAPIImport(t, mux, url.Values{"mode": {"preview"}}, nil))
	if res.Mode != "preview" || res.Counts.New != 0 {
		t.Fatalf("response %+v", res)
	}
	var p map[string]any
	if err := json.Unmarshal(captured.Payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if p["mode"] != "preview" || p["source"] != "api" {
		t.Fatalf("payload %v lacks mode/source", p)
	}
	if _, has := p["file_handle"]; has {
		t.Fatalf("api payload carries a file handle: %v", p)
	}
	if ents, _ := p["entities"].([]any); len(ents) != 1 || ents[0] != "items" {
		t.Fatalf("entities %v, want only the granted items", p["entities"])
	}
}

func TestAPIImport_BadModeAndFileRejected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedAPIImportPlugin(t, dp.Db, apiImportPluginID, []string{"items"}, []string{"items"})
	subscribeImportAsk(t, dp.Db, apiImportPluginID, json.RawMessage(`{"ok":true}`))

	for _, mode := range []string{"", "apply"} {
		rec := postAPIImport(t, mux, url.Values{"mode": {mode}}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("mode %q: %d %s", mode, rec.Code, rec.Body.String())
		}
	}
	rec := postImportUpload(t, mux, []byte("data"), map[string]string{"mode": "preview"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(exportErrorEnvelope(t, rec), "no file") {
		t.Fatalf("file on an api entry: %d %s", rec.Code, rec.Body.String())
	}
}

// A file entry with no file still answers "file required" — unchanged.
func TestAPIImport_FileEntryStillNeedsFile(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedImportPlugin(t, dp.Db, "com.t.imp1", "bkp", "BKP Import", []string{"items"}, []string{"items"})
	rec := postAPIImport(t, mux, url.Values{"mode": {"preview"}}, nil)
	if rec.Code != http.StatusBadRequest || exportErrorEnvelope(t, rec) != "file required" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAPIImport_CashierForbidden(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp := newDataAPITestDeps(t)
	seedAPIImportPlugin(t, dp.Db, apiImportPluginID, []string{"items"}, []string{"items"})
	asked := false
	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	bus.SetEventMode("import.requested.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(t.Context(), apiImportPluginID, []string{"import.requested.ask"},
		func(context.Context, plugins.Event) (json.RawMessage, error) {
			asked = true
			return json.RawMessage(`{"ok":true}`), nil
		}); err != nil {
		t.Fatal(err)
	}
	cashierID, err := data.NewAuthRepo(dp.Db).CreateUser(t.Context(), "api-import-cashier", "Cashier", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	rec := postAPIImport(t, mux, url.Values{"mode": {"commit"}}, &auth.User{ID: cashierID, Role: "cashier"})
	if rec.Code != http.StatusForbidden || asked {
		t.Fatalf("cashier: %d asked=%v", rec.Code, asked)
	}
}

// An additional till never commits an import: the plugin is not even asked.
func TestAPIImport_CommitRefusedOnAdditionalTill(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedAPIImportPlugin(t, dp.Db, apiImportPluginID, []string{"items"}, []string{"items"})
	captured := subscribeImportAsk(t, dp.Db, apiImportPluginID, json.RawMessage(`{"ok":true}`))
	if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	rec := postAPIImport(t, mux, url.Values{"mode": {"commit"}}, nil)
	if rec.Code != http.StatusConflict || captured.Type != "" {
		t.Fatalf("commit on an additional till: %d asked=%q %s", rec.Code, captured.Type, rec.Body.String())
	}
	// A preview is read-only and still allowed there.
	decodeAPIImport(t, postAPIImport(t, mux, url.Values{"mode": {"preview"}}, nil))
}

// One commit per entry at a time.
func TestAPIImport_ConcurrentCommitRefused(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedAPIImportPlugin(t, dp.Db, apiImportPluginID, []string{"items"}, []string{"items"})
	subscribeImportAsk(t, dp.Db, apiImportPluginID, json.RawMessage(`{"ok":true}`))
	key := apiImportCommitKey(apiImportPluginID, "api")
	if !reserveImportCommit(key) {
		t.Fatal("could not reserve")
	}
	defer releaseImportCommit(key)
	if rec := postAPIImport(t, mux, url.Values{"mode": {"commit"}}, nil); rec.Code != http.StatusConflict {
		t.Fatalf("second commit: %d %s", rec.Code, rec.Body.String())
	}
}

var (
	importRowsGuestOnce sync.Once
	importRowsGuestPath string
	importRowsGuestErr  error
)

// installImportRowsGuest builds testdata/importrows_guest (once per test
// binary) and runs it as apiImportPluginID through the real WasmRuntime.
func installImportRowsGuest(t *testing.T, dp *common.Deps) {
	t.Helper()
	importRowsGuestOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		dir, err := os.MkdirTemp("", "importrows-guest-")
		if err != nil {
			importRowsGuestErr = err
			return
		}
		importRowsGuestPath = filepath.Join(dir, "plugin.wasm")
		cmd := exec.Command("go", "build", "-o", importRowsGuestPath, "../plugins/testdata/importrows_guest")
		cmd.Dir = filepath.Dir(file)
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
		if raw, err := cmd.CombinedOutput(); err != nil {
			importRowsGuestErr = err
			importRowsGuestPath = string(raw)
		}
	})
	if importRowsGuestErr != nil {
		t.Fatalf("build importrows guest: %v\n%s", importRowsGuestErr, importRowsGuestPath)
	}
	raw, err := os.ReadFile(importRowsGuestPath)
	if err != nil {
		t.Fatal(err)
	}
	baseDir := t.TempDir()
	modDir := filepath.Join(baseDir, apiImportPluginID, "1.0.0")
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

func setGuestSetting(t *testing.T, dp *common.Deps, key, value string) {
	t.Helper()
	b, _ := json.Marshal(value)
	if err := data.NewPluginRepo(dp.Db).UpsertPluginSetting(t.Context(), apiImportPluginID, key, string(b)); err != nil {
		t.Fatal(err)
	}
}

func apiImportCount(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// The end-to-end path with a real compiled guest: preview writes nothing,
// commit creates and links, a re-run is a no-op, a changed price updates
// one item and a changed stock records a movement. One audit entry per
// committed run.
func TestAPIImport_EndToEnd_RealGuest(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedAPIImportPlugin(t, dp.Db, apiImportPluginID, []string{"items", "stock"}, []string{"items", "stock"})
	installImportRowsGuest(t, dp)
	rows := func(price, stock string) string {
		return `[{"external_id":"p-1","name":"Flat white","barcodes":["5000000000017"],"price_minor":` + price + `,"category_path":["Drinks"],"vat_rate_bp":2000,"stock_qty":` + stock + `},` +
			`{"external_id":"p-2","name":"Scone","price_minor":250,"category_path":["Bakery"]},` +
			`{"external_id":"bad","price_minor":1}]`
	}
	setGuestSetting(t, dp, "rows", rows("320", "12"))
	itemsBefore := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`)
	auditBefore := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM audit_log WHERE action = 'import_sync'`)

	pre := decodeAPIImport(t, postAPIImport(t, mux, url.Values{"mode": {"preview"}}, nil))
	if pre.Counts.New != 2 || pre.Counts.Issues != 1 || len(pre.Samples) != 3 {
		t.Fatalf("preview %+v", pre)
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`); n != itemsBefore {
		t.Fatalf("preview wrote items: %d -> %d", itemsBefore, n)
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM import_source_links`); n != 0 {
		t.Fatalf("preview wrote links")
	}

	c := decodeAPIImport(t, postAPIImport(t, mux, url.Values{"mode": {"commit"}}, nil))
	if c.Counts.New != 2 || c.Counts.Issues != 1 {
		t.Fatalf("commit %+v", c)
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`); n != itemsBefore+2 {
		t.Fatalf("items after commit = %d, want %d", n, itemsBefore+2)
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM import_source_links WHERE source = ?`, apiImportPluginID); n != 2 {
		t.Fatalf("links = %d, want 2 (source = the plugin id)", n)
	}

	again := decodeAPIImport(t, postAPIImport(t, mux, url.Values{"mode": {"commit"}}, nil))
	if again.Counts.New != 0 || again.Counts.Updated != 0 || again.Counts.Unchanged != 2 {
		t.Fatalf("re-run %+v, want 0 new 0 updated", again)
	}

	moves := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM stock_movements`)
	setGuestSetting(t, dp, "rows", rows("350", "9"))
	upd := decodeAPIImport(t, postAPIImport(t, mux, url.Values{"mode": {"commit"}}, nil))
	if upd.Counts.Updated != 1 || upd.Counts.New != 0 || upd.Counts.Unchanged != 1 {
		t.Fatalf("price change %+v", upd)
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items i JOIN import_source_links l ON l.local_id = i.id WHERE l.external_id = 'p-1' AND i.base_price = 350`); n != 1 {
		t.Fatal("price not updated")
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM stock_movements`); n != moves+1 {
		t.Fatalf("stock movements %d -> %d, want one more", moves, n)
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM audit_log WHERE action = 'import_sync'`); n != auditBefore+3 {
		t.Fatalf("import_sync audit entries = %d, want one per committed run (3)", n-auditBefore)
	}
	var payload string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'import_sync' ORDER BY created_at DESC, rowid DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{apiImportPluginID, `"updated":1`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("audit payload %s lacks %s", payload, want)
		}
	}
	if strings.Contains(payload, "Flat white") {
		t.Fatalf("audit payload carries row contents: %s", payload)
	}
}

// A plugin that declines after submitting gets nothing applied; a run over
// a cap is refused whole.
func TestAPIImport_DeclineOrCapAppliesNothing(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedAPIImportPlugin(t, dp.Db, apiImportPluginID, []string{"items"}, []string{"items"})
	installImportRowsGuest(t, dp)
	itemsBefore := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`)

	setGuestSetting(t, dp, "gen", "3")
	setGuestSetting(t, dp, "answer", "decline")
	if rec := postAPIImport(t, mux, url.Values{"mode": {"commit"}}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("declined run: %d %s", rec.Code, rec.Body.String())
	}
	setGuestSetting(t, dp, "answer", "")
	setGuestSetting(t, dp, "batch", "501")
	setGuestSetting(t, dp, "gen", "501")
	// The guest ignores the failed submit and answers ok: core still
	// refuses the run.
	setGuestSetting(t, dp, "ignore_errors", "1")
	rec := postAPIImport(t, mux, url.Values{"mode": {"commit"}}, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(exportErrorEnvelope(t, rec), "more rows than an import run allows") {
		t.Fatalf("over-cap run: %d %s", rec.Code, rec.Body.String())
	}
	if n := apiImportCount(t, dp.Db, `SELECT COUNT(*) FROM items`); n != itemsBefore {
		t.Fatalf("items %d -> %d after a refused run", itemsBefore, n)
	}
}

// ut-docs#4485: the JSON API keeps the English issue text and adds the
// stable code (and args) so a client can translate it itself.
func TestAPIImport_SamplesCarryIssueCodeAndEnglish(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedAPIImportPlugin(t, dp.Db, apiImportPluginID, []string{"items"}, []string{"items"})
	installImportRowsGuest(t, dp)
	setGuestSetting(t, dp, "rows", `[{"external_id":"bad","price_minor":1},`+
		`{"external_id":"deep","name":"D","price_minor":1,"category_path":["a","b","c","d","e"]}]`)

	rec := postAPIImport(t, mux, url.Values{"mode": {"preview"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Samples []struct {
				Issue     string   `json:"issue"`
				IssueCode string   `json:"issue_code"`
				IssueArgs []string `json:"issue_args"`
			} `json:"samples"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || len(env.Data.Samples) != 2 {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	a, b := env.Data.Samples[0], env.Data.Samples[1]
	if a.Issue != "name is missing or invalid" || a.IssueCode != "name_invalid" || len(a.IssueArgs) != 0 {
		t.Errorf("sample 1: %+v", a)
	}
	if b.Issue != "category_path is deeper than 4 levels" || b.IssueCode != "category_too_deep" || len(b.IssueArgs) != 1 || b.IssueArgs[0] != "4" {
		t.Errorf("sample 2: %+v", b)
	}
}
