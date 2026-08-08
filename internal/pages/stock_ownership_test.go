package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
)

// Stock ownership is primary-only (ADR-0036, amending ADR-0011 §3; field
// report ut-docs#404): a replica caches and reports stock, it never gates a
// sale on its own locally-read figure — the 30s sync tick cannot keep that
// copy current, so the local `cur+qtyDelta < 0` check passes/fails against
// data that is legitimately stale. These tests pin the four behaviours the
// fix consists of: replica bypass, unchanged primary gate, negative stock
// surfacing as a Problem, and the immediate (non-blocking) journal push.

// logging.Recent() is a process-global 50-entry ring shared by the whole
// test binary. A length-based before/after diff turned out NOT to be
// reliable here: this package's test suite has other tests whose own
// background loops keep logging (mostly "database is closed", from a
// loop that outlives its owning test's DB) well after they return, so the
// ring can already be churning entries — including overflowing its own
// 50-entry cap — purely from unrelated concurrent noise between a
// snapshot and the assertion, silently invalidating a length-based count.
// logging.ResetRecent() (test-only) sidesteps this by clearing the ring
// right before the scenario under test, so recentMatches only ever needs
// to filter concurrent-noise entries out by content, not disentangle them
// from a shifting length.

// recentMatches counts Problem lines (since the last logging.ResetRecent())
// that contain ALL the given substrings.
func recentMatches(subs ...string) int {
	n := 0
	for _, p := range logging.Recent() {
		ok := true
		for _, s := range subs {
			if !strings.Contains(p.Msg, s) {
				ok = false
				break
			}
		}
		if ok {
			n++
		}
	}
	return n
}

// A replica's direct sale must NEVER be blocked by its own local stock
// figure — even a request explicitly asking for the gate
// (allowNegativeInventory:false) is overridden, because the replica does
// not own the number the gate would read.
func TestTenderHandler_ReplicaDirectSaleNeverBlocksOnLocalStock(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	ctx := context.Background()
	// Mark this till a replica.
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	// itm1 has 50 units (seedForPages); sell 1000 — far past zero.
	if _, err := dp.Engine.ScanQty("ABC", 1000); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120000}],"allowNegativeInventory":false,"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("replica tender must complete regardless of local stock, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "insufficient stock") {
		t.Fatalf("replica tender must never surface the insufficient-stock error, got: %s", rec.Body.String())
	}
	var out struct {
		Data struct {
			SaleID string `json:"saleId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Data.SaleID == "" {
		t.Fatalf("expected a completed sale summary, err=%v body=%s", err, rec.Body.String())
	}
	var qty float64
	if err := dp.Db.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itm1'`).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != -950 {
		t.Fatalf("expected local stock cache at -950 after the oversell, got %v", qty)
	}
}

// The PRIMARY's own gate is unchanged by ADR-0036: with the shop setting
// off and no override, an oversell is still rejected exactly as before.
func TestTenderHandler_PrimaryDirectSaleGateUnchanged(t *testing.T) {
	mux, dp := newPOSTestDeps(t) // no sync.primary_url — primary/single till
	if _, err := dp.Engine.ScanQty("ABC", 1000); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120000}],"allowNegativeInventory":false,"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place toast), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "pos-notice error") {
		t.Fatalf("expected the insufficient-stock notice on the primary, got: %s", rec.Body.String())
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected no sale recorded when the primary's gate trips, got %d", count)
	}
}

// The kiosk checkout is the same class of direct-sale-completion path: a
// replica kiosk must complete the sale even when the local stock cache
// reads zero.
func TestSelfOrderShop_ReplicaCheckoutNeverBlocksOnLocalStock(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 0) // local cache says none left
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	post("/api/self-order/scan", "code=5000001")
	rec := post("/api/self-order/checkout", "method=card")
	if rec.Code != http.StatusOK {
		t.Fatalf("replica kiosk checkout must complete regardless of local stock, got %d: %s", rec.Code, rec.Body.String())
	}
	var count int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sales WHERE status='completed'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected the kiosk sale completed, got %d sales", count)
	}
}

