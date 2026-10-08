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
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3562: a replica's no-sale drawer opens journal to the main till
// over LAN sync D3 (POST /api/sync/no-sales), so the main till's ADR-0111
// rollup counts them under the reporting till's key — once.

func newSyncNoSalesTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newSyncSalesTestDeps(t)
	registerSyncNoSales(mux, dp)
	return mux, dp
}

type syncNoSalesResp struct {
	Data struct {
		Applied  int `json:"applied"`
		Skipped  int `json:"skipped"`
		Rejected int `json:"rejected"`
	} `json:"data"`
	Error any `json:"error"`
}

func postNoSales(t *testing.T, mux http.Handler, bearer string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/no-sales", bytes.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSyncNoSalesAPI_RejectsUnauthorized(t *testing.T) {
	mux, _ := newSyncNoSalesTestDeps(t)
	rec := postNoSales(t, mux, "", []byte(`[]`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: status %d, want 401", rec.Code)
	}
	rec = postNoSales(t, mux, "nope", []byte(`[]`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown bearer: status %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"unauthorized"`) {
		t.Fatalf("401 body = %s, want the unauthorized envelope", rec.Body.String())
	}
}

func TestSyncNoSalesAPI_RejectsOversizedBatch(t *testing.T) {
	mux, dp := newSyncNoSalesTestDeps(t)
	if _, err := data.NewTillsRepo(dp.Db).InsertTill(context.Background(), "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(make([]journalNoSale, 101))
	if rec := postNoSales(t, mux, "token-abc", raw); rec.Code != http.StatusBadRequest {
		t.Fatalf("101 entries: status %d, want 400", rec.Code)
	}
}

func TestSyncNoSalesAPI_AppliesSkipsRejectsAndPinsTill(t *testing.T) {
	mux, dp := newSyncNoSalesTestDeps(t)
	ctx := context.Background()
	tillID, err := data.NewTillsRepo(dp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc"))
	if err != nil {
		t.Fatal(err)
	}
	// till_id on the wire is NOT part of the contract: a peer naming some
	// other till must still be booked under its own authenticated till.
	body := []byte(`[
 {"id":"ns-1","created_at":"2026-09-01T10:00:00Z","register_id":"reg-R","actor_id":"u1","reason":"float","till_id":"spoofed-till"},
 {"id":"ns-1","created_at":"2026-09-01T10:00:00Z","register_id":"reg-R","actor_id":"u1"},
 {"id":"ns-bad","created_at":"yesterday","register_id":"reg-R"},
 {"id":"","created_at":"2026-09-01T10:00:00Z"},
 {"id":"ns-long","created_at":"2026-09-01T10:00:00Z","reason":"` + strings.Repeat("é", 201) + `"},
 {"id":"ns-2","created_at":"2026-09-01T10:00:00Z","actor_id":"u2","approver_id":"m1"}
]`)
	rec := postNoSales(t, mux, "token-abc", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp syncNoSalesResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Applied != 2 || resp.Data.Skipped != 1 || resp.Data.Rejected != 3 || resp.Error != nil {
		t.Fatalf("response = %+v, want applied=2 skipped=1 rejected=3 error=null", resp)
	}

	rows, err := dp.Db.QueryContext(ctx, `SELECT id, till_id, COALESCE(reason,'') FROM no_sale_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id, till, reason string
		if err := rows.Scan(&id, &till, &reason); err != nil {
			t.Fatal(err)
		}
		if till != tillID {
			t.Errorf("%s stored till_id %q, want the authenticated till %q", id, till, tillID)
		}
		got = append(got, id)
	}
	if strings.Join(got, ",") != "ns-1,ns-2" {
		t.Fatalf("stored rows = %v, want [ns-1 ns-2] (invalid entries skipped, duplicate once)", got)
	}

	var audits int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action = 'no_sales_synced' AND entity_id = ?`, tillID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("no_sales_synced audit rows = %d, want 1", audits)
	}

	// The same batch again: nothing new, no further audit row.
	rec = postNoSales(t, mux, "token-abc", []byte(`[{"id":"ns-1","created_at":"2026-09-01T10:00:00Z"}]`))
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	if resp.Data.Applied != 0 || resp.Data.Skipped != 1 || resp.Data.Rejected != 0 {
		t.Fatalf("retry response = %+v, want skipped=1 only", resp.Data)
	}
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action = 'no_sales_synced'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit rows after a pure-skip retry = %d (%v), want still 1", audits, err)
	}
}

func TestJournalNoSale_WireFormatIsSnakeCase(t *testing.T) {
	raw, _ := json.Marshal(journalNoSale{ID: "a", CreatedAt: "b", RegisterID: "c", ActorID: "d", ApproverID: "e", Reason: "f"})
	want := `{"id":"a","created_at":"b","register_id":"c","actor_id":"d","approver_id":"e","reason":"f"}`
	if string(raw) != want {
		t.Fatalf("wire = %s, want %s (no till_id on the wire)", raw, want)
	}
}

// The card's acceptance criterion, end to end: an open on a secondary till
// reaches the main till through syncPushTick and appears ONCE in the main
// till's aggregate under that till's key — and a second tick changes nothing.
func TestSyncPushTick_NoSaleOpenReachesMainAggregateOnce(t *testing.T) {
	primaryMux, primaryDp := newSyncNoSalesTestDeps(t)
	ctx := context.Background()
	replicaTillID, err := data.NewTillsRepo(primaryDp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(primaryMux)
	t.Cleanup(server.Close)

	_, replicaDp := newSyncSalesTestDeps(t)
	replicaRepo := data.NewPOSRepo(replicaDp.Db)
	now := time.Now().UTC().Format(time.RFC3339)
	// Two opens in the same second plus no sales at all: the no-sale push
	// must run even when there is nothing in the sales journal.
	for _, id := range []string{"ns-r1", "ns-r2"} {
		if _, err := replicaRepo.InsertNoSaleEvent(ctx, nil, data.NoSaleEvent{ID: id, CreatedAt: now, RegisterID: "reg-replica", ActorID: "u1"}); err != nil {
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

		opens, err := primaryRepo.NoSaleOpensForTill(ctx, day, replicaTillID, "self-till")
		if err != nil {
			t.Fatal(err)
		}
		if opens.Total != 2 || opens.ByActor["u1"] != 2 {
			t.Fatalf("tick %d: main till's opens for the replica = %+v, want 2 (each open once)", tick, opens)
		}
		agg, err := primaryRepo.SalesAggregateForTill(ctx, day, replicaTillID, "self-till", true)
		if err != nil {
			t.Fatal(err)
		}
		if agg.NoSaleCount != 2 {
			t.Fatalf("tick %d: aggregate no_sale_count = %d, want 2", tick, agg.NoSaleCount)
		}
		// Nothing lands under the main till's own key or the register key.
		for _, key := range []string{"self-till", "reg-replica"} {
			other, _ := primaryRepo.NoSaleOpensForTill(ctx, day, key, "self-till")
			if other.Total != 0 {
				t.Fatalf("tick %d: %d open(s) under %q, want 0", tick, other.Total, key)
			}
		}
	}
	cursor, _, _ := replicaDp.Settings.Get(ctx, "sync.no_sale_push_cursor")
	if cursor != now+"|ns-r2" {
		t.Fatalf("no-sale cursor = %q, want %q", cursor, now+"|ns-r2")
	}
	// The sales cursor is untouched: there were no sales.
	if v, _, _ := replicaDp.Settings.Get(ctx, "sync.push_cursor"); v != "" {
		t.Fatalf("sync.push_cursor = %q, want untouched", v)
	}
	// A no-sale-only push is still contact with the main till (review finding 3).
	if v, _, _ := replicaDp.Settings.Get(ctx, "sync.last_push_at"); v == "" {
		t.Fatal("sync.last_push_at not stamped by a successful no-sale push")
	}
}

// An older primary that doesn't know the route answers 404: the replica keeps
// its cursor and retries next tick, rather than dropping the opens.
func TestSyncPushTick_NoSales404_CursorNotAdvanced(t *testing.T) {
	primaryMux, primaryDp := newSyncSalesTestDeps(t) // no /api/sync/no-sales
	ctx := context.Background()
	if _, err := data.NewTillsRepo(primaryDp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync/no-sales" {
			hits.Add(1)
		}
		primaryMux.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	_, replicaDp := newSyncSalesTestDeps(t)
	if _, err := data.NewPOSRepo(replicaDp.Db).InsertNoSaleEvent(ctx, nil, data.NoSaleEvent{ID: "ns-old", CreatedAt: "2026-09-01T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"sync.primary_url": server.URL, "sync.bearer": "token-abc"} {
		if err := replicaDp.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	syncPushTick(ctx, replicaDp, &http.Client{Timeout: 5 * time.Second})
	if n := hits.Load(); n != 1 {
		t.Fatalf("no-sale push attempts = %d, want 1", n)
	}
	if v, _, _ := replicaDp.Settings.Get(ctx, "sync.no_sale_push_cursor"); v != "" {
		t.Fatalf("cursor = %q after a 404, want unchanged (empty)", v)
	}
}

func TestParseNoSalePushCursor(t *testing.T) {
	for in, want := range map[string][2]string{
		"":                          {"", ""},
		"2026-09-01T10:00:00Z|ns-1": {"2026-09-01T10:00:00Z", "ns-1"},
		"2026-09-01T10:00:00Z":      {"2026-09-01T10:00:00Z", ""},
	} {
		at, id := parseNoSalePushCursor(in)
		if at != want[0] || id != want[1] {
			t.Errorf("parse(%q) = (%q, %q), want %v", in, at, id, want)
		}
	}
}
