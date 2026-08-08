package pages

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
// and events:receive/sales:read/inventory:read all granted (required before
// Ask will invoke a handler — see EventBus.Ask's CheckPermission call — and
// before the export.requested.ask payload will carry sales/stock data —
// see ut-docs#228). Most tests in this file want the full happy path; use
// seedExportPluginWithPermissions directly for the permission-gating tests.
func seedExportPlugin(t *testing.T, db *sql.DB, pluginID, key, label string) {
	t.Helper()
	seedExportPluginWithPermissions(t, db, pluginID, key, label, true, true)
}

func seedExportPluginWithPermissions(t *testing.T, db *sql.DB, pluginID, key, label string, salesRead, inventoryRead bool) {
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
	if salesRead {
		mustExec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, 'sales:read', 1)`, pluginID+"-p-sales", pluginID)
	}
	if inventoryRead {
		mustExec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, 'inventory:read', 1)`, pluginID+"-p-inv", pluginID)
	}
}

// exportErrorEnvelope decodes /api/data/export's failure envelope --
// { "data": null, "error": "…" } (universal-till/CLAUDE.md, ut-docs#387) --
// and returns the error string, failing the test if data isn't null.
func exportErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Data  any    `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error envelope: %v (%s)", err, rec.Body.String())
	}
	if out.Data != nil {
		t.Fatalf("expected data:null on error, got %v", out.Data)
	}
	return out.Error
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
	errMsg := exportErrorEnvelope(t, rec)
	if errMsg != "from must not be after to" {
		t.Fatalf("unexpected message: %q", errMsg)
	}
}

// TestExportDispatch_DateRangeTooLarge is the ut-docs#229 regression: with
// no cap, a year-plus-long range would attempt SalesForExport's full
// data-gathering step (however cheap the query shape is made) with no
// upper bound at all — on till-class hardware, an unbounded request is
// itself the risk, independent of how efficient the query becomes. The
// host rejects an over-long range before ever calling SalesForExport,
// mirroring TestExportDispatch_FromAfterTo's shape.
func TestExportDispatch_DateRangeTooLarge(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.exp1", "csv", "CSV Export")
	// Independent review finding (2026-08-08): without a subscriber, an
	// unrelated failure ("export plugin did not respond") also 400s, so
	// the status-code check alone can't tell the cap from a red herring —
	// subscribe so a missing cap would 200, making the status check
	// genuinely load-bearing rather than just the exact message text.
	subscribeExportAsk(t, dp.Db, "com.t.exp1", json.RawMessage(`{"ok":true,"message":"accepted"}`))

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2024-01-01"}, "to": {"2026-06-01"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	errMsg := exportErrorEnvelope(t, rec)
	if errMsg != "date range exceeds the 366-day maximum" {
		t.Fatalf("unexpected message: %q", errMsg)
	}
}

// TestExportDispatch_DateRangeAtCap proves the cap is inclusive — exactly
// maxExportRangeDays apart must still succeed, matching the "off-by-one"
// caution most range-boundary code in this repo already takes (e.g.
// SalesForExport's own inclusive-of-final-day handling).
func TestExportDispatch_DateRangeAtCap(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.exp1", "csv", "CSV Export")
	subscribeExportAsk(t, dp.Db, "com.t.exp1", json.RawMessage(`{"ok":true,"message":"accepted"}`))

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2025-01-01"}, "to": {"2026-01-02"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 at exactly the cap (366 days), got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestExportDispatch_RowCountTooLarge is the ut-docs#439 regression: the
// 366-day range cap alone only bounds elapsed time, not row count -- a
// matched-sale count over maxExportSalesRows must be rejected before
// SalesForExport's batch gather (and the plugin dispatch after it) ever
// runs, the same "reject before doing the expensive work" shape
// TestExportDispatch_DateRangeTooLarge already proves for the range cap.
// Overrides the package var rather than seeding 50,000 real rows.
func TestExportDispatch_RowCountTooLarge(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	orig := maxExportSalesRows
	maxExportSalesRows = 2
	t.Cleanup(func() { maxExportSalesRows = orig })

	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.exp1", "csv", "CSV Export")
	// Subscribed so a missing/broken cap would 200, not just 400 for an
	// unrelated reason (same red-herring caution as the date-range test).
	subscribeExportAsk(t, dp.Db, "com.t.exp1", json.RawMessage(`{"ok":true,"message":"accepted"}`))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dp.Db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	for i, r := range []string{"R1", "R2", "R3"} {
		mustExec(`INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
		          VALUES(?, ?, 'completed', 'sale', 'GBP', 1000, 0, 0, 1000, ?)`,
			fmt.Sprintf("rc-sale-%d", i), r, fmt.Sprintf("2026-01-%02dT10:00:00Z", i+1))
	}

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	errMsg := exportErrorEnvelope(t, rec)
	if errMsg != "matched sale count (3) exceeds the 2-row maximum — narrow the date range" {
		t.Fatalf("unexpected message: %q", errMsg)
	}
}

// TestExportDispatch_RowCountAtCap proves the bound is inclusive -- exactly
// maxExportSalesRows matched sales must still succeed, matching the
// boundary-inclusive convention TestExportDispatch_DateRangeAtCap already
// establishes for the range cap.
func TestExportDispatch_RowCountAtCap(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	orig := maxExportSalesRows
	maxExportSalesRows = 2
	t.Cleanup(func() { maxExportSalesRows = orig })

	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.exp1", "csv", "CSV Export")
	subscribeExportAsk(t, dp.Db, "com.t.exp1", json.RawMessage(`{"ok":true,"message":"accepted"}`))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dp.Db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	for i, r := range []string{"R1", "R2"} {
		mustExec(`INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
		          VALUES(?, ?, 'completed', 'sale', 'GBP', 1000, 0, 0, 1000, ?)`,
			fmt.Sprintf("ac-sale-%d", i), r, fmt.Sprintf("2026-01-%02dT10:00:00Z", i+1))
	}

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 at exactly the cap (2 sales), got %d: %s", rec.Code, rec.Body.String())
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
	errMsg := exportErrorEnvelope(t, rec)
	if errMsg != "no export plugin installed" {
		t.Fatalf("unexpected message: %q", errMsg)
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
	errMsg := exportErrorEnvelope(t, rec)
	if errMsg != "multiple export entries installed — specify entry_key" {
		t.Fatalf("unexpected message: %q", errMsg)
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
	errMsg := exportErrorEnvelope(t, rec)
	if errMsg != "no installed export entry with that key" {
		t.Fatalf("unexpected message: %q", errMsg)
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
	errMsg := exportErrorEnvelope(t, rec)
	if errMsg != "export plugin did not respond" {
		t.Fatalf("unexpected message: %q", errMsg)
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
	errMsg := exportErrorEnvelope(t, rec)
	if errMsg != "export plugin did not respond" {
		t.Fatalf("leaked the wrong plugin's answer instead of failing: %q", errMsg)
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
	// Envelope: { "data": { "message": "triggered" }, "error": null }
	// (universal-till/CLAUDE.md, ut-docs#387).
	body := dataAPIJSONBody(t, rec)
	if body["error"] != nil {
		t.Fatalf("expected error:null, got %+v", body)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil || data["message"] != "triggered" {
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
	errMsg := exportErrorEnvelope(t, rec)
	if errMsg != "boom" {
		t.Fatalf("unexpected message: %q", errMsg)
	}
}

// TestExportDispatch_PayloadIncludesSalesData is the regression for
// ut-docs#221: export.requested.ask carried only {from,to,entry_key} before
// this change, so a real export/report plugin had no actual sale/tax/
// payment data to build a file from. This asserts the host now gathers the
// real sales in range (via data.POSRepo.SalesForExport) and includes them
// in the payload the plugin receives.
func TestExportDispatch_PayloadIncludesSalesData(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.exp1", "csv", "CSV Export")

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dp.Db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
	          VALUES('sale1','R900','completed','sale','GBP',1000,0,200,1200,'2026-01-15T10:00:00Z')`)
	mustExec(`INSERT INTO sale_lines(id, sale_id, line_no, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
	          VALUES('line1','sale1',1,'Widget',1,1000,2000,200,1000,1200)`)
	mustExec(`INSERT INTO payments(id, sale_id, method_id, amount, currency, paid_at)
	          VALUES('pay1','sale1','cash',1200,'GBP','2026-01-15T10:00:00Z')`)
	// Outside the requested range -- must not appear in the captured payload.
	mustExec(`INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
	          VALUES('sale2','R901','completed','sale','GBP',500,0,0,500,'2026-02-15T10:00:00Z')`)

	var captured plugins.Event
	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	bus.SetEventMode("export.requested.ask", plugins.Blocking)
	answer, _ := json.Marshal(map[string]any{"ok": true, "message": "ok"})
	if _, err := bus.SubscribeWithHandler(t.Context(), "com.t.exp1", []string{"export.requested.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			captured = ev
			return answer, nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Sales []struct {
			ReceiptNo string `json:"receipt_no"`
			Total     int64  `json:"total"`
			TaxLines  []struct {
				RateBP int64 `json:"rate_bp"`
				Net    int64 `json:"net"`
				Tax    int64 `json:"tax"`
			} `json:"tax_lines"`
			Payments []struct {
				Method string `json:"method"`
				Amount int64  `json:"amount"`
			} `json:"payments"`
		} `json:"sales"`
	}
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("parse captured payload %s: %v", captured.Payload, err)
	}
	if len(payload.Sales) != 1 {
		t.Fatalf("expected exactly the in-range sale, got %+v", payload.Sales)
	}
	s := payload.Sales[0]
	if s.ReceiptNo != "R900" || s.Total != 1200 {
		t.Fatalf("unexpected sale row: %+v", s)
	}
	if len(s.TaxLines) != 1 || s.TaxLines[0].RateBP != 2000 || s.TaxLines[0].Net != 1000 || s.TaxLines[0].Tax != 200 {
		t.Fatalf("unexpected tax lines: %+v", s.TaxLines)
	}
	if len(s.Payments) != 1 || s.Payments[0].Method != "cash" || s.Payments[0].Amount != 1200 {
		t.Fatalf("unexpected payments: %+v", s.Payments)
	}
}

