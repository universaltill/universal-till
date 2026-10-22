package cloudsync

// ut-docs#4797: a generic directive whose result post failed is re-delivered
// by the cloud; it must not be applied twice. The stored first result is
// re-posted instead (one mechanism, keyed on the directive id).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// redeliverTwice runs two ticks against a cloud that delivers dir both times
// and refuses the first result post with a 500. It returns the cloud so the
// caller can compare what was posted.
func redeliverTwice(t *testing.T, name string, dir map[string]any, hooks Hooks) *guestCloud {
	t.Helper()
	cloud := &guestCloud{batches: [][]map[string]any{{dir}, {dir}}}
	posts := 0
	cloud.resultStatus = func(string) int {
		posts++ // called under cloud.mu
		if posts == 1 {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, name).DB
	for i := 0; i < 2; i++ {
		if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
			t.Fatal(err)
		}
	}
	res := cloud.snapshot()
	if len(res) != 2 {
		t.Fatalf("want a result post on each tick, got %v", res)
	}
	if res[0]["status"] != res[1]["status"] || res[0]["message"] != res[1]["message"] || res[1]["directive_id"] != dir["id"] {
		t.Fatalf("second post differs from the first: %v vs %v", res[0], res[1])
	}
	return cloud
}

func stockDirective(id string, extra map[string]any) map[string]any {
	p := map[string]any{"item_id": "i-1", "qty_delta": 5.0}
	for k, v := range extra {
		p[k] = v
	}
	return map[string]any{"id": id, "type": "adjust_stock", "payload": p}
}

func TestTick_AdjustStockDeltaNotReappliedAfterFailedResultPost(t *testing.T) {
	calls := 0
	hooks := Hooks{AdjustStock: func(context.Context, string, string, string, float64, string) (string, error) {
		calls++
		return "adjusted", nil
	}}
	c := redeliverTwice(t, "r4797_delta.db", stockDirective("d1", nil), hooks)
	if calls != 1 {
		t.Fatalf("AdjustStock ran %d times, want 1", calls)
	}
	if r := c.snapshot()[1]; r["status"] != "applied" || r["message"] != "adjusted" {
		t.Fatalf("re-posted %v", r)
	}
}

func TestTick_AdjustStockReceiveNotReappliedAfterFailedResultPost(t *testing.T) {
	calls := 0
	hooks := Hooks{
		// apply requires AdjustStock to be wired even for a receipt.
		AdjustStock: func(context.Context, string, string, string, float64, string) (string, error) { return "", nil },
		ReceiveStock: func(context.Context, ReceiveStockRequest) (string, error) {
			calls++
			return "received", nil
		},
	}
	c := redeliverTwice(t, "r4797_receive.db", stockDirective("d1", map[string]any{"type": "receive", "cost_price": 149.0}), hooks)
	if calls != 1 {
		t.Fatalf("ReceiveStock ran %d times, want 1", calls)
	}
	if r := c.snapshot()[1]; r["status"] != "applied" || r["message"] != "received" {
		t.Fatalf("re-posted %v", r)
	}
}

func TestTick_AdjustStockCountNotReappliedAfterFailedResultPost(t *testing.T) {
	calls := 0
	hooks := Hooks{CountStock: func(context.Context, CountStockRequest) (string, error) {
		calls++
		return "counted", nil
	}}
	dir := stockDirective("d1", map[string]any{"mode": "count", "target": 7.0, "counted_at": "2026-10-01T10:00:00Z"})
	c := redeliverTwice(t, "r4797_count.db", dir, hooks)
	if calls != 1 {
		t.Fatalf("CountStock ran %d times, want 1", calls)
	}
	if r := c.snapshot()[1]; r["status"] != "applied" || r["message"] != "counted" {
		t.Fatalf("re-posted %v", r)
	}
}

