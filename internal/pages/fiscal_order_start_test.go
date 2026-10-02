package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// --- ADR-0138 (ut-docs#3310): fiscal.order.start ------------------------------

// orderStartRecorder is an in-process fiscal.order.start answerer that
// records every request payload it is handed and answers with a fixed body.
type orderStartRecorder struct {
	mu       sync.Mutex
	payloads []fiscalOrderStartPayload
	raw      []string
	answer   string
	err      error
}

func (r *orderStartRecorder) handle(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
	var p fiscalOrderStartPayload
	_ = json.Unmarshal(ev.Payload, &p)
	r.mu.Lock()
	r.payloads = append(r.payloads, p)
	r.raw = append(r.raw, string(ev.Payload))
	r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return json.RawMessage(r.answer), nil
}

func (r *orderStartRecorder) calls() []fiscalOrderStartPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]fiscalOrderStartPayload(nil), r.payloads...)
}

// subscribeFiscalOrderStartHandler registers h as the fiscal.order.start
// answerer — seeded rows plus an in-process Go handler, same shape as
// subscribeFiscalSignStartHandler.
func subscribeFiscalOrderStartHandler(t *testing.T, dp *common.Deps, pluginID string, h plugins.EventHandler) {
	t.Helper()
	seedFiscalSignHookPlugin(t, dp, pluginID, fiscalOrderStartEvent, true)
	if _, err := plugins.SharedBus(dp.Db).SubscribeWithHandler(context.Background(), pluginID, []string{fiscalOrderStartEvent}, h); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
}

// newFiscalOrderHoldDeps is newFiscalSignDeps with the hold/resume routes
// mounted on the same mux, so one test can park, resume and tender through
// the real handlers.
func newFiscalOrderHoldDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newFiscalSignDeps(t)
	registerHoldAPI(mux, dp)
	return mux, dp
}

