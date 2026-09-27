package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3038, the card's AC end to end through the Open orders reader:
// heldSalesForDisplay fetches the primary's list, then reconciles. While
// that fetch is in flight, a failed resume on this till gives the order
// back local-only (UpsertLocalOnly). The fake primary performs that
// give-back inside its list handler -- after the request left this till,
// before the reply lands -- and still lists the order, exactly the stale
// list of the card's scenario. The reconcile must not confirm the
// local-only row, and the next render (the primary no longer lists the
// order, it was claimed off it) must keep it.
func TestHeldSalesForDisplay_StaleListDoesNotLoseLocalOnlyGiveBack(t *testing.T) {
	const id = "hold-stale-list-render"
	var repo *data.HeldSalesRepo
	var renders atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/sync/held-sales" {
			t.Errorf("unexpected primary call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		rows := []syncHeldSaleRow{}
		if renders.Add(1) == 1 {
			// The concurrent give-back lands while this fetch is in flight.
			if err := repo.UpsertLocalOnly(context.Background(), data.HeldSale{ID: id, Label: "Table 4", Payload: `{"lines":[]}`, TotalMinor: 100, LineCount: 1}); err != nil {
				t.Errorf("give-back: %v", err)
			}
			rows = append(rows, syncHeldSaleRow{ID: id, Label: "Table 4", Payload: `{"lines":[]}`, TotalMinor: 100, LineCount: 1})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": rows, "error": nil})
	}))
	defer primary.Close()

	_, dp := newHoldCrossTillReplica(t, primary.URL, "b-123")
	repo = data.NewHeldSalesRepo(dp.Db)
	ctx := context.Background()
	if err := repo.Upsert(ctx, data.HeldSale{ID: id, Label: "Table 4", Payload: `{"lines":[]}`, TotalMinor: 100, LineCount: 1, PrimarySynced: true}); err != nil {
		t.Fatal(err)
	}

	if _, err := heldSalesForDisplay(ctx, dp, repo); err != nil {
		t.Fatal(err)
	}
	got, ok, err := repo.Get(ctx, id)
	if err != nil || !ok || got.PrimarySynced {
		t.Fatalf("the stale list must not confirm the local-only give-back, got ok=%v %+v err=%v", ok, got, err)
	}

	items, err := heldSalesForDisplay(ctx, dp, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := repo.Get(ctx, id); err != nil || !ok {
		t.Fatalf("the open order was lost on the next render, ok=%v err=%v", ok, err)
	}
	found := false
	for _, h := range items {
		found = found || h.ID == id
	}
	if !found {
		t.Fatalf("the open order must still be listed, got %+v", items)
	}
	if n := renders.Load(); n != 2 {
		t.Fatalf("expected two primary list fetches, got %d", n)
	}
}