// A journal replay that legitimately drives the primary's stock negative
// must surface a Warn-level Problem naming the item and resulting level —
// logging.Recent() feeds the back-office Problems panel.
func TestApplyJournal_NegativeStockSurfacesProblem(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()

	// itm1 has 50 units; a remote till sold 51.
	logging.ResetRecent()
	j := seedJournalSale("remote-neg-1", "T2-NEG-1", "sale", "", "itm1", 51, 100)
	applied, err := applyJournal(ctx, dp, "till-neg-1", j)
	if err != nil || !applied {
		t.Fatalf("expected the journal applied, got applied=%v err=%v", applied, err)
	}
	var qty float64
	if err := dp.Db.QueryRowContext(ctx, `SELECT quantity FROM inventory WHERE item_id='itm1'`).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != -1 {
		t.Fatalf("expected stock at -1 after the replay, got %v", qty)
	}
	if n := recentMatches("Apple", "T2-NEG-1"); n != 1 {
		t.Fatalf("expected exactly one Problem naming the item for receipt T2-NEG-1, got %d\nrecent: %+v", n, logging.Recent())
	}
}

// The primary's own direct sale gets the same Problem surfacing when the
// shop setting legitimately lets it go negative (ADR-0036: whichever
// till's application of a movement takes shop-wide stock negative
// surfaces it).
func TestTenderHandler_PrimaryDirectSaleNegativeStockSurfacesProblem(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	dp.State.AllowNegativeInventory = true // shop policy: oversell allowed
	if _, err := dp.Engine.ScanQty("ABC", 60); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	logging.ResetRecent()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":7200}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the oversell allowed by policy, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			ReceiptNo string `json:"receiptNo"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Data.ReceiptNo == "" {
		t.Fatalf("expected a receipt, err=%v body=%s", err, rec.Body.String())
	}
	// 50 - 60 = -10.
	if n := recentMatches("Apple", "-10.00", "sale "+out.Data.ReceiptNo); n < 1 {
		t.Fatalf("expected a Problem naming Apple at -10.00 for %s, got none\nrecent: %+v", out.Data.ReceiptNo, logging.Recent())
	}
}

