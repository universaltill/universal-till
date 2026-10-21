package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#4711: a replica's baskets cleared with New sale journal to the
// main till over LAN sync D3 (POST /api/sync/cleared-baskets), so the main
// till's Z report counts every till's clears — once.

func newSyncClearedBasketsTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newSyncSalesTestDeps(t)
	registerSyncClearedBaskets(mux, dp)
	return mux, dp
}

func postClearedBaskets(t *testing.T, mux http.Handler, bearer string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/cleared-baskets", bytes.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSyncClearedBasketsAPI_RejectsUnauthorized(t *testing.T) {
	mux, _ := newSyncClearedBasketsTestDeps(t)
	if rec := postClearedBaskets(t, mux, "", []byte(`[]`)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: status %d, want 401", rec.Code)
	}
	rec := postClearedBaskets(t, mux, "nope", []byte(`[]`))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"unauthorized"`) {
		t.Fatalf("unknown bearer: %d %s, want 401 unauthorized envelope", rec.Code, rec.Body.String())
	}
}

func TestSyncClearedBasketsAPI_RejectsOversizedAndUndecodableBatch(t *testing.T) {
	mux, dp := newSyncClearedBasketsTestDeps(t)
	if _, err := data.NewTillsRepo(dp.Db).InsertTill(context.Background(), "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(make([]journalClearedBasket, 101))
	if rec := postClearedBaskets(t, mux, "token-abc", raw); rec.Code != http.StatusBadRequest {
		t.Fatalf("101 entries: status %d, want 400", rec.Code)
	}
	if rec := postClearedBaskets(t, mux, "token-abc", []byte(`{not json`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("undecodable: status %d, want 400", rec.Code)
	}
}

func TestSyncClearedBasketsAPI_AppliesSkipsRejectsAndPinsTill(t *testing.T) {
	mux, dp := newSyncClearedBasketsTestDeps(t)
	ctx := context.Background()
	tillID, err := data.NewTillsRepo(dp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc"))
	if err != nil {
		t.Fatal(err)
	}
	// till_id on the wire is NOT part of the contract: a peer naming some
	// other till must still be booked under its own authenticated till.
	body := []byte(`[
 {"id":"cb-1","created_at":"2026-09-01T10:00:00Z","register_id":"reg-R","actor_id":"u1","line_count":2,"total_minor":750,"till_id":"spoofed-till"},
 {"id":"cb-1","created_at":"2026-09-01T10:00:00Z","line_count":2,"total_minor":750},
 {"id":"cb-bad","created_at":"yesterday","line_count":1,"total_minor":1},
 {"id":"","created_at":"2026-09-01T10:00:00Z","line_count":1,"total_minor":1},
 {"id":"cb-neg","created_at":"2026-09-01T10:00:00Z","line_count":1,"total_minor":-5},
 {"id":"cb-lines","created_at":"2026-09-01T10:00:00Z","line_count":-1,"total_minor":5},
 {"id":"cb-huge","created_at":"2026-09-01T10:00:00Z","line_count":1000000,"total_minor":5},
 {"id":"cb-2","created_at":"2026-09-01T10:00:00Z","actor_id":"u2","line_count":1,"total_minor":0}
]`)
	rec := postClearedBaskets(t, mux, "token-abc", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp syncNoSalesResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Applied != 2 || resp.Data.Skipped != 1 || resp.Data.Rejected != 5 || resp.Error != nil {
		t.Fatalf("response = %+v, want applied=2 skipped=1 rejected=5 error=null", resp)
	}
	rows, err := dp.Db.QueryContext(ctx, `SELECT id, till_id, line_count, total_minor FROM cleared_baskets ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id, till string
		var lines int
		var total int64
		if err := rows.Scan(&id, &till, &lines, &total); err != nil {
			t.Fatal(err)
		}
		if till != tillID {
			t.Errorf("%s stored till_id %q, want the authenticated till %q", id, till, tillID)
		}
		if id == "cb-1" && (lines != 2 || total != 750) {
			t.Errorf("cb-1 stored lines=%d total=%d, want 2/750", lines, total)
		}
		got = append(got, id)
	}
	if strings.Join(got, ",") != "cb-1,cb-2" {
		t.Fatalf("stored rows = %v, want [cb-1 cb-2]", got)
	}
	var audits int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action = 'cleared_baskets_synced' AND entity_id = ?`, tillID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("cleared_baskets_synced audit rows = %d (%v), want 1", audits, err)
	}
	// A pure replay: skipped only, no further audit row.
	rec = postClearedBaskets(t, mux, "token-abc", []byte(`[{"id":"cb-1","created_at":"2026-09-01T10:00:00Z","line_count":2,"total_minor":750}]`))
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body.String())
	}
	if resp.Data.Applied != 0 || resp.Data.Skipped != 1 || resp.Data.Rejected != 0 {
		t.Fatalf("replay response = %+v, want skipped=1 only", resp.Data)
	}
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action = 'cleared_baskets_synced'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit rows after a pure-skip replay = %d (%v), want still 1", audits, err)
	}
}

func TestJournalClearedBasket_WireFormatIsSnakeCase(t *testing.T) {
	raw, _ := json.Marshal(journalClearedBasket{ID: "a", CreatedAt: "b", RegisterID: "c", ActorID: "d", LineCount: 3, TotalMinor: 4})
	want := `{"id":"a","created_at":"b","register_id":"c","actor_id":"d","line_count":3,"total_minor":4}`
	if string(raw) != want {
		t.Fatalf("wire = %s, want %s (no till_id on the wire)", raw, want)
	}
}

// End to end: a clear on a secondary till reaches the main till through
// syncPushTick and the main till's Z report counts it ONCE — a second tick
// changes nothing.
func TestSyncPushTick_ClearedBasketReachesMainZReportOnce(t *testing.T) {
	primaryMux, primaryDp := newSyncClearedBasketsTestDeps(t)
	ctx := context.Background()
	if _, err := data.NewTillsRepo(primaryDp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(primaryMux)
	t.Cleanup(server.Close)

	_, replicaDp := newSyncSalesTestDeps(t)
	replicaRepo := data.NewPOSRepo(replicaDp.Db)
	now := time.Now().UTC().Format(time.RFC3339)
	// Two clears in the same second and no sales at all: the push must run
	// even when there is nothing in the sales journal.
	for _, id := range []string{"cb-r1", "cb-r2"} {
		if _, err := replicaRepo.InsertClearedBasket(ctx, data.ClearedBasket{
			ID: id, CreatedAt: now, RegisterID: "reg-replica", ActorID: "u1", LineCount: 1, Total: money.FromMinor(400),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range map[string]string{"sync.primary_url": server.URL, "sync.bearer": "token-abc"} {
		if err := replicaDp.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	client := &http.Client{Timeout: 5 * time.Second}
	primaryRepo := data.NewPOSRepo(primaryDp.Db)
	var day string
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT date(?, 'localtime')`, now).Scan(&day); err != nil {
		t.Fatal(err)
	}
	for tick := 1; tick <= 2; tick++ {
		syncPushTick(ctx, replicaDp, client)
		rep, err := primaryRepo.EndOfDay(ctx, day)
		if err != nil {
			t.Fatal(err)
		}
		if rep.ClearedBasketCount != 2 || rep.ClearedBasketTotal != 800 {
			t.Fatalf("tick %d: main Z report cleared = %d / %d, want 2 / 800 (each clear once)", tick, rep.ClearedBasketCount, rep.ClearedBasketTotal)
		}
	}
	if cursor, _, _ := replicaDp.Settings.Get(ctx, "sync.cleared_basket_push_cursor"); cursor != now+"|cb-r2" {
		t.Fatalf("cleared-basket cursor = %q, want %q", cursor, now+"|cb-r2")
	}
	if v, _, _ := replicaDp.Settings.Get(ctx, "sync.push_cursor"); v != "" {
		t.Fatalf("sync.push_cursor = %q, want untouched", v)
	}
	if v, _, _ := replicaDp.Settings.Get(ctx, "sync.last_push_at"); v == "" {
		t.Fatal("sync.last_push_at not stamped by a successful cleared-basket push")
	}
}

