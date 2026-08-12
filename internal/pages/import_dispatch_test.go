package pages

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
)

// seedImportPlugin installs an active plugin with one 'import'-type entry
// declaring entities (persisted in config_json, the same shape
// plugins.PersistManifest writes), an active "import.requested.ask" hook,
// events:receive granted, and "<entity>:write" granted for each entity in
// grantWrite — mirrors seedExportPlugin/seedExportPluginWithPermissions
// (export_dispatch_test.go).
func seedImportPlugin(t *testing.T, db *sql.DB, pluginID, key, label string, entities []string, grantWrite []string) {
	t.Helper()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	cfg, _ := json.Marshal(map[string]any{"entities": entities, "file_formats": []string{".bkp", ".csv"}})
	if len(entities) == 0 {
		cfg = nil
	}
	mustExec(`INSERT INTO plugins (id, name, version, is_active) VALUES (?, ?, '1.0.0', 1)`, pluginID, pluginID)
	mustExec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, is_active, sort_order, config_json)
	          VALUES (?, ?, ?, ?, 'import', 1, 0, ?)`, pluginID+"-e", pluginID, key, label, string(cfg))
	mustExec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, 'events:receive', 1)`, pluginID+"-p", pluginID)
	mustExec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?, ?, 'import.requested.ask', 'import', 1)`, pluginID+"-h", pluginID)
	for _, e := range grantWrite {
		mustExec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, ?, 1)`,
			pluginID+"-p-"+e, pluginID, e+":write")
	}
}

// subscribeImportAsk registers a fake in-process handler answering
// "import.requested.ask" with resp, capturing the event it received —
// mirrors subscribeExportAsk. Returns a pointer the test reads after the
// request.
func subscribeImportAsk(t *testing.T, db *sql.DB, pluginID string, resp json.RawMessage) *plugins.Event {
	t.Helper()
	captured := &plugins.Event{}
	bus := plugins.SharedBus(db)
	bus.ResetSubscribers()
	bus.SetEventMode("import.requested.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(t.Context(), pluginID, []string{"import.requested.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			*captured = ev
			return resp, nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return captured
}

// postImportUpload builds a multipart POST /api/data/import with content in
// the "file" field (same field name as the existing /api/import) plus any
// extra form fields, and serves it.
func postImportUpload(t *testing.T, mux *http.ServeMux, content []byte, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "upload.bkp")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/data/import", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// globImportTempFiles lists the staged-upload temp files currently in the
// OS temp dir — the leak detector for the handler's always-close cleanup.
func globImportTempFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "ut-import-*"))
	if err != nil {
		t.Fatalf("glob temp dir: %v", err)
	}
	return matches
}