// TestExportDispatch_PayloadIncludesStockData is the ut-docs#59 counterpart
// to TestExportDispatch_PayloadIncludesSalesData: the payload must also
// carry current stock levels (data.POSRepo.StockForExport) so a subscribing
// export/report plugin can build a stock-level export, not just a sales one.
func TestExportDispatch_PayloadIncludesStockData(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPlugin(t, dp.Db, "com.t.exp1", "csv", "CSV Export")

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dp.Db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO items(id, sku, name, base_price, reorder_level, is_active) VALUES('itm-stk','SKU-STK','Stock Widget',500,3,1)`)
	mustExec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv-stk','itm-stk',NULL,'loc_main',7.5,datetime('now'))`)

	var captured plugins.Event
	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	bus.SetEventMode("export.requested.ask", plugins.Blocking)
	answer, _ := json.Marshal(map[string]any{"ok": true, "message": "ok"})
	if _, err := bus.SubscribeWithHandler(t.Context(), "com.t.exp1", []string{"export.requested.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			captured = ev
			return answer, nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Stock []struct {
			ItemID       string  `json:"item_id"`
			Name         string  `json:"name"`
			SKU          string  `json:"sku"`
			LocationID   string  `json:"location_id"`
			LocationName string  `json:"location_name"`
			CurrentQty   float64 `json:"current_qty"`
			ReorderLevel int     `json:"reorder_level"`
		} `json:"stock"`
	}
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("parse captured payload %s: %v", captured.Payload, err)
	}
	// seedForPages already seeds one baseline inventory row (itm1 @
	// loc_main, qty 50) alongside the one seeded above -- assert on the
	// row this test cares about rather than the exact count.
	var got *struct {
		ItemID       string  `json:"item_id"`
		Name         string  `json:"name"`
		SKU          string  `json:"sku"`
		LocationID   string  `json:"location_id"`
		LocationName string  `json:"location_name"`
		CurrentQty   float64 `json:"current_qty"`
		ReorderLevel int     `json:"reorder_level"`
	}
	for i := range payload.Stock {
		if payload.Stock[i].ItemID == "itm-stk" {
			got = &payload.Stock[i]
		}
	}
	if got == nil {
		t.Fatalf("expected itm-stk in stock payload, got %+v", payload.Stock)
	}
	if got.Name != "Stock Widget" || got.SKU != "SKU-STK" || got.LocationID != "loc_main" {
		t.Fatalf("unexpected stock row: %+v", got)
	}
	if got.CurrentQty != 7.5 || got.ReorderLevel != 3 {
		t.Fatalf("unexpected qty/reorder: %+v", got)
	}
}