// An older primary that doesn't know the route answers 404: the replica keeps
// its cursor and retries next tick, rather than dropping the clears.
func TestSyncPushTick_ClearedBaskets404_CursorNotAdvanced(t *testing.T) {
	primaryMux, primaryDp := newSyncSalesTestDeps(t) // no /api/sync/cleared-baskets
	ctx := context.Background()
	if _, err := data.NewTillsRepo(primaryDp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync/cleared-baskets" {
			hits.Add(1)
		}
		primaryMux.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	_, replicaDp := newSyncSalesTestDeps(t)
	if _, err := data.NewPOSRepo(replicaDp.Db).InsertClearedBasket(ctx, data.ClearedBasket{ID: "cb-old", CreatedAt: "2026-09-01T10:00:00Z", LineCount: 1}); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"sync.primary_url": server.URL, "sync.bearer": "token-abc"} {
		if err := replicaDp.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	syncPushTick(ctx, replicaDp, &http.Client{Timeout: 5 * time.Second})
	if n := hits.Load(); n != 1 {
		t.Fatalf("cleared-basket push attempts = %d, want 1", n)
	}
	if v, _, _ := replicaDp.Settings.Get(ctx, "sync.cleared_basket_push_cursor"); v != "" {
		t.Fatalf("cursor = %q after a 404, want unchanged (empty)", v)
	}
	if v, _, _ := replicaDp.Settings.Get(ctx, "sync.last_push_at"); v != "" {
		t.Fatalf("sync.last_push_at = %q after a 404, want unstamped", v)
	}
}