func TestImportDispatch_NoImporterInstalled(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newDataAPITestDeps(t)

	rec := postImportUpload(t, mux, []byte("data"), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if msg := exportErrorEnvelope(t, rec); msg != "no import plugin installed" {
		t.Fatalf("unexpected message: %q", msg)
	}
}

func TestImportDispatch_AmbiguousEntryKey(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedImportPlugin(t, dp.Db, "com.t.imp1", "bkp", "BKP Import", []string{"items"}, []string{"items"})
	seedImportPlugin(t, dp.Db, "com.t.imp2", "csv", "CSV Import", []string{"items"}, []string{"items"})

	rec := postImportUpload(t, mux, []byte("data"), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if msg := exportErrorEnvelope(t, rec); msg != "multiple import entries installed — specify entry_key" {
		t.Fatalf("unexpected message: %q", msg)
	}
}

func TestImportDispatch_UnknownEntryKey(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedImportPlugin(t, dp.Db, "com.t.imp1", "bkp", "BKP Import", []string{"items"}, []string{"items"})

	rec := postImportUpload(t, mux, []byte("data"), map[string]string{"entry_key": "does-not-exist"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if msg := exportErrorEnvelope(t, rec); msg != "no installed import entry with that key" {
		t.Fatalf("unexpected message: %q", msg)
	}
}

// TestImportDispatch_ExplicitEntryKeyTargetsThatEntry: with two import
// entries installed, entry_key must resolve to exactly the named one —
// and, per the AskPlugin rule inherited from export (ut-docs#189), only
// that entry's OWNING plugin may answer.
func TestImportDispatch_ExplicitEntryKeyTargetsThatEntry(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedImportPlugin(t, dp.Db, "com.t.right", "bkp", "BKP Import", []string{"items"}, []string{"items"})
	seedImportPlugin(t, dp.Db, "com.t.wrong", "csv", "CSV Import", []string{"items"}, []string{"items"})

	// Only the WRONG plugin has a live subscriber: targeting com.t.right's
	// entry must NOT silently succeed with com.t.wrong's answer.
	subscribeImportAsk(t, dp.Db, "com.t.wrong", json.RawMessage(`{"ok":true,"message":"answered by wrong plugin"}`))

	rec := postImportUpload(t, mux, []byte("data"), map[string]string{"entry_key": "bkp"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (right plugin has no subscriber), got %d: %s", rec.Code, rec.Body.String())
	}
	if msg := exportErrorEnvelope(t, rec); msg != "import plugin did not respond" {
		t.Fatalf("leaked the wrong plugin's answer instead of failing: %q", msg)
	}
}

// TestImportDispatch_PayloadCarriesHandleNeverBytes is the structural half
// of the ADR-0001 amendment: the real handler's real
// importRequestPayload, as marshaled onto the event bus, must carry a
// file_handle (plus name/size/entities) and must NOT contain the file's
// content in any form — the bytes travel exclusively through the
// import_file_* host functions (proven by
// internal/plugins/import_wasm_dispatch_test.go against a real module).
func TestImportDispatch_PayloadCarriesHandleNeverBytes(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedImportPlugin(t, dp.Db, "com.t.imp1", "bkp", "BKP Import", []string{"items", "customers"}, []string{"items", "customers"})
	captured := subscribeImportAsk(t, dp.Db, "com.t.imp1", json.RawMessage(`{"ok":true,"message":"parsed","counts":{"items":42}}`))

	// A distinctive marker that could not appear in the payload by accident.
	content := []byte("CONTENT-MARKER-7f3a9c" + strings.Repeat("x", 4096))
	before := globImportTempFiles(t)
	rec := postImportUpload(t, mux, content, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// The fake handler (unlike a real guest) never calls import_file_close
	// — the handler's own deferred cleanup must still have removed the
	// staged temp file, so no ut-import-* file may outlive the request.
	if after := globImportTempFiles(t); len(after) != len(before) {
		t.Fatalf("staged temp file leaked: before=%v after=%v", before, after)
	}

	var payload struct {
		EntryKey   string          `json:"entry_key"`
		Entities   []string        `json:"entities"`
		FileHandle *int32          `json:"file_handle"`
		FileName   string          `json:"file_name"`
		FileSize   int64           `json:"file_size"`
		Raw        json.RawMessage `json:"-"`
	}
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("parse captured payload %s: %v", captured.Payload, err)
	}
	if payload.FileHandle == nil {
		t.Fatalf("payload has no file_handle field: %s", captured.Payload)
	}
	if payload.EntryKey != "bkp" || payload.FileName != "upload.bkp" || payload.FileSize != int64(len(content)) {
		t.Fatalf("unexpected payload: %s", captured.Payload)
	}
	if len(payload.Entities) != 2 || payload.Entities[0] != "items" || payload.Entities[1] != "customers" {
		t.Fatalf("expected all declared+granted entities by default, got %+v", payload.Entities)
	}
	// Structural: no trace of the file content in the event envelope.
	if bytes.Contains(captured.Payload, []byte("CONTENT-MARKER")) {
		t.Fatalf("file content leaked into the event payload: %s", captured.Payload)
	}
	if len(captured.Payload) > 512 {
		t.Fatalf("payload is %d bytes for a %d-byte upload — content is being marshaled in", len(captured.Payload), len(content))
	}

	// Success envelope carries the plugin's counts through.
	body := dataAPIJSONBody(t, rec)
	data, _ := body["data"].(map[string]any)
	if data == nil || data["message"] != "parsed" {
		t.Fatalf("unexpected body: %+v", body)
	}
	counts, _ := data["counts"].(map[string]any)
	if counts == nil || counts["items"] != float64(42) {
		t.Fatalf("counts not passed through: %+v", body)
	}
}

// TestImportDispatch_EntitiesFilteredByGrant: requested entities are
// narrowed to declared ∩ granted "<entity>:write" — a partial grant omits
// the ungranted entity but still dispatches (mirror of export's
// per-ledger gating, ut-docs#228), and an undeclared requested entity is
// dropped the same way.
func TestImportDispatch_EntitiesFilteredByGrant(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	// Declares items+customers, but only items:write is granted.
	seedImportPlugin(t, dp.Db, "com.t.imp1", "bkp", "BKP Import", []string{"items", "customers"}, []string{"items"})
	captured := subscribeImportAsk(t, dp.Db, "com.t.imp1", json.RawMessage(`{"ok":true,"message":"ok"}`))

	// Request all three: customers (declared, ungranted) and sales
	// (undeclared) must both be silently omitted; items survives.
	rec := postImportUpload(t, mux, []byte("data"), map[string]string{"entities": "items, customers, sales"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (partial grant must not fail the request), got %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Entities []string `json:"entities"`
	}
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("parse captured payload %s: %v", captured.Payload, err)
	}
	if len(payload.Entities) != 1 || payload.Entities[0] != "items" {
		t.Fatalf("expected entities narrowed to [items], got %+v", payload.Entities)
	}
}

// TestImportDispatch_NoGrantedEntitiesRejected: when the narrowing leaves
// NOTHING (nothing requested is both declared and granted), the request is
// invalid — 400, never a dispatch with an empty entity list.
func TestImportDispatch_NoGrantedEntitiesRejected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	// Declared but NOTHING granted.
	seedImportPlugin(t, dp.Db, "com.t.imp1", "bkp", "BKP Import", []string{"items"}, nil)
	// Subscribed, so a missing check would 200 — the status is load-bearing
	// (same red-herring caution as the export tests).
	subscribeImportAsk(t, dp.Db, "com.t.imp1", json.RawMessage(`{"ok":true,"message":"ok"}`))

	rec := postImportUpload(t, mux, []byte("data"), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if msg := exportErrorEnvelope(t, rec); !strings.Contains(msg, "no importable entities") {
		t.Fatalf("unexpected message: %q", msg)
	}
}

// TestImportDispatch_EntryWithNoDeclaredEntitiesRejected: an import entry
// whose manifest declares no entities has nothing the dispatcher could
// ever forward — same 400, before any staging work.
func TestImportDispatch_EntryWithNoDeclaredEntitiesRejected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedImportPlugin(t, dp.Db, "com.t.imp1", "bkp", "BKP Import", nil, []string{"items"})
	subscribeImportAsk(t, dp.Db, "com.t.imp1", json.RawMessage(`{"ok":true,"message":"ok"}`))

	rec := postImportUpload(t, mux, []byte("data"), map[string]string{"entities": "items"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if msg := exportErrorEnvelope(t, rec); !strings.Contains(msg, "no importable entities") {
		t.Fatalf("unexpected message: %q", msg)
	}
}

// TestImportDispatch_SizeCap: an upload past maxImportFileSize is rejected
// with 400. The cap is a var so the test lowers it instead of building a
// >1GB fixture (same rationale as maxExportSalesRows / bkpMaxDBSize).
func TestImportDispatch_SizeCap(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	orig := maxImportFileSize
	maxImportFileSize = 64
	t.Cleanup(func() { maxImportFileSize = orig })

	mux, dp := newDataAPITestDeps(t)
	seedImportPlugin(t, dp.Db, "com.t.imp1", "bkp", "BKP Import", []string{"items"}, []string{"items"})
	// Subscribed so a missing cap would 200 — status stays load-bearing.
	subscribeImportAsk(t, dp.Db, "com.t.imp1", json.RawMessage(`{"ok":true,"message":"ok"}`))

	rec := postImportUpload(t, mux, bytes.Repeat([]byte("x"), 65), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if msg := exportErrorEnvelope(t, rec); !strings.Contains(msg, "exceeds the 64-byte maximum") {
		t.Fatalf("unexpected message: %q", msg)
	}

	// Exactly at the cap still succeeds (inclusive bound, matching the
	// export caps' boundary convention).
	rec = postImportUpload(t, mux, bytes.Repeat([]byte("x"), 64), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 at exactly the cap, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportDispatch_FileFieldRequired(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedImportPlugin(t, dp.Db, "com.t.imp1", "bkp", "BKP Import", []string{"items"}, []string{"items"})

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("entry_key", "bkp")
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/data/import", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if msg := exportErrorEnvelope(t, rec); msg != "file required" {
		t.Fatalf("unexpected message: %q", msg)
	}
}

func TestImportDispatch_PluginError(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedImportPlugin(t, dp.Db, "com.t.imp1", "bkp", "BKP Import", []string{"items"}, []string{"items"})
	subscribeImportAsk(t, dp.Db, "com.t.imp1", json.RawMessage(`{"ok":false,"error":"unrecognised backup format"}`))

	rec := postImportUpload(t, mux, []byte("data"), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if msg := exportErrorEnvelope(t, rec); msg != "unrecognised backup format" {
		t.Fatalf("unexpected message: %q", msg)
	}
}

func TestImportDispatch_NoSubscriberAnswers(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedImportPlugin(t, dp.Db, "com.t.imp1", "bkp", "BKP Import", []string{"items"}, []string{"items"})
	plugins.SharedBus(dp.Db).ResetSubscribers()

	rec := postImportUpload(t, mux, []byte("data"), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if msg := exportErrorEnvelope(t, rec); msg != "import plugin did not respond" {
		t.Fatalf("unexpected message: %q", msg)
	}
}

func TestImportDispatch_RequiresManager(t *testing.T) {
	mux, _ := newDataAPITestDeps(t) // UT_AUTH not off, no signed-in manager
	rec := postImportUpload(t, mux, []byte("data"), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}