// TestExportDispatch_OmitsSalesWithoutSalesReadPermission is the ut-docs#228
// regression: a plugin that has NOT been granted sales:read must not receive
// the sales ledger, even though it has events:receive (so AskPlugin itself
// still invokes it) and inventory:read (so this isn't just "no permissions
// at all" — the two ledgers must gate independently, per the review finding
// that a flat single check would leak one ledger to a plugin that only
// asked for the other).
func TestExportDispatch_OmitsSalesWithoutSalesReadPermission(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPluginWithPermissions(t, dp.Db, "com.t.exp1", "csv", "CSV Export", false, true)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dp.Db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
	          VALUES('sale1','R900','completed','sale','GBP',1000,0,200,1200,'2026-01-15T10:00:00Z')`)

	var captured plugins.Event
	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	bus.SetEventMode("export.requested.ask", plugins.Blocking)
	answer, _ := json.Marshal(map[string]any{"ok": true, "message": "ok"})
	if _, err := bus.SubscribeWithHandler(t.Context(), "com.t.exp1", []string{"export.requested.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			captured = ev
			return answer, nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (missing a data permission must not fail the whole request), got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Sales json.RawMessage `json:"sales"`
		Stock []struct {
			ItemID string `json:"item_id"`
		} `json:"stock"`
	}
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("parse captured payload %s: %v", captured.Payload, err)
	}
	if payload.Sales != nil && string(payload.Sales) != "null" {
		t.Fatalf("expected sales omitted (null) without sales:read, got %s", payload.Sales)
	}
	// The granted side must still be populated -- proves this is genuine
	// independent per-ledger gating, not an accidental blanket omission.
	// seedForPages seeds one baseline inventory row regardless of this test.
	if len(payload.Stock) == 0 {
		t.Fatalf("expected stock still populated (inventory:read granted), got none")
	}
}

// TestExportDispatch_OmitsBothWithoutEitherPermission is the ut-docs#228
// third case: a plugin granted neither sales:read nor inventory:read (but
// still events:receive, so AskPlugin invokes it at all -- e.g. a plugin like
// ut-plugin-tax-de's dsfinvk-export-de entry that triggers an external
// export with no local ledger data) must still get a real response, not a
// 4xx -- "no data permissions" is not the same thing as "invalid request".
func TestExportDispatch_OmitsBothWithoutEitherPermission(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPluginWithPermissions(t, dp.Db, "com.t.exp1", "fiskaly", "Fiskaly DSFinV-K", false, false)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dp.Db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
	          VALUES('sale1','R900','completed','sale','GBP',1000,0,200,1200,'2026-01-15T10:00:00Z')`)

	var captured plugins.Event
	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	bus.SetEventMode("export.requested.ask", plugins.Blocking)
	answer, _ := json.Marshal(map[string]any{"ok": true, "message": "triggered"})
	if _, err := bus.SubscribeWithHandler(t.Context(), "com.t.exp1", []string{"export.requested.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			captured = ev
			return answer, nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (a plugin needing neither ledger must still be invoked), got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Sales json.RawMessage `json:"sales"`
		Stock json.RawMessage `json:"stock"`
	}
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("parse captured payload %s: %v", captured.Payload, err)
	}
	if payload.Sales != nil && string(payload.Sales) != "null" {
		t.Fatalf("expected sales omitted (null) with neither permission, got %s", payload.Sales)
	}
	if payload.Stock != nil && string(payload.Stock) != "null" {
		t.Fatalf("expected stock omitted (null) with neither permission, got %s", payload.Stock)
	}
}

