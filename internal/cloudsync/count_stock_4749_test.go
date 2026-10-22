package cloudsync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// adjust_stock count mode routing (ut-docs#4749, ADR-0168 §4).

func countDirective(p map[string]any) directive {
	return directive{ID: "d1", Type: "adjust_stock", Payload: p, ServerTime: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
}

func TestApplyAdjustStock_CountModeReachesTheHook(t *testing.T) {
	var got CountStockRequest
	hooks := Hooks{CountStock: func(_ context.Context, r CountStockRequest) (string, error) { got = r; return "counted", nil }}
	status, msg := apply(context.Background(), countDirective(map[string]any{
		"mode": "count", "item_id": "i1", "variant_id": "v1", "location_id": "l1",
		"target": 12.5, "counted_at": "2026-10-22T11:00:00Z", "reason": "stock file",
	}), hooks)
	if status != "applied" || msg != "counted" {
		t.Fatalf("status/msg = %q/%q", status, msg)
	}
	want := CountStockRequest{
		ItemID: "i1", VariantID: "v1", LocationID: "l1", Target: 12.5, Reason: "stock file",
		CountedAt: time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC), CloudNow: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC),
	}
	if !got.CountedAt.Equal(want.CountedAt) || !got.CloudNow.Equal(want.CloudNow) {
		t.Fatalf("times = %v / %v", got.CountedAt, got.CloudNow)
	}
	got.CountedAt, got.CloudNow, want.CountedAt, want.CloudNow = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	if got != want {
		t.Fatalf("request = %+v, want %+v", got, want)
	}
}

func TestApplyAdjustStock_CountModeRefusals(t *testing.T) {
	hooks := Hooks{CountStock: func(context.Context, CountStockRequest) (string, error) { t.Fatal("hook must not run"); return "", nil }}
	ok := func() map[string]any {
		return map[string]any{"mode": "count", "item_id": "i1", "target": 3.0, "counted_at": "2026-10-22T11:00:00Z"}
	}
	for name, mut := range map[string]func(map[string]any){
		"no item":        func(p map[string]any) { delete(p, "item_id") },
		"no target":      func(p map[string]any) { delete(p, "target") },
		"negative":       func(p map[string]any) { p["target"] = -1.0 },
		"string target":  func(p map[string]any) { p["target"] = "3" },
		"huge":           func(p map[string]any) { p["target"] = 1e12 },
		"no counted_at":  func(p map[string]any) { delete(p, "counted_at") },
		"bad counted_at": func(p map[string]any) { p["counted_at"] = "yesterday" },
		"bad location":   func(p map[string]any) { p["location_id"] = 7.0 },
	} {
		p := ok()
		mut(p)
		if status, _ := apply(context.Background(), countDirective(p), hooks); status != "failed" {
			t.Errorf("%s: status = %q, want failed", name, status)
		}
	}
	// A till without the hook refuses, naming the mode.
	if status, msg := apply(context.Background(), countDirective(ok()), Hooks{}); status != "failed" || msg == "" {
		t.Errorf("no hook: %q %q", status, msg)
	}
	// A mode that is not text is refused too, never read as a delta.
	pn := ok()
	pn["mode"] = 1.0
	if status, _ := apply(context.Background(), countDirective(pn), hooks); status != "failed" {
		t.Errorf("non-text mode: status = %q", status)
	}
	// An unknown mode is refused, never read as a delta.
	p := ok()
	p["mode"] = "teleport"
	p["qty_delta"] = 5.0
	hooks.AdjustStock = func(context.Context, string, string, string, float64, string) (string, error) {
		t.Fatal("must not adjust")
		return "", nil
	}
	if status, _ := apply(context.Background(), countDirective(p), hooks); status != "failed" {
		t.Errorf("unknown mode: status = %q", status)
	}
}

func TestApplyAdjustStock_DeltaModeUnchanged(t *testing.T) {
	var delta float64
	hooks := Hooks{AdjustStock: func(_ context.Context, _, _, _ string, d float64, _ string) (string, error) {
		delta = d
		return "ok", nil
	}}
	for _, p := range []map[string]any{
		{"item_id": "i1", "qty_delta": 2.0},
		{"item_id": "i1", "qty_delta": 2.0, "mode": "delta"},
	} {
		delta = 0
		if status, _ := apply(context.Background(), directive{Type: "adjust_stock", Payload: p}, hooks); status != "applied" || delta != 2 {
			t.Errorf("payload %v: status %q delta %v", p, status, delta)
		}
	}
}

// The sync response's server_time rides on every directive it serves, so a
// count can convert the cloud's counted_at into the till's clock.
func TestPushSync_ServerTimeIsStampedOnDirectives(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"directives":[{"id":"a","type":"adjust_stock","payload":{}}],"server_time":"2026-10-22T12:00:00Z"}}`))
	}))
	defer srv.Close()
	cfg, d := bootEnrolledTill(t, srv.URL)
	settings := data.NewSettingsRepo(d.DB)
	ctx := context.Background()
	dirs, err := pushSync(ctx, cfg, settings, buildSyncRequest(ctx, cfg, settings, Hooks{}))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("pushSync: %v, %d directives", err, len(dirs))
	}
	if want := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC); !dirs[0].ServerTime.Equal(want) {
		t.Fatalf("ServerTime = %v, want %v", dirs[0].ServerTime, want)
	}
	if dirs[0].ReceivedAt.IsZero() || time.Since(dirs[0].ReceivedAt) > time.Minute {
		t.Fatalf("ReceivedAt = %v, want the moment the response arrived", dirs[0].ReceivedAt)
	}
}
