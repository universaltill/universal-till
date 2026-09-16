package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till held-sale write-through, primary side (ADR-0093, ut-docs#1920):
// POST /api/sync/held-sales/upsert, POST /api/sync/held-sales/delete and
// GET /api/sync/held-sales are the bearer-authed endpoints a replica's
// heldSaleWriteThrough / heldSaleDeleteWriteThrough / fetchHeldSalesFromPrimary
// (held_sale_sync_proxy.go) hit. Same shape as sync_tables_claim_test.go:
// syncTill auth, JSON envelope, snake_case, a real migrated database.

func newSyncHeldSalesTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "sync_held_sales.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })

	dp := &common.Deps{Db: dbase.DB}
	mux := http.NewServeMux()
	registerSyncHeldSales(mux, dp)
	return mux, dp
}

func postSyncHeldSaleJSON(mux *http.ServeMux, action, body, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/sync/held-sales/"+action, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func getSyncHeldSales(mux *http.ServeMux, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/sync/held-sales", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func upsertBody(t *testing.T, row syncHeldSaleRow) string {
	t.Helper()
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func decodeSyncHeldSaleUpsert(t *testing.T, rec *httptest.ResponseRecorder) syncHeldSaleUpsertResult {
	t.Helper()
	var resp struct {
		Data  *syncHeldSaleUpsertResult `json:"data"`
		Error any                       `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %q)", err, rec.Body.String())
	}
	if resp.Data == nil {
		t.Fatalf("expected a data object, got body %q", rec.Body.String())
	}
	return *resp.Data
}

func decodeSyncHeldSaleList(t *testing.T, rec *httptest.ResponseRecorder) []syncHeldSaleRow {
	t.Helper()
	var resp struct {
		Data  []syncHeldSaleRow `json:"data"`
		Error any               `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %q)", err, rec.Body.String())
	}
	return resp.Data
}

func TestSyncHeldSales_RequiresBearer(t *testing.T) {
	mux, dp := newSyncHeldSalesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	body := `{"id":"h1","payload":"{}","updated_at":"2026-09-15 10:00:00"}`

	for _, bearer := range []string{"", "wrong"} {
		if rec := postSyncHeldSaleJSON(mux, "upsert", body, bearer); rec.Code != http.StatusUnauthorized {
			t.Fatalf("upsert bearer %q: status = %d, want 401", bearer, rec.Code)
		}
		if rec := postSyncHeldSaleJSON(mux, "delete", `{"id":"h1"}`, bearer); rec.Code != http.StatusUnauthorized {
			t.Fatalf("delete bearer %q: status = %d, want 401", bearer, rec.Code)
		}
		if rec := getSyncHeldSales(mux, bearer); rec.Code != http.StatusUnauthorized {
			t.Fatalf("list bearer %q: status = %d, want 401", bearer, rec.Code)
		}
	}
	// Nothing was written by the refused calls.
	if rows, err := data.NewHeldSalesRepo(dp.Db).List(t.Context()); err != nil || len(rows) != 0 {
		t.Fatalf("a refused upsert must write nothing, got %+v err=%v", rows, err)
	}
}

func TestSyncHeldSales_ValidationIs400(t *testing.T) {
	mux, dp := newSyncHeldSalesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	for _, tc := range []struct{ action, body string }{
		{"upsert", `not json`},
		{"upsert", `{"id":"  ","payload":"{}"}`},
		{"upsert", `{"id":"h1","payload":""}`},
		{"delete", `not json`},
		{"delete", `{"id":""}`},
	} {
		if rec := postSyncHeldSaleJSON(mux, tc.action, tc.body, "bearer-t2"); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s %q: status = %d, want 400 (body %q)", tc.action, tc.body, rec.Code, rec.Body.String())
		}
	}
}