// TestExportDispatch_OmitsStockWithoutInventoryReadPermission is the
// ut-docs#228 counterpart: a plugin granted sales:read but not
// inventory:read must still get its sales data, but not the stock ledger.
func TestExportDispatch_OmitsStockWithoutInventoryReadPermission(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	seedExportPluginWithPermissions(t, dp.Db, "com.t.exp1", "csv", "CSV Export", true, false)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dp.Db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
	          VALUES('sale1','R900','completed','sale','GBP',1000,0,200,1200,'2026-01-15T10:00:00Z')`)
	mustExec(`INSERT INTO items(id, sku, name, base_price, reorder_level, is_active) VALUES('itm-stk','SKU-STK','Stock Widget',500,3,1)`)
	mustExec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv-stk','itm-stk',NULL,'loc_main',7.5,datetime('now'))`)

	var captured plugins.Event
	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	bus.SetEventMode("export.requested.ask", plugins.Blocking)
	answer, _ := json.Marshal(map[string]any{"ok": true, "message": "ok"})
	if _, err := bus.SubscribeWithHandler(t.Context(), "com.t.exp1", []string{"export.requested.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			captured = ev
			return answer, nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	rec := postForm(mux, "/api/data/export", url.Values{"from": {"2026-01-01"}, "to": {"2026-01-31"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Sales []struct {
			ReceiptNo string `json:"receipt_no"`
		} `json:"sales"`
		Stock json.RawMessage `json:"stock"`
	}
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("parse captured payload %s: %v", captured.Payload, err)
	}
	if len(payload.Sales) != 1 {
		t.Fatalf("expected sales still populated (sales:read granted), got %+v", payload.Sales)
	}
	if payload.Stock != nil && string(payload.Stock) != "null" {
		t.Fatalf("expected stock omitted (null) without inventory:read, got %s", payload.Stock)
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