// The ut-docs#404 field report, end to end: two tills sell the same last
// unit. Both cashiers succeed (no error either side), the journal replay
// lands the shop-wide truth at -1 on the primary, and exactly one Problem
// is logged for it.
func TestTwoTills_SameLastUnit_BothSalesSucceed_StockNegative_OneProblem(t *testing.T) {
	ctx := context.Background()

	// Primary till, with the replica enrolled, serving the journal endpoint.
	primaryMux, primaryDp := newSyncSalesTestDeps(t)
	tillID, err := data.NewTillsRepo(primaryDp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc"))
	if err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	server := httptest.NewServer(primaryMux)
	t.Cleanup(server.Close)

	// One unit left, shop-wide.
	if _, err := primaryDp.Db.ExecContext(ctx, `UPDATE inventory SET quantity=1 WHERE item_id='itm1'`); err != nil {
		t.Fatal(err)
	}

	// Replica till; seeded to 0, not 1 — deliberately WORSE than the field
	// report's actual staleness (till 1's sale hadn't synced anywhere yet,
	// so till 2's real-world cache still read 1). At cache=1, cur+qtyDelta
	// (1-1=0) is not negative, so the OLD local gate would legitimately
	// pass this exact scenario too — proving nothing about the bypass this
	// test exists to pin. At cache=0, cur+qtyDelta (0-1=-1) trips the old
	// gate for real; only the fix (ut-docs#404, ADR-0036) lets this sale
	// through, so a reverted bypass fails this test, not just the
	// dedicated bypass test below.
	replicaMux, replicaDp := newPOSTestDeps(t)
	if _, err := replicaDp.Db.ExecContext(ctx, `UPDATE inventory SET quantity=0 WHERE item_id='itm1'`); err != nil {
		t.Fatal(err)
	}
	if err := replicaDp.Settings.Set(ctx, "sync.primary_url", server.URL); err != nil {
		t.Fatal(err)
	}
	if err := replicaDp.Settings.Set(ctx, "sync.bearer", "token-abc"); err != nil {
		t.Fatal(err)
	}
	// A real enrolled replica carries a receipt prefix (ADR-0011) so its
	// receipts never collide with the primary's own sequence on replay.
	if err := replicaDp.Settings.Set(ctx, "sync.receipt_prefix", "T2-"); err != nil {
		t.Fatal(err)
	}

	logging.ResetRecent()

	// Till 1 (the primary) sells the last unit first — gate on, passes at
	// exactly zero.
	if _, err := pos.CompleteSale(ctx, primaryDp.Db, pos.SaleInput{
		SaleType: "sale", Currency: "GBP", TaxInclusive: false,
		CashierID: "user1", ActorID: "user1",
		Lines: []pos.SaleLineInput{{
			ItemID: "itm1", SKU: "ABC", Name: "Apple", Qty: 1,
			UnitPrice: money.FromMinor(100), TaxRateBasisPoints: 2000, LocationID: "loc_main",
		}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(120), Currency: "GBP"}},
	}); err != nil {
		t.Fatalf("till 1's sale of the last unit must succeed: %v", err)
	}

	// Till 2 (the replica) sells the same unit 21s later, before any sync
	// tick corrected its cache — must ALSO succeed, no error to the cashier.
	if _, err := replicaDp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	replicaMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("till 2's sale must succeed too, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			ReceiptNo string `json:"receiptNo"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Data.ReceiptNo == "" {
		t.Fatalf("expected a receipt from the replica sale, err=%v body=%s", err, rec.Body.String())
	}

	// The journal reaches the primary (here: one driven push tick).
	syncPushTick(ctx, replicaDp, &http.Client{Timeout: 5 * time.Second})

	var count int
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sales WHERE status='completed'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected both sales on the primary after the push, got %d", count)
	}
	var qty float64
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT quantity FROM inventory WHERE item_id='itm1'`).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != -1 {
		t.Fatalf("expected shop-wide stock at -1 after both sales, got %v", qty)
	}
	// Exactly one Problem, attributed to the replayed journal from this till.
	if n := recentMatches("Apple", "from till "+tillID); n != 1 {
		t.Fatalf("expected exactly one negative-stock Problem for till %s, got %d\nrecent: %+v", tillID, n, logging.Recent())
	}
}

// After a replica completes a local sale, one push attempt fires
// immediately (not at the next 30s tick) and NEVER delays the checkout
// response — proven against a primary that accepts the connection and then
// blocks.
func TestTenderHandler_ReplicaImmediatePushDoesNotBlockResponse(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	ctx := context.Background()

	hit := make(chan struct{}, 1)
	release := make(chan struct{})
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case hit <- struct{}{}:
		default:
		}
		<-release // hold the push in flight
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]int{"applied": 0, "skipped": 0}, "error": nil})
	}))

	if err := dp.Settings.Set(ctx, "sync.primary_url", primary.URL); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, "sync.bearer", "token-abc"); err != nil {
		t.Fatal(err)
	}

	loopCtx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	StartSyncPush(loopCtx, dp, &wg)
	t.Cleanup(func() {
		close(release) // unblock any in-flight push
		cancel()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("push loop did not drain after cancel")
		}
		primary.Close()
	})

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		done <- rec
	}()
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tender response blocked on the sync push — checkout must never wait on the network (ADR-0003)")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the sale completed, got %d: %s", rec.Code, rec.Body.String())
	}

	// The push attempt itself must have fired immediately (the loop's own
	// ticker would take 30s — far past this deadline).
	select {
	case <-hit:
	case <-time.After(5 * time.Second):
		t.Fatal("no immediate push attempt reached the primary after the replica's sale")
	}
}
