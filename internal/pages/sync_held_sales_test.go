package pages

import (
	"context"
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

// Cross-till held-sale endpoints, primary side (ADR-0093, ut-docs#1920):
// the bearer-authed trio a replica's held_sale_sync_proxy.go talks to.
// Same shape as sync_vouchers_test.go: a real migrated database (so
// migration 030's updated_at is genuinely there), syncTill auth, JSON
// envelope, snake_case.

func newSyncHeldSalesTestDeps(t *testing.T) (*http.ServeMux, *common.Deps, *data.HeldSalesRepo) {
	t.Helper()
	dbase, err := db.Open(filepath.Join(t.TempDir(), "sync_held_sales.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })

	dp := &common.Deps{Db: dbase.DB}
	mux := http.NewServeMux()
	registerSyncHeldSales(mux, dp)
	return mux, dp, data.NewHeldSalesRepo(dbase.DB)
}

func syncHeldSalesReq(mux *http.ServeMux, method, path, body, bearer string) *httptest.ResponseRecorder {
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type syncHeldSalesUpsertResp struct {
	Data  *syncHeldSaleUpsertResult `json:"data"`
	Error any                       `json:"error"`
}

type syncHeldSalesListResp struct {
	Data  []syncHeldSaleRow `json:"data"`
	Error any               `json:"error"`
}

func decodeSyncHeldSalesUpsert(t *testing.T, rec *httptest.ResponseRecorder) syncHeldSaleUpsertResult {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var resp syncHeldSalesUpsertResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data == nil {
		t.Fatalf("data must be an object, got %s", rec.Body.String())
	}
	return *resp.Data
}

func TestSyncHeldSales_AllThreeRequireBearer(t *testing.T) {
	mux, dp, _ := newSyncHeldSalesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/sync/held-sales", ""},
		{http.MethodPost, "/api/sync/held-sales/upsert", `{"id":"h1","payload":"{}"}`},
		{http.MethodPost, "/api/sync/held-sales/delete", `{"id":"h1"}`},
	} {
		if rec := syncHeldSalesReq(mux, c.method, c.path, c.body, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no bearer: status = %d, want 401", c.method, c.path, rec.Code)
		}
		if rec := syncHeldSalesReq(mux, c.method, c.path, c.body, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a bad bearer: status = %d, want 401", c.method, c.path, rec.Code)
		}
	}
}

func TestSyncHeldSales_UpsertCreatesAndListReturnsIt(t *testing.T) {
	mux, dp, repo := newSyncHeldSalesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	// A replica's own fresh write: no updated_at on the wire, the primary
	// stamps it.
	out := decodeSyncHeldSalesUpsert(t, syncHeldSalesReq(mux, http.MethodPost, "/api/sync/held-sales/upsert",
		`{"id":"h1","label":"Table 4","total_minor":1250,"line_count":3,"payload":"{\"lines\":[]}","table_id":"","created_at":"2026-09-15 10:00:00"}`, "bearer-t2"))
	if !out.Applied {
		t.Fatal("a fresh row must be applied")
	}
	if out.Row == nil || out.Row.ID != "h1" || out.Row.Label != "Table 4" || out.Row.TotalMinor != 1250 || out.Row.LineCount != 3 {
		t.Fatalf("the applied row must be echoed back, got %+v", out.Row)
	}
	if out.Row.UpdatedAt == "" || out.Row.CreatedAt != "2026-09-15 10:00:00" {
		t.Fatalf("the primary must stamp updated_at and honour created_at, got %+v", out.Row)
	}
	got, found, err := repo.Get(context.Background(), "h1")
	if err != nil || !found || got.Payload != `{"lines":[]}` || got.UpdatedAt != out.Row.UpdatedAt {
		t.Fatalf("stored row = %+v found=%v err=%v (echoed stamp %q)", got, found, err, out.Row.UpdatedAt)
	}

	rec := syncHeldSalesReq(mux, http.MethodGet, "/api/sync/held-sales", "", "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status = %d (body %q)", rec.Code, rec.Body.String())
	}
	var list syncHeldSalesListResp
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != "h1" || list.Data[0].UpdatedAt != out.Row.UpdatedAt {
		t.Fatalf("list = %+v, want the one row with its stamp", list.Data)
	}
	// snake_case on the wire, pinned literally.
	for _, key := range []string{`"total_minor"`, `"line_count"`, `"table_id"`, `"created_at"`, `"updated_at"`} {
		if !strings.Contains(rec.Body.String(), key) {
			t.Fatalf("list body must carry %s, got %s", key, rec.Body.String())
		}
	}
}

// An empty primary lists [] (not null) -- the replica-side proxy treats a
// null data array as a malformed answer and falls back to local-only, so
// "no parked orders anywhere" must be distinguishable from "broken".
func TestSyncHeldSales_EmptyListIsAnEmptyArray(t *testing.T) {
	mux, dp, _ := newSyncHeldSalesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	rec := syncHeldSalesReq(mux, http.MethodGet, "/api/sync/held-sales", "", "bearer-t2")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("empty list: status=%d body=%s, want 200 with \"data\":[]", rec.Code, rec.Body.String())
	}
}

// The core ADR-0093 Decision 2 outcome: a stale write loses cleanly. 200,
// applied=false, and the CURRENT (newer) row comes back for the caller to
// adopt -- never a 409, never a clobber.
func TestSyncHeldSales_UpsertRefusesOlderStampAndReturnsCurrentRow(t *testing.T) {
	mux, dp, repo := newSyncHeldSalesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	ctx := context.Background()
	if err := repo.Upsert(ctx, data.HeldSale{ID: "h1", Label: "Newer", TotalMinor: 900, Payload: `{"v":2}`, UpdatedAt: "2026-09-15 12:00:00"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out := decodeSyncHeldSalesUpsert(t, syncHeldSalesReq(mux, http.MethodPost, "/api/sync/held-sales/upsert",
		`{"id":"h1","label":"Stale","total_minor":100,"line_count":1,"payload":"{\"v\":1}","updated_at":"2026-09-15 11:59:00"}`, "bearer-t2"))
	if out.Applied {
		t.Fatal("an older stamp must be refused")
	}
	if out.Row == nil || out.Row.Label != "Newer" || out.Row.TotalMinor != 900 || out.Row.UpdatedAt != "2026-09-15 12:00:00" {
		t.Fatalf("a refusal must carry the CURRENT row back, got %+v", out.Row)
	}
	if got, _, _ := repo.Get(ctx, "h1"); got.Label != "Newer" || got.Payload != `{"v":2}` {
		t.Fatalf("a refused write must leave the primary's row untouched, got %+v", got)
	}

	// A newer stamp then goes through.
	out = decodeSyncHeldSalesUpsert(t, syncHeldSalesReq(mux, http.MethodPost, "/api/sync/held-sales/upsert",
		`{"id":"h1","label":"Newest","total_minor":950,"line_count":2,"payload":"{\"v\":3}","updated_at":"2026-09-15 12:00:01"}`, "bearer-t2"))
	if !out.Applied || out.Row == nil || out.Row.Label != "Newest" {
		t.Fatalf("a newer stamp must be applied and echoed, got applied=%v row=%+v", out.Applied, out.Row)
	}
}

func TestSyncHeldSales_UpsertValidation(t *testing.T) {
	mux, dp, _ := newSyncHeldSalesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	if rec := syncHeldSalesReq(mux, http.MethodPost, "/api/sync/held-sales/upsert", `not json`, "bearer-t2"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid body: status = %d, want 400", rec.Code)
	}
	if rec := syncHeldSalesReq(mux, http.MethodPost, "/api/sync/held-sales/upsert", `{"id":"  ","payload":"{}"}`, "bearer-t2"); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank id: status = %d, want 400", rec.Code)
	}
	if rec := syncHeldSalesReq(mux, http.MethodPost, "/api/sync/held-sales/delete", `{"id":""}`, "bearer-t2"); rec.Code != http.StatusBadRequest {
		t.Fatalf("delete blank id: status = %d, want 400", rec.Code)
	}
	if rec := syncHeldSalesReq(mux, http.MethodPost, "/api/sync/held-sales/delete", `nope`, "bearer-t2"); rec.Code != http.StatusBadRequest {
		t.Fatalf("delete invalid body: status = %d, want 400", rec.Code)
	}
}

func TestSyncHeldSales_DeleteIsIdempotent(t *testing.T) {
	mux, dp, repo := newSyncHeldSalesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	ctx := context.Background()
	if err := repo.Insert(ctx, data.HeldSale{ID: "h1", Label: "Table 4", Payload: `{}`}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := syncHeldSalesReq(mux, http.MethodPost, "/api/sync/held-sales/delete", `{"id":"h1"}`, "bearer-t2")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("delete: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, found, _ := repo.Get(ctx, "h1"); found {
		t.Fatal("the row must be gone after delete")
	}
	// Already gone: still a 200 no-op, never an error.
	rec = syncHeldSalesReq(mux, http.MethodPost, "/api/sync/held-sales/delete", `{"id":"h1"}`, "bearer-t2")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("second delete: status=%d body=%s, want 200/deleted", rec.Code, rec.Body.String())
	}
	rec = syncHeldSalesReq(mux, http.MethodPost, "/api/sync/held-sales/delete", `{"id":"never-existed"}`, "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete of an unknown id: status=%d, want 200", rec.Code)
	}
}
