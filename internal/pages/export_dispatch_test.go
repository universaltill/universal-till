package pages

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
)

// seedExportPlugin installs an active plugin with one 'export'-type entry
// (key/label), an active "export.requested.ask" hook (required before the
// event bus will accept a subscription for it — see EventBus.subscribe),
// and events:receive granted (required before Ask will invoke a handler —
// see EventBus.Ask's CheckPermission call).
func seedExportPlugin(t *testing.T, db *sql.DB, pluginID, key, label string) {
	t.Helper()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO plugins (id, name, version, is_active) VALUES (?, ?, '1.0.0', 1)`, pluginID, pluginID)
	mustExec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, is_active, sort_order)
	          VALUES (?, ?, ?, ?, 'export', 1, 0)`, pluginID+"-e", pluginID, key, label)
	mustExec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, 'events:receive', 1)`, pluginID+"-p", pluginID)
	mustExec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?, ?, 'export.requested.ask', 'export', 1)`, pluginID+"-h", pluginID)
}

// subscribeExportAsk resets the shared bus's subscribers (the bus is a
// process-wide singleton — see SharedBus's doc comment — so each test that
// needs a fresh, single answering handler for "export.requested.ask" must
// clear any handler left behind by an earlier test in this file) and
// registers a fake in-process handler answering with resp, mirroring
// internal/plugins/ipc_test.go's TestEventBus_Ask.
func subscribeExportAsk(t *testing.T, db *sql.DB, pluginID string, resp json.RawMessage) {
	t.Helper()
	bus := plugins.SharedBus(db)
	bus.ResetSubscribers()
	bus.SetEventMode("export.requested.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(t.Context(), pluginID, []string{"export.requested.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return resp, nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
}

func TestExportDispatch_FromAfterTo(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.exp1", "csv", "CSV Export")

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-06-01"}, "to": {"2026-01-01"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if body["message"] != "from must not be after to" {
		t.Fatalf("unexpected message: %+v", body)
	}
}

func TestExportDispatch_InvalidBase64Content(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.exp1", "csv", "CSV Export")

	answer, _ := json.Marshal(map[string]any{"ok": true, "filename": "x.csv", "content_b64": "not-valid-base64!!"})
	subscribeExportAsk(t, dp.Db, "com.t.exp1", answer)

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestExportDispatch_NoExporterInstalled(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newDataAPITestDeps(t)

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if body["message"] != "no export plugin installed" {
		t.Fatalf("unexpected message: %+v", body)
	}
}

func TestExportDispatch_AmbiguousEntryKey(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.exp1", "csv", "CSV Export")
	seedExportPlugin(t, dp.Db, "com.t.exp2", "zip", "Zip Export")

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if body["message"] != "multiple export entries installed — specify entry_key" {
		t.Fatalf("unexpected message: %+v", body)
	}
}

func TestExportDispatch_UnknownEntryKey(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.exp1", "csv", "CSV Export")

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}, "entry_key": {"does-not-exist"}}, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if body["message"] != "no installed export entry with that key" {
		t.Fatalf("unexpected message: %+v", body)
	}
}

func TestExportDispatch_NoSubscriberAnswers(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.exp1", "csv", "CSV Export")
	// Reset any subscriber a previous test left registered on the shared
	// bus, but do NOT register one here — the plugin has a hook declared
	// but nothing is actually listening.
	plugins.SharedBus(dp.Db).ResetSubscribers()

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if body["message"] != "export plugin did not respond" {
		t.Fatalf("unexpected message: %+v", body)
	}
}

func TestExportDispatch_StreamsFileContent(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.exp1", "csv", "CSV Export")

	want := []byte("id,total\n1,9.99\n")
	answer, _ := json.Marshal(map[string]any{
		"ok":          true,
		"filename":    "sales.csv",
		"content_b64": base64.StdEncoding.EncodeToString(want),
	})
	subscribeExportAsk(t, dp.Db, "com.t.exp1", answer)

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") {
		t.Fatalf("expected an attachment Content-Disposition, got %q", cd)
	}
	if mt, params, err := mime.ParseMediaType(cd); err != nil || mt != "attachment" || params["filename"] != "sales.csv" {
		t.Fatalf("unexpected Content-Disposition: %q (parsed type=%q params=%+v err=%v)", cd, mt, params, err)
	}
	if rec.Body.String() != string(want) {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}
}

// TestExportDispatch_NeverAnswersFromWrongPlugin is the HTTP-level
// regression for a bug an independent review found in ut-docs#189: the
// handler resolves entry_key to a specific owning plugin, but originally
// asked via a broadcast EventBus.Ask, so ANY installed plugin subscribed
// to "export.requested.ask" could answer on the targeted plugin's behalf.
// Here only "com.t.wrong" answers; the request targets "com.t.right" (via
// its own entry_key) which has no subscriber — this must NOT silently
// succeed with com.t.wrong's answer.
func TestExportDispatch_NeverAnswersFromWrongPlugin(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.right", "csv", "CSV Export")
	seedExportPlugin(t, dp.Db, "com.t.wrong", "zip", "Zip Export")

	wrongAnswer, _ := json.Marshal(map[string]any{"ok": true, "message": "answered by wrong plugin"})
	subscribeExportAsk(t, dp.Db, "com.t.wrong", wrongAnswer)

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}, "entry_key": {"csv"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (right plugin has no subscriber), got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if body["message"] != "export plugin did not respond" {
		t.Fatalf("leaked the wrong plugin's answer instead of failing: %+v", body)
	}
}

func TestExportDispatch_MessageOnlyResponse(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.exp1", "fiskaly", "Fiskaly DSFinV-K")

	answer, _ := json.Marshal(map[string]any{"ok": true, "message": "triggered"})
	subscribeExportAsk(t, dp.Db, "com.t.exp1", answer)

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if body["success"] != true || body["message"] != "triggered" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestExportDispatch_PluginError(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.exp1", "csv", "CSV Export")

	answer, _ := json.Marshal(map[string]any{"ok": false, "error": "boom"})
	subscribeExportAsk(t, dp.Db, "com.t.exp1", answer)

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if body["message"] != "boom" {
		t.Fatalf("unexpected message: %+v", body)
	}
}

func TestExportDispatch_RequiresManager(t *testing.T) {
	mux, _ := newDataAPITestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/data/export",
		strings.NewReader(url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}