func TestTick_SetPriceNotReappliedAfterFailedResultPost(t *testing.T) {
	calls := 0
	hooks := Hooks{SetPrice: func(context.Context, string, int64) (string, error) {
		calls++
		return "price set", nil
	}}
	dir := map[string]any{"id": "d1", "type": "set_price", "payload": map[string]any{"item_id": "i-1", "price_minor": 250.0}}
	redeliverTwice(t, "r4797_price.db", dir, hooks)
	if calls != 1 {
		t.Fatalf("SetPrice ran %d times, want 1", calls)
	}
}

func TestTick_FailedDirectiveResultIsStoredToo(t *testing.T) {
	calls := 0
	hooks := Hooks{RenameItem: func(context.Context, string, string) (string, error) {
		calls++
		return "", context.DeadlineExceeded
	}}
	dir := map[string]any{"id": "d1", "type": "rename_item", "payload": map[string]any{"item_id": "i-1", "name": "New"}}
	c := redeliverTwice(t, "r4797_failed.db", dir, hooks)
	if calls != 1 || c.snapshot()[1]["status"] != "failed" {
		t.Fatalf("calls %d, second post %v", calls, c.snapshot()[1])
	}
}

func TestTick_AcknowledgedDirectiveStillDedupedOnLaterRedelivery(t *testing.T) {
	calls := 0
	hooks := Hooks{RenameItem: func(context.Context, string, string) (string, error) {
		calls++
		return "renamed", nil
	}}
	dir := map[string]any{"id": "d1", "type": "rename_item", "payload": map[string]any{"item_id": "i-1", "name": "New"}}
	cloud := &guestCloud{batches: [][]map[string]any{{dir}, {dir}}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, "r4797_acked.db").DB
	for i := 0; i < 2; i++ {
		_ = Tick(context.Background(), testCfg(srv.URL), db, hooks)
	}
	if calls != 1 {
		t.Fatalf("hook ran %d times for a re-delivered, already acknowledged directive, want 1", calls)
	}
}

// A refusal other than 404 (here 403) leaves the directive pending on the
// cloud, so the stored row must stay unposted: Prune keeps unposted rows 30
// days, posted ones 7, and a row dropped early would let a later
// re-delivery apply again (review of ut-docs#4797).
func TestTick_RefusedResultPostKeepsRowUnposted(t *testing.T) {
	hooks := Hooks{RenameItem: func(context.Context, string, string) (string, error) { return "renamed", nil }}
	dir := map[string]any{"id": "d1", "type": "rename_item", "payload": map[string]any{"item_id": "i-1", "name": "New"}}
	cloud := &guestCloud{batches: [][]map[string]any{{dir}}}
	cloud.resultStatus = func(string) int { return http.StatusForbidden }
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, "r4797_403.db").DB
	_ = Tick(context.Background(), testCfg(srv.URL), db, hooks)
	r, ok, err := data.NewDirectiveResultsRepo(db).Get(context.Background(), "d1")
	if err != nil || !ok || r.Posted {
		t.Fatalf("after a 403 want a stored, unposted row; got %+v ok=%v err=%v", r, ok, err)
	}
}

// A 404 means the cloud no longer holds the directive pending: the row is
// marked posted.
func TestTick_NotFoundResultPostMarksRowPosted(t *testing.T) {
	hooks := Hooks{RenameItem: func(context.Context, string, string) (string, error) { return "renamed", nil }}
	dir := map[string]any{"id": "d1", "type": "rename_item", "payload": map[string]any{"item_id": "i-1", "name": "New"}}
	cloud := &guestCloud{batches: [][]map[string]any{{dir}}}
	cloud.resultStatus = func(string) int { return http.StatusNotFound }
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, "r4797_404.db").DB
	_ = Tick(context.Background(), testCfg(srv.URL), db, hooks)
	r, ok, err := data.NewDirectiveResultsRepo(db).Get(context.Background(), "d1")
	if err != nil || !ok || !r.Posted {
		t.Fatalf("after a 404 want a posted row; got %+v ok=%v err=%v", r, ok, err)
	}
}