// Happy path: an upsert lands on the primary's held_sales with every field
// (created_at honoured, so the Open orders page's age survives a re-park
// after a cross-till resume, ut-docs#1918), the list serves it back, and a
// delete removes it -- idempotently.
func TestSyncHeldSales_UpsertListDelete(t *testing.T) {
	mux, dp := newSyncHeldSalesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	// held_sales.table_id is a real FK onto tables (001_init.sql), so the
	// table must exist on this migrated database first.
	tableID, err := data.NewPOSRepo(dp.Db).CreateTable(t.Context(), "T4", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	row := syncHeldSaleRow{ID: "h1", Label: "Table 4", TableID: tableID, Payload: `{"lines":[]}`, LineCount: 3, TotalMinor: 1250, CreatedAt: "2026-09-15 09:00:00", UpdatedAt: "2026-09-15 10:00:00"}
	rec := postSyncHeldSaleJSON(mux, "upsert", upsertBody(t, row), "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if res := decodeSyncHeldSaleUpsert(t, rec); !res.Applied {
		t.Fatalf("a fresh upsert must report applied=true, got %+v", res)
	}

	got, ok, err := data.NewHeldSalesRepo(dp.Db).Get(t.Context(), "h1")
	if err != nil || !ok {
		t.Fatalf("Get h1 on the primary: ok=%v err=%v", ok, err)
	}
	if got.Label != "Table 4" || got.TableID != tableID || got.Payload != `{"lines":[]}` || got.LineCount != 3 || got.TotalMinor != 1250 || got.CreatedAt != "2026-09-15 09:00:00" || got.UpdatedAt != "2026-09-15 10:00:00" {
		t.Fatalf("upsert must land every field as sent, got %+v", got)
	}

	list := decodeSyncHeldSaleList(t, getSyncHeldSales(mux, "bearer-t2"))
	if len(list) != 1 || list[0] != row {
		t.Fatalf("list must serve the row back exactly as stored, got %+v want %+v", list, row)
	}

	for i := 0; i < 2; i++ {
		rec = postSyncHeldSaleJSON(mux, "delete", `{"id":"h1"}`, "bearer-t2")
		if rec.Code != http.StatusOK {
			t.Fatalf("delete #%d: status = %d, want 200 (body %q)", i+1, rec.Code, rec.Body.String())
		}
		var resp struct {
			Data struct {
				Deleted bool `json:"deleted"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || !resp.Data.Deleted {
			t.Fatalf("delete #%d must be 200/deleted=true even when already gone, got %q err=%v", i+1, rec.Body.String(), err)
		}
	}
	if list := decodeSyncHeldSaleList(t, getSyncHeldSales(mux, "bearer-t2")); len(list) != 0 {
		t.Fatalf("list after delete must be empty, got %+v", list)
	}
}

// TestSyncHeldSales_StaleUpsertIsRefused is THE test ADR-0093 exists for:
// two tills push an update to the same parked order close together and
// the writes land on the primary out of order -- the NEWER one first, then
// the OLDER one. The older one must lose the predicate guard cleanly:
// applied=false on a 200 (not an error status -- it's a business
// refusal, not a caller bug), and the row must still hold the newer
// values byte-for-byte. Without the guard the second write would silently
// clobber the first, which is exactly the race #1903 named.
func TestSyncHeldSales_StaleUpsertIsRefused(t *testing.T) {
	mux, dp := newSyncHeldSalesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedSyncOrdersTill(t, dp, "Till 3", "bearer-t3")

	newer := syncHeldSaleRow{ID: "h1", Label: "Table 4", Payload: `{"v":"newer"}`, LineCount: 2, TotalMinor: 900, UpdatedAt: "2026-09-15 10:00:05"}
	older := syncHeldSaleRow{ID: "h1", Label: "Table 4", Payload: `{"v":"older"}`, LineCount: 1, TotalMinor: 400, UpdatedAt: "2026-09-15 10:00:03"}

	rec := postSyncHeldSaleJSON(mux, "upsert", upsertBody(t, newer), "bearer-t2")
	if rec.Code != http.StatusOK || !decodeSyncHeldSaleUpsert(t, rec).Applied {
		t.Fatalf("newer upsert must apply, got %d %q", rec.Code, rec.Body.String())
	}

	rec = postSyncHeldSaleJSON(mux, "upsert", upsertBody(t, older), "bearer-t3")
	if rec.Code != http.StatusOK {
		t.Fatalf("a stale upsert is a 200 with applied=false, not an error status, got %d %q", rec.Code, rec.Body.String())
	}
	if res := decodeSyncHeldSaleUpsert(t, rec); res.Applied {
		t.Fatalf("the OLDER upsert must be refused (applied=false), got %+v", res)
	}

	got, ok, err := data.NewHeldSalesRepo(dp.Db).Get(t.Context(), "h1")
	if err != nil || !ok {
		t.Fatalf("Get h1: ok=%v err=%v", ok, err)
	}
	if got.Payload != `{"v":"newer"}` || got.LineCount != 2 || got.TotalMinor != 900 || got.UpdatedAt != "2026-09-15 10:00:05" {
		t.Fatalf("the row must still hold the NEWER write after a stale one was refused, got %+v", got)
	}

	// And a genuinely newer follow-up from the refused till (its re-fetch
	// and retry, per the ADR) applies as normal.
	retry := syncHeldSaleRow{ID: "h1", Label: "Table 4", Payload: `{"v":"retry"}`, LineCount: 3, TotalMinor: 1300, UpdatedAt: "2026-09-15 10:00:09"}
	rec = postSyncHeldSaleJSON(mux, "upsert", upsertBody(t, retry), "bearer-t3")
	if rec.Code != http.StatusOK || !decodeSyncHeldSaleUpsert(t, rec).Applied {
		t.Fatalf("a newer retry must apply, got %d %q", rec.Code, rec.Body.String())
	}
	if got, _, _ := data.NewHeldSalesRepo(dp.Db).Get(t.Context(), "h1"); got.Payload != `{"v":"retry"}` {
		t.Fatalf("the retry must have landed, got %+v", got)
	}
}

// TestSyncHeldSales_BlankUpdatedAtIsStampedByPrimaryClock is ut-docs#2271,
// closing an ADR-0093 Decision 2 accepted residual: a replica used to
// stamp updated_at with its OWN wall clock before pushing a write, so a
// genuinely newer edit from a slow-clocked till could lose the guard to an
// older edit from a fast-clocked one (refused cleanly, but still the wrong
// outcome). The fix is that a replica going through the real write-through
// path (held_sale_sync_proxy.go's heldSaleWriteThrough) never sends
// updated_at at all any more -- this pins the PRIMARY side of that fix: a
// blank incoming updated_at is stamped with the primary's OWN clock (the
// single serialization point the guard already depends on), and the
// stamped value is handed back on the wire so a replica can mirror the
// row locally under the exact value the primary holds.
func TestSyncHeldSales_BlankUpdatedAtIsStampedByPrimaryClock(t *testing.T) {
	mux, dp := newSyncHeldSalesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	before := time.Now().UTC()
	row := syncHeldSaleRow{ID: "h1", Label: "Table 4", Payload: `{"lines":[]}`, LineCount: 1, TotalMinor: 100}
	// UpdatedAt deliberately left blank -- exactly what heldSaleWriteThrough
	// now sends, never a caller/replica-stamped value.
	rec := postSyncHeldSaleJSON(mux, "upsert", upsertBody(t, row), "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	res := decodeSyncHeldSaleUpsert(t, rec)
	if !res.Applied {
		t.Fatalf("a fresh upsert with a blank updated_at must still apply, got %+v", res)
	}
	if res.UpdatedAt == "" {
		t.Fatal("the primary must report the value it actually stamped, so a replica can mirror it exactly")
	}
	stamped, err := time.Parse(heldSaleTimeLayout, res.UpdatedAt)
	if err != nil {
		t.Fatalf("stamped updated_at %q must parse as %s: %v", res.UpdatedAt, heldSaleTimeLayout, err)
	}
	if stamped.Before(before.Add(-2 * time.Second)) {
		t.Fatalf("the stamped updated_at %v must be close to this process's own clock (test ran at %v) -- it must come from the PRIMARY, never an unrelated/caller clock", stamped, before)
	}

	got, ok, err := data.NewHeldSalesRepo(dp.Db).Get(t.Context(), "h1")
	if err != nil || !ok {
		t.Fatalf("Get h1: ok=%v err=%v", ok, err)
	}
	if got.UpdatedAt != res.UpdatedAt {
		t.Fatalf("the stored row must hold exactly the stamped/reported value, got %q want %q", got.UpdatedAt, res.UpdatedAt)
	}

	// A second blank-timestamped push, moments later, is measured against
	// the primary's own clock again -- never against whatever a replica's
	// clock might have forged -- and must never regress behind the first.
	second := syncHeldSaleRow{ID: "h1", Label: "Table 4", Payload: `{"v":"second"}`, LineCount: 1, TotalMinor: 200}
	rec = postSyncHeldSaleJSON(mux, "upsert", upsertBody(t, second), "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("second upsert: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	res2 := decodeSyncHeldSaleUpsert(t, rec)
	if !res2.Applied {
		t.Fatalf("a second blank-timestamped upsert must apply (equal-or-later always applies), got %+v", res2)
	}
	if res2.UpdatedAt < res.UpdatedAt {
		t.Fatalf("the primary's own second stamp %q must never be earlier than its first %q", res2.UpdatedAt, res.UpdatedAt)
	}

	// A caller-supplied (non-blank) updated_at is still honoured exactly as
	// before -- this fix only changes what happens when it is left blank.
	explicit := syncHeldSaleRow{ID: "h2", Label: "Table 5", Payload: `{"v":"explicit"}`, LineCount: 1, TotalMinor: 300, UpdatedAt: "2026-09-15 10:00:00"}
	rec = postSyncHeldSaleJSON(mux, "upsert", upsertBody(t, explicit), "bearer-t2")
	if res3 := decodeSyncHeldSaleUpsert(t, rec); !res3.Applied || res3.UpdatedAt != "2026-09-15 10:00:00" {
		t.Fatalf("a caller-supplied updated_at must be honoured as-is, got %+v", res3)
	}
}
