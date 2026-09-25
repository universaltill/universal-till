package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ut-docs#2714: POST /api/self-order/checkout is anonymous and auth-exempt
// (ADR-0020). Before this card nothing stopped a double-tap (or a loop)
// from checking out the same basket twice at once -- two C-numbers, two
// held sales, two kitchen tickets for one order -- or one source hammering
// the endpoint.

// countCounterRows is the number of kiosk_counter_orders rows (every one
// is a C-number handed out).
func (h *counterHeldHarness) countCounterRows() int {
	h.t.Helper()
	var n int
	if err := h.db.DB.QueryRow(`SELECT COUNT(*) FROM kiosk_counter_orders`).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

// fillWalkUpBasket puts one cake in the shared walk-up kiosk basket.
func (h *counterHeldHarness) fillWalkUpBasket() {
	h.t.Helper()
	if rec := h.post("/api/self-order/scan", "code=5000002"); rec.Code != http.StatusOK {
		h.t.Fatalf("scan cake = %d: %s", rec.Code, rec.Body.String())
	}
}

// blockingPrimary is a fake main till whose FIRST held-sale upsert blocks
// until release is closed (signalling entered once it is inside), so a
// test can hold one checkout mid-flight; every later call answers at once.
// It answers "applied" like newHeldSaleProxyPrimary.
type blockingPrimary struct {
	srv     *httptest.Server
	calls   atomic.Int64
	entered chan struct{}
	release chan struct{}
}

func newBlockingPrimary(t *testing.T) *blockingPrimary {
	t.Helper()
	p := &blockingPrimary{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sync/held-sales/upsert" {
			http.NotFound(w, r)
			return
		}
		if p.calls.Add(1) == 1 {
			once.Do(func() { close(p.entered) })
			select {
			case <-p.release:
			case <-time.After(5 * time.Second):
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"applied":true,"updated_at":"2026-09-25 10:00:00","created_at":"2026-09-25 10:00:00"},"error":null}`))
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// A second checkout of the SAME basket while the first is still in flight
// is refused with 409 and does nothing: no second C-number, no second
// held sale, no second call to the main till.
func TestSelfOrderCheckout_ConcurrentSameBasketIsRefused(t *testing.T) {
	h := newCounterHeldHarness(t)
	primary := newBlockingPrimary(t)
	// The till's held-sale write-through client has an 800ms budget; widen
	// it so the first checkout reliably stays inside the fake main till
	// while the second one arrives.
	prev := heldSaleProxyClient
	heldSaleProxyClient = &http.Client{Timeout: 10 * time.Second}
	t.Cleanup(func() { heldSaleProxyClient = prev })
	setReplicaSettings(t, h.dp.Settings, primary.srv.URL, "tok")
	h.fillWalkUpBasket()

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- h.post("/api/self-order/checkout", "") }()
	select {
	case <-primary.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first checkout never reached the main till")
	}

	second := h.post("/api/self-order/checkout", "")
	close(primary.release)
	if second.Code != http.StatusConflict {
		t.Fatalf("second concurrent checkout = %d, want 409: %s", second.Code, second.Body.String())
	}
	assertKioskRefusalPaintsNothing(t, second)
	rec := <-first
	if rec.Code != http.StatusOK {
		t.Fatalf("first checkout = %d: %s", rec.Code, rec.Body.String())
	}
	if n := h.countCounterRows(); n != 1 {
		t.Fatalf("kiosk_counter_orders rows = %d, want 1 (one C-number)", n)
	}
	if got := primary.calls.Load(); got != 1 {
		t.Fatalf("main till upsert calls = %d, want 1", got)
	}
	if n := len(h.heldSales()); n != 1 {
		t.Fatalf("held sales = %d, want 1", n)
	}

	// The guard is released on return: the next basket checks out fine.
	h.fillWalkUpBasket()
	if rec := h.post("/api/self-order/checkout", ""); rec.Code != http.StatusOK {
		t.Fatalf("checkout after the first finished = %d: %s", rec.Code, rec.Body.String())
	}
}

// The in-flight guard is per basket: a table-QR guest checking out while
// the walk-up kiosk's checkout is still in flight is not blocked.
func TestSelfOrderCheckout_InFlightGuardIsPerBasket(t *testing.T) {
	h := newCounterHeldHarness(t)
	primary := newBlockingPrimary(t)
	prev := heldSaleProxyClient
	heldSaleProxyClient = &http.Client{Timeout: 10 * time.Second}
	t.Cleanup(func() { heldSaleProxyClient = prev })
	setReplicaSettings(t, h.dp.Settings, primary.srv.URL, "tok")
	h.fillWalkUpBasket()

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- h.post("/api/self-order/checkout", "") }()
	select {
	case <-primary.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("walk-up checkout never reached the main till")
	}
	rec, _ := h.tableOrder() // fatals unless the table checkout returns 200
	close(primary.release)
	if !strings.Contains(rec.Body.String(), "C-") {
		t.Fatalf("table checkout confirmation carries no C-number: %s", rec.Body.String())
	}
	if rec := <-first; rec.Code != http.StatusOK {
		t.Fatalf("walk-up checkout = %d: %s", rec.Code, rec.Body.String())
	}
}

// One source may POST checkout at most 20 times a minute; past that it gets
// 429 before any work -- even with a full basket nothing is written. Another
// source is unaffected.
func TestSelfOrderCheckout_RateLimitedPerSource(t *testing.T) {
	h := newCounterHeldHarness(t)
	for i := 0; i < 20; i++ {
		// Empty basket: a cheap 400, but it still counts as a hit.
		if rec := h.post("/api/self-order/checkout", ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("checkout %d = %d, want 400 (empty basket)", i+1, rec.Code)
		}
	}
	h.fillWalkUpBasket()
	rec := h.post("/api/self-order/checkout", "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("21st checkout from one source = %d, want 429: %s", rec.Code, rec.Body.String())
	}
	assertKioskRefusalPaintsNothing(t, rec)
	if n := h.countCounterRows(); n != 0 {
		t.Fatalf("a rate-limited checkout wrote %d counter rows", n)
	}
	if n := len(h.heldSales()); n != 0 {
		t.Fatalf("a rate-limited checkout parked %d held sales", n)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/self-order/checkout", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.77:4321"
	other := httptest.NewRecorder()
	h.mux.ServeHTTP(other, req)
	if other.Code != http.StatusOK {
		t.Fatalf("checkout from another source = %d, want 200: %s", other.Code, other.Body.String())
	}
}

// When parking the order as a held sale fails, the "held" counter row just
// created is deleted: no invisible orphan, and the retry gets the SAME
// C-number rather than burning one.
func TestCounterCheckout_ParkFailureLeavesNoOrphanAndReusesTheNumber(t *testing.T) {
	h := newCounterHeldHarness(t)
	if _, err := h.db.DB.Exec(`CREATE TRIGGER test_block_held BEFORE INSERT ON held_sales BEGIN SELECT RAISE(ABORT, 'test: held_sales unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	h.fillWalkUpBasket()
	if rec := h.post("/api/self-order/checkout", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("checkout with parking broken = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if n := h.countCounterRows(); n != 0 {
		t.Fatalf("a failed park left %d orphaned kiosk_counter_orders rows", n)
	}

	if _, err := h.db.DB.Exec(`DROP TRIGGER test_block_held`); err != nil {
		t.Fatal(err)
	}
	rec := h.post("/api/self-order/checkout", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("retry = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "C-1") {
		t.Fatalf("retry must reuse C-1: %s", rec.Body.String())
	}
	if held := h.onlyHeld(); !strings.HasPrefix(held.Label, "C-1 ") {
		t.Fatalf("held label = %q, want C-1", held.Label)
	}
}

// assertKioskRefusalPaintsNothing: the kiosk page force-swaps every 4xx
// from /api/self-order/* into its modal (self_order_shop.html), so a guard
// refusal must tell htmx not to swap and carry no (untranslated) body --
// otherwise a customer sees raw English, or a late 409 blanks the first
// request's confirmation.
func assertKioskRefusalPaintsNothing(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("HX-Reswap"); got != "none" {
		t.Errorf("HX-Reswap = %q, want none", got)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "" {
		t.Errorf("refusal body = %q, want empty", body)
	}
}
