package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