func postOrderForm(t *testing.T, mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func onlyHeldSaleID(t *testing.T, dp *common.Deps) string {
	t.Helper()
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM held_sales`).Scan(&id); err != nil {
		t.Fatalf("expected exactly one held_sales row: %v", err)
	}
	return id
}

func countFiscalOrderStarts(t *testing.T, dp *common.Deps) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM fiscal_order_starts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Zero-plugin cost (ADR-0138 D2, ADR-0041 Decision A): with no
// fiscal.order.start subscriber, the dispatch both new call sites make is one
// subscriber lookup — zero allocations, and therefore no goroutine (a `go`
// statement allocates).
func TestFiscalOrderStart_ZeroPluginAllocatesNothing(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	plugins.SharedBus(dp.Db).ResetSubscribers()
	allocs := testing.AllocsPerRun(100, func() {
		dispatchFiscalOrderStart(context.Background(), dp, "hold-1", fiscalOrderKindHeld, false)
		dispatchFiscalOrderStart(context.Background(), dp, "hold-2", fiscalOrderKindCounter, false)
	})
	if allocs != 0 {
		t.Fatalf("zero-plugin fiscal.order.start fast path must not allocate, got %v allocs/op", allocs)
	}
}

// A genuine first park dispatches fiscal.order.start exactly once, with the
// held sale's own id and kind "held"; resuming and re-parking that SAME
// order never re-fires it (ADR-0138 D2: "never silently overwritten" — the
// order's TSE start happens once, at first capture).
func TestFiscalOrderStart_FirstParkDispatchesReParkDoesNot(t *testing.T) {
	mux, dp := newFiscalOrderHoldDeps(t)
	rec := &orderStartRecorder{answer: `{"status":"acknowledged","tx_id":"tx-order-1","tx_revision":1}`}
	subscribeFiscalOrderStartHandler(t, dp, "com.test.order-start", rec.handle)

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Add(-time.Second)
	if r := postOrderForm(t, mux, "/api/pos/hold", "label=Table+4"); r.Code != http.StatusOK || dp.Engine.HasItems() {
		t.Fatalf("hold failed: %d %s", r.Code, r.Body.String())
	}
	dp.WaitForAsyncWork()
	heldID := onlyHeldSaleID(t, dp)

	calls := rec.calls()
	if len(calls) != 1 {
		t.Fatalf("a first park must dispatch fiscal.order.start exactly once, got %d", len(calls))
	}
	if calls[0].OrderID != heldID || calls[0].OrderKind != "held" {
		t.Fatalf("payload must carry the held sale's own id and kind held, got %+v (held id %q)", calls[0], heldID)
	}
	startedAt, err := time.Parse(time.RFC3339, calls[0].StartedAt)
	if err != nil || startedAt.Before(before) {
		t.Fatalf("started_at must be an RFC3339 now-ish timestamp, got %q (%v)", calls[0].StartedAt, err)
	}
	got, ok, err := data.NewPOSRepo(dp.Db).GetFiscalOrderStart(context.Background(), heldID)
	if err != nil || !ok || got.TxID != "tx-order-1" || got.OrderKind != "held" {
		t.Fatalf("acknowledged answer must persist into fiscal_order_starts, got %+v ok=%v err=%v", got, ok, err)
	}

	// Resume, change the order, park again: same identity, no new dispatch.
	if r := postOrderForm(t, mux, "/api/pos/resume", "id="+heldID); r.Code != http.StatusOK {
		t.Fatalf("resume failed: %d %s", r.Code, r.Body.String())
	}
	if dp.Engine.HeldOrigin().ID != heldID {
		t.Fatalf("precondition: resumed basket must carry the held origin %q, got %+v", heldID, dp.Engine.HeldOrigin())
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	if r := postOrderForm(t, mux, "/api/pos/hold", ""); r.Code != http.StatusOK {
		t.Fatalf("re-park failed: %d %s", r.Code, r.Body.String())
	}
	dp.WaitForAsyncWork()
	if id := onlyHeldSaleID(t, dp); id != heldID {
		t.Fatalf("re-park must keep the order's identity, got %q want %q", id, heldID)
	}
	if n := len(rec.calls()); n != 1 {
		t.Fatalf("a re-park of an already-held order must NOT re-dispatch fiscal.order.start, got %d dispatches", n)
	}
}

// A hold request carrying the till's declared offline flag (the same
// #offline-flag / offline_override signal the tender path reads) never
// dispatches — and the park itself still succeeds (offline-first, ADR-0003).
func TestFiscalOrderStart_KnownOfflineParkSkipsDispatch(t *testing.T) {
	for _, body := range []string{"offline=1", "offline_override=1"} {
		t.Run(body, func(t *testing.T) {
			mux, dp := newFiscalOrderHoldDeps(t)
			var n atomic.Int32
			subscribeFiscalOrderStartHandler(t, dp, "com.test.order-start-offline", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
				n.Add(1)
				return json.RawMessage(`{"status":"acknowledged","tx_id":"tx","tx_revision":1}`), nil
			})
			if _, err := dp.Engine.Scan("ABC"); err != nil {
				t.Fatal(err)
			}
			if r := postOrderForm(t, mux, "/api/pos/hold", body); r.Code != http.StatusOK || dp.Engine.HasItems() {
				t.Fatalf("an offline park must still succeed: %d %s", r.Code, r.Body.String())
			}
			dp.WaitForAsyncWork()
			_ = onlyHeldSaleID(t, dp)
			if got := n.Load(); got != 0 {
				t.Fatalf("known-offline park must never dispatch fiscal.order.start, got %d", got)
			}
		})
	}
}

// dispatchFiscalOrderStart returns immediately — the plugin round trip runs
// on a goroutine the caller (a park / a kiosk checkout) never waits for.
func TestFiscalOrderStart_NeverBlocksTheCaller(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	release := make(chan struct{})
	subscribeFiscalOrderStartHandler(t, dp, "com.test.order-start-slow", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		<-release
		return json.RawMessage(`{"status":"acknowledged","tx_id":"tx-slow","tx_revision":1}`), nil
	})
	defer close(release)
	done := make(chan struct{})
	go func() {
		dispatchFiscalOrderStart(context.Background(), dp, "hold-slow", fiscalOrderKindHeld, false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatchFiscalOrderStart must return immediately, not wait for the plugin's handler")
	}
}

// Best-effort round trip: only {"status":"acknowledged"} with a non-empty,
// bounded tx_id and a non-negative revision is persisted. Every other answer
// — another status, garbage, a handler error, a wedged handler past the
// safety ceiling — persists nothing and raises nothing.
func TestFiscalOrderStart_OnlyValidAcknowledgedAnswerPersists(t *testing.T) {
	old := fiscalOrderStartAsyncTimeout
	fiscalOrderStartAsyncTimeout = 50 * time.Millisecond
	t.Cleanup(func() { fiscalOrderStartAsyncTimeout = old })

	cases := []struct {
		name    string
		answer  string
		err     error
		wedge   bool
		persist bool
	}{
		{name: "acknowledged", answer: `{"status":"acknowledged","tx_id":"tx-ok","tx_revision":4}`, persist: true},
		{name: "pending", answer: `{"status":"pending","tx_id":"tx-p","tx_revision":1}`},
		{name: "garbage", answer: `not json at all`},
		{name: "empty tx_id", answer: `{"status":"acknowledged","tx_id":"","tx_revision":1}`},
		{name: "oversized tx_id", answer: `{"status":"acknowledged","tx_id":"` + strings.Repeat("x", 257) + `","tx_revision":1}`},
		{name: "negative revision", answer: `{"status":"acknowledged","tx_id":"tx-neg","tx_revision":-1}`},
		{name: "handler error", err: errors.New("tse unreachable")},
		{name: "timeout", wedge: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, dp := newFiscalSignDeps(t)
			subscribeFiscalOrderStartHandler(t, dp, "com.test.order-start-rt", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
				if tc.wedge {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				if tc.err != nil {
					return nil, tc.err
				}
				return json.RawMessage(tc.answer), nil
			})
			dispatchFiscalOrderStart(context.Background(), dp, "hold-rt", fiscalOrderKindCounter, false)
			dp.WaitForAsyncWork()
			got, ok, err := data.NewPOSRepo(dp.Db).GetFiscalOrderStart(context.Background(), "hold-rt")
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.persist {
				t.Fatalf("persisted=%v, want %v (row %+v)", ok, tc.persist, got)
			}
			if tc.persist && (got.TxID != "tx-ok" || got.TxRevision != 4 || got.OrderKind != "counter") {
				t.Fatalf("unexpected persisted row: %+v", *got)
			}
		})
	}
}

// End to end through the REAL tender handler (ADR-0138 D2 correlation): a
// sale tendered from a resumed held order echoes that order's id as
// order_id on BOTH fiscal.sign.start and fiscal.sign.ask; an ordinary
// walk-up sale afterwards omits the field entirely.
func TestFiscalOrderStart_OrderIDEchoedAtTenderOfResumedOrder(t *testing.T) {
	mux, dp := newFiscalOrderHoldDeps(t)
	orders := &orderStartRecorder{answer: `{"status":"acknowledged","tx_id":"tx-order","tx_revision":1}`}
	subscribeFiscalOrderStartHandler(t, dp, "com.test.order-start-echo", orders.handle)

	var mu sync.Mutex
	var startRaw, askRaw []string
	subscribeFiscalSignStartHandler(t, dp, "com.test.sign-start-echo", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		mu.Lock()
		startRaw = append(startRaw, string(ev.Payload))
		mu.Unlock()
		return json.RawMessage(`{"status":"acknowledged","tx_id":"tx-sale","tx_revision":1}`), nil
	})
	subscribeFiscalSignHandler(t, dp, "com.test.sign-ask-echo", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		mu.Lock()
		askRaw = append(askRaw, string(ev.Payload))
		mu.Unlock()
		return json.RawMessage(`{"status":"approved"}`), nil
	})

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	if r := postOrderForm(t, mux, "/api/pos/hold", "label=Table+7"); r.Code != http.StatusOK {
		t.Fatalf("hold: %d", r.Code)
	}
	dp.WaitForAsyncWork()
	heldID := onlyHeldSaleID(t, dp)
	if r := postOrderForm(t, mux, "/api/pos/resume", "id="+heldID); r.Code != http.StatusOK {
		t.Fatalf("resume: %d", r.Code)
	}
	if r := fiscalSignTender(t, mux, false); r.Code != http.StatusOK {
		t.Fatalf("tender of resumed order: %d %s", r.Code, r.Body.String())
	}
	dp.WaitForAsyncWork()

	// Walk-up sale: no origin → order_id omitted on both.
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	if r := fiscalSignTender(t, mux, false); r.Code != http.StatusOK {
		t.Fatalf("walk-up tender: %d %s", r.Code, r.Body.String())
	}
	dp.WaitForAsyncWork()

	mu.Lock()
	defer mu.Unlock()
	if len(startRaw) != 2 || len(askRaw) != 2 {
		t.Fatalf("expected two start and two ask dispatches, got %d/%d", len(startRaw), len(askRaw))
	}
	for name, raw := range map[string]string{"fiscal.sign.start": startRaw[0], "fiscal.sign.ask": askRaw[0]} {
		var p struct {
			OrderID string `json:"order_id"`
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		if p.OrderID != heldID {
			t.Fatalf("%s for a resumed held order must carry order_id %q, got %q (%s)", name, heldID, p.OrderID, raw)
		}
	}
	for name, raw := range map[string]string{"fiscal.sign.start": startRaw[1], "fiscal.sign.ask": askRaw[1]} {
		if strings.Contains(raw, "order_id") {
			t.Fatalf("%s for a walk-up sale must omit order_id entirely, got %s", name, raw)
		}
	}
}

// A held order captured while NO fiscal.order.start subscriber existed has no
// fiscal_order_starts row, so its eventual tender omits order_id — never a
// fabricated default — even once a signer subscribes before tender.
func TestFiscalOrderStart_OrderIDOmittedWhenNothingCapturedAtParkTime(t *testing.T) {
	mux, dp := newFiscalOrderHoldDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	if r := postOrderForm(t, mux, "/api/pos/hold", ""); r.Code != http.StatusOK {
		t.Fatalf("hold: %d", r.Code)
	}
	dp.WaitForAsyncWork()
	heldID := onlyHeldSaleID(t, dp)
	if n := countFiscalOrderStarts(t, dp); n != 0 {
		t.Fatalf("no subscriber at capture → no row, got %d", n)
	}

	// Signer installed between capture and tender.
	subscribeFiscalOrderStartHandler(t, dp, "com.test.order-start-late", (&orderStartRecorder{answer: `{"status":"acknowledged","tx_id":"t","tx_revision":1}`}).handle)
	var askRaw string
	subscribeFiscalSignHandler(t, dp, "com.test.sign-ask-late", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		askRaw = string(ev.Payload)
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	if r := postOrderForm(t, mux, "/api/pos/resume", "id="+heldID); r.Code != http.StatusOK {
		t.Fatalf("resume: %d", r.Code)
	}
	if r := fiscalSignTender(t, mux, false); r.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", r.Code, r.Body.String())
	}
	dp.WaitForAsyncWork()
	if askRaw == "" || strings.Contains(askRaw, "order_id") {
		t.Fatalf("expected an ask dispatch with order_id omitted, got %q", askRaw)
	}
}

// The order_id lookup reads the sale's own carried origin
// (SaleInput.HeldOriginID), never another engine's: a refund/return-shaped
// SaleInput with no origin omits order_id even while the CASHIER basket
// (d.Engine) currently holds a resumed order with a fiscal_order_starts row.
func TestFiscalOrderStart_OrderIDNotBorrowedFromCashierBasket(t *testing.T) {
	mux, dp := newFiscalOrderHoldDeps(t)
	subscribeFiscalOrderStartHandler(t, dp, "com.test.order-start-iso", (&orderStartRecorder{answer: `{"status":"acknowledged","tx_id":"t","tx_revision":1}`}).handle)
	var askRaw string
	subscribeFiscalSignHandler(t, dp, "com.test.sign-ask-iso", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		askRaw = string(ev.Payload)
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postOrderForm(t, mux, "/api/pos/hold", "")
	dp.WaitForAsyncWork()
	heldID := onlyHeldSaleID(t, dp)
	postOrderForm(t, mux, "/api/pos/resume", "id="+heldID)
	if dp.Engine.HeldOrigin().ID != heldID || countFiscalOrderStarts(t, dp) != 1 {
		t.Fatal("precondition: cashier basket holds a resumed, captured order")
	}

	in := pos.SaleInput{Currency: "EUR", SaleType: "return"}
	dispatchFiscalSignAsk(context.Background(), dp, &in)
	if askRaw == "" || strings.Contains(askRaw, "order_id") {
		t.Fatalf("a sale with no carried origin must omit order_id regardless of d.Engine's state, got %q", askRaw)
	}

	// And with the origin carried, it is echoed (dispatch-level check).
	in2 := pos.SaleInput{Currency: "EUR", HeldOriginID: heldID}
	dispatchFiscalSignAsk(context.Background(), dp, &in2)
	if !strings.Contains(askRaw, `"order_id":"`+heldID+`"`) {
		t.Fatalf("carried origin with a captured row must echo order_id, got %q", askRaw)
	}
}

// Every pay-at-counter checkout is a genuine new order capture (each mints a
// fresh kiosk_counter_orders id), so each dispatches fiscal.order.start once
// with that id and kind "counter"; a kiosk known offline dispatches nothing.
// The counter order's id is ALSO its held sale's id, so recalling it on the
// till (resumeHeldSale → HeldOrigin.ID) correlates through the same single
// fiscal_order_starts lookup the held/table path uses.
func TestFiscalOrderStart_CounterCheckoutDispatchesEveryOrder(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	t.Cleanup(dp.WaitForAsyncWork)
	dp.State.KioskPaymentMode = common.KioskPaymentModeCounter
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)
	rec := &orderStartRecorder{answer: `{"status":"acknowledged","tx_id":"tx-counter","tx_revision":1}`}
	subscribeFiscalOrderStartHandler(t, dp, "com.test.order-start-counter", rec.handle)

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)
	registerHoldAPI(mux, dp)

	for i := 0; i < 2; i++ {
		postOrderForm(t, mux, "/api/self-order/scan", "code=5000001")
		if r := postOrderForm(t, mux, "/api/self-order/checkout", "offline=0"); r.Code != http.StatusOK {
			t.Fatalf("counter checkout %d: %d %s", i, r.Code, r.Body.String())
		}
	}
	dp.WaitForAsyncWork()

	ids := map[string]bool{}
	rows, err := dp.Db.Query(`SELECT id FROM kiosk_counter_orders`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids[id] = true
	}
	rows.Close()
	calls := rec.calls()
	if len(calls) != 2 || len(ids) != 2 {
		t.Fatalf("each counter checkout must dispatch once: %d dispatches for %d orders", len(calls), len(ids))
	}
	for _, c := range calls {
		if !ids[c.OrderID] || c.OrderKind != "counter" {
			t.Fatalf("dispatch must carry a real counter order id and kind counter, got %+v", c)
		}
	}
	if calls[0].OrderID == calls[1].OrderID {
		t.Fatal("two counter orders must dispatch two distinct order ids")
	}

	// Known-offline kiosk: the order is still placed, nothing is dispatched.
	postOrderForm(t, mux, "/api/self-order/scan", "code=5000001")
	if r := postOrderForm(t, mux, "/api/self-order/checkout", "offline=1"); r.Code != http.StatusOK {
		t.Fatalf("offline counter checkout must still place the order: %d", r.Code)
	}
	dp.WaitForAsyncWork()
	if n := len(rec.calls()); n != 2 {
		t.Fatalf("known-offline counter checkout must not dispatch, got %d total dispatches", n)
	}

	// Recall one on the till: its HeldOrigin.ID is the counter order's id,
	// which is what completeTender carries into SaleInput.HeldOriginID — so
	// the eventual fiscal.sign.ask echoes it.
	counterID := calls[0].OrderID
	if r := postOrderForm(t, mux, "/api/pos/resume", "id="+counterID); r.Code != http.StatusOK {
		t.Fatalf("resume counter order: %d %s", r.Code, r.Body.String())
	}
	if got := dp.Engine.HeldOrigin().ID; got != counterID {
		t.Fatalf("a recalled counter order's HeldOrigin.ID must be the counter order id %q, got %q", counterID, got)
	}
	var askRaw string
	subscribeFiscalSignHandler(t, dp, "com.test.sign-ask-counter", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		askRaw = string(ev.Payload)
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	in := pos.SaleInput{Currency: "GBP", HeldOriginID: dp.Engine.HeldOrigin().ID}
	dispatchFiscalSignAsk(context.Background(), dp, &in)
	if !strings.Contains(askRaw, `"order_id":"`+counterID+`"`) {
		t.Fatalf("tender of a recalled counter order must echo its order_id, got %q", askRaw)
	}
}
