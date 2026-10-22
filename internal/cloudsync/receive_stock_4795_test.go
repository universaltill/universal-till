package cloudsync

import (
	"context"
	"strings"
	"testing"
)

// adjust_stock type / cost_price (ut-docs#4795): receive goes to the
// ReceiveStock hook with the cost; adjust (or no type) stays on AdjustStock;
// anything else fails the directive.

func TestApplyAdjustStock_ReceiveReachesTheReceiveHook(t *testing.T) {
	var got ReceiveStockRequest
	var adjusted bool
	hooks := Hooks{
		AdjustStock: func(context.Context, string, string, string, float64, string) (string, error) {
			adjusted = true
			return "adjusted", nil
		},
		ReceiveStock: func(_ context.Context, r ReceiveStockRequest) (string, error) {
			got = r
			return "received", nil
		},
	}
	st, msg := apply(context.Background(), directive{Type: "adjust_stock", Payload: map[string]any{
		"item_id": "i-1", "variant_id": "v-1", "location_id": "loc-2", "type": "receive", "qty_delta": 12.0, "cost_price": 149.0, "reason": "delivery",
	}}, hooks)
	if st != "applied" || msg != "received" || adjusted {
		t.Fatalf("status %q msg %q adjusted %v", st, msg, adjusted)
	}
	want := ReceiveStockRequest{ItemID: "i-1", VariantID: "v-1", LocationID: "loc-2", Quantity: 12, CostPrice: 149, Reason: "delivery"}
	if got != want {
		t.Fatalf("request = %+v, want %+v", got, want)
	}
}

func TestApplyAdjustStock_TypeAdjustAndAbsentStayOnAdjust(t *testing.T) {
	for _, typ := range []any{nil, "adjust", ""} {
		var adjusted bool
		hooks := Hooks{AdjustStock: func(context.Context, string, string, string, float64, string) (string, error) {
			adjusted = true
			return "ok", nil
		}}
		p := map[string]any{"item_id": "i-1", "qty_delta": -2.0}
		if typ != nil {
			p["type"] = typ
		}
		if st, msg := apply(context.Background(), directive{Type: "adjust_stock", Payload: p}, hooks); st != "applied" || !adjusted {
			t.Fatalf("type %v: %q %q adjusted %v", typ, st, msg, adjusted)
		}
	}
}

func TestApplyAdjustStock_RefusesBadTypeAndCost(t *testing.T) {
	hooks := Hooks{
		AdjustStock: func(context.Context, string, string, string, float64, string) (string, error) {
			t.Fatal("adjust hook called")
			return "", nil
		},
		ReceiveStock: func(context.Context, ReceiveStockRequest) (string, error) {
			t.Fatal("receive hook called")
			return "", nil
		},
	}
	for name, p := range map[string]map[string]any{
		"unknown type":      {"item_id": "i", "qty_delta": 1.0, "type": "transfer"},
		"type not text":     {"item_id": "i", "qty_delta": 1.0, "type": 1.0},
		"receive negative":  {"item_id": "i", "qty_delta": -1.0, "type": "receive"},
		"cost on adjust":    {"item_id": "i", "qty_delta": 1.0, "cost_price": 5.0},
		"cost on adjust 2":  {"item_id": "i", "qty_delta": 1.0, "type": "adjust", "cost_price": 5.0},
		"negative cost":     {"item_id": "i", "qty_delta": 1.0, "type": "receive", "cost_price": -1.0},
		"cost over ceiling": {"item_id": "i", "qty_delta": 1.0, "type": "receive", "cost_price": 1000000000.0},
		"fractional cost":   {"item_id": "i", "qty_delta": 1.0, "type": "receive", "cost_price": 1.5},
		"cost not a number": {"item_id": "i", "qty_delta": 1.0, "type": "receive", "cost_price": true},
	} {
		if st, msg := apply(context.Background(), directive{Type: "adjust_stock", Payload: p}, hooks); st != "failed" {
			t.Errorf("%s: status %q msg %q, want failed", name, st, msg)
		}
	}
	// A till build without the hook says so rather than booking an adjustment.
	st, msg := apply(context.Background(), directive{Type: "adjust_stock", Payload: map[string]any{"item_id": "i", "qty_delta": 1.0, "type": "receive"}},
		Hooks{AdjustStock: hooks.AdjustStock})
	if st != "failed" || !strings.Contains(msg, "receipt") {
		t.Fatalf("no hook: %q %q", st, msg)
	}
}
