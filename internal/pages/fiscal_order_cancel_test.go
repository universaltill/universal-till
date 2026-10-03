package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// --- ADR-0138 D2 (ut-docs#3582): fiscal.order.cancel ---------------------------

// orderCancelRecorder records every fiscal.order.cancel request it is handed.
type orderCancelRecorder struct {
	mu  sync.Mutex
	raw []string
}

func (r *orderCancelRecorder) handle(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
	r.mu.Lock()
	r.raw = append(r.raw, string(ev.Payload))
	r.mu.Unlock()
	return json.RawMessage(`{"status":"acknowledged"}`), nil
}

func (r *orderCancelRecorder) calls() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]map[string]any, 0, len(r.raw))
	for _, s := range r.raw {
		var m map[string]any
		_ = json.Unmarshal([]byte(s), &m)
		out = append(out, m)
	}
	return out
}

func subscribeFiscalOrderCancelHandler(t *testing.T, dp *common.Deps, pluginID string, h plugins.EventHandler) {
	t.Helper()
	seedFiscalSignHookPlugin(t, dp, pluginID, fiscalOrderCancelEvent, true)
	if _, err := plugins.SharedBus(dp.Db).SubscribeWithHandler(context.Background(), pluginID, []string{fiscalOrderCancelEvent}, h); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
}

// Zero-plugin cost (ADR-0138 D2, ADR-0041 Decision A): with no
// fiscal.order.cancel subscriber the dispatch is one subscriber lookup --
// no allocation, no goroutine, no fiscal_order_starts read.
func TestFiscalOrderCancel_ZeroPluginAllocatesNothing(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	plugins.SharedBus(dp.Db).ResetSubscribers()
	allocs := testing.AllocsPerRun(100, func() {
		dispatchFiscalOrderCancel(context.Background(), dp, "hold-1", false)
	})
	if allocs != 0 {
		t.Fatalf("zero-plugin fiscal.order.cancel fast path must not allocate, got %v allocs/op", allocs)
	}
}

// The reserved wire shape (ADR-0138 D2): order_id, the tx_id/tx_revision
// fiscal.order.start captured for that order, and cancelled_at.
func TestFiscalOrderCancel_PayloadCarriesCapturedStart(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	if err := data.NewPOSRepo(dp.Db).RecordFiscalOrderStart(context.Background(), "hold-9", "held", "tx-order-9", 3); err != nil {
		t.Fatal(err)
	}
	rec := &orderCancelRecorder{}
	subscribeFiscalOrderCancelHandler(t, dp, "com.test.order-cancel", rec.handle)

	before := time.Now().UTC().Add(-time.Second)
	dispatchFiscalOrderCancel(context.Background(), dp, "hold-9", false)
	dp.WaitForAsyncWork()

	calls := rec.calls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one fiscal.order.cancel dispatch, got %d", len(calls))
	}
	c := calls[0]
	if c["order_id"] != "hold-9" || c["tx_id"] != "tx-order-9" || c["tx_revision"] != float64(3) {
		t.Fatalf("payload = %+v, want order_id hold-9, tx_id tx-order-9, tx_revision 3", c)
	}
	at, err := time.Parse(time.RFC3339, c["cancelled_at"].(string))
	if err != nil || at.Before(before) {
		t.Fatalf("cancelled_at must be an RFC3339 now-ish timestamp, got %v (%v)", c["cancelled_at"], err)
	}
	if len(c) != 4 {
		t.Fatalf("payload must stay minimal (ADR-0138 D2 shape), got %+v", c)
	}
}

// An order captured with no signer listening has no start row: the cancel
// still announces itself, with no tx fields at all rather than empty ones.
func TestFiscalOrderCancel_NoCapturedStartOmitsTx(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	rec := &orderCancelRecorder{}
	subscribeFiscalOrderCancelHandler(t, dp, "com.test.order-cancel-nostart", rec.handle)

	dispatchFiscalOrderCancel(context.Background(), dp, "hold-nostart", false)
	dp.WaitForAsyncWork()

	calls := rec.calls()
	if len(calls) != 1 {
		t.Fatalf("expected one dispatch, got %d", len(calls))
	}
	if _, ok := calls[0]["tx_id"]; ok {
		t.Fatalf("no captured start: tx_id must be omitted, got %+v", calls[0])
	}
	if _, ok := calls[0]["tx_revision"]; ok {
		t.Fatalf("no captured start: tx_revision must be omitted, got %+v", calls[0])
	}
}

// Known offline: never dispatched (same posture as fiscal.order.start).
func TestFiscalOrderCancel_KnownOfflineSkipsDispatch(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	var n atomic.Int32
	subscribeFiscalOrderCancelHandler(t, dp, "com.test.order-cancel-offline", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		n.Add(1)
		return nil, nil
	})
	dispatchFiscalOrderCancel(context.Background(), dp, "hold-off", true)
	dp.WaitForAsyncWork()
	if got := n.Load(); got != 0 {
		t.Fatalf("known-offline cancel must never dispatch, got %d", got)
	}
}

// The handler path: cancelling a held order through POST
// /api/pos/held/cancel dispatches fiscal.order.cancel for that order --
// from the same place as the audit row.
func TestFiscalOrderCancel_CancelHandlerDispatches(t *testing.T) {
	mux, dp := newFiscalOrderHoldDeps(t)
	rec := &orderCancelRecorder{}
	subscribeFiscalOrderCancelHandler(t, dp, "com.test.order-cancel-handler", rec.handle)
	seedCancelHeldOrder(t, dp, "hold-fc", "Sarah", "")

	if r := postOrderForm(t, mux, "/api/pos/held/cancel", "id=hold-fc&view=parked-orders&tab=hold"); r.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", r.Code, r.Body.String())
	}
	dp.WaitForAsyncWork()
	calls := rec.calls()
	if len(calls) != 1 || calls[0]["order_id"] != "hold-fc" {
		t.Fatalf("cancel must dispatch fiscal.order.cancel once for hold-fc, got %+v", calls)
	}
}

// Resuming an order is NOT a cancel: it never dispatches fiscal.order.cancel.
func TestFiscalOrderCancel_ResumeDoesNotDispatch(t *testing.T) {
	mux, dp := newFiscalOrderHoldDeps(t)
	rec := &orderCancelRecorder{}
	subscribeFiscalOrderCancelHandler(t, dp, "com.test.order-cancel-resume", rec.handle)
	seedCancelHeldOrder(t, dp, "hold-fr", "Sarah", "")

	if r := postOrderForm(t, mux, "/api/pos/resume", "id=hold-fr"); r.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", r.Code, r.Body.String())
	}
	dp.WaitForAsyncWork()
	if n := len(rec.calls()); n != 0 {
		t.Fatalf("a resume must not dispatch fiscal.order.cancel, got %d", n)
	}
}
