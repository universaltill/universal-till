package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#4206: order tracking is a Shop-and-up feature (owner decision):
// the kiosk confirmation shows the tracking QR only while this till's
// cached entitlement allows cloud_sync, on BOTH checkout paths. A
// pay-at-counter order is a held sale until the cashier tenders it, so its
// token rides in the held payload first and moves to the sale on payment;
// /o/{token} shows "waiting for payment" until then.

// seedTrackingPlan caches a fresh, active entitlement block for plan.
func seedTrackingPlan(t *testing.T, dp *common.Deps, plan entitlement.Plan) {
	t.Helper()
	seedRetentionEntitlement(t, dp, string(plan), entitlement.StatusActive, time.Now())
}

func heldPayloadSnapshot(t *testing.T, h *counterHeldHarness) pos.BasketSnapshot {
	t.Helper()
	held := h.onlyHeld()
	var snap pos.BasketSnapshot
	if err := json.Unmarshal([]byte(held.Payload), &snap); err != nil {
		t.Fatalf("payload is not a BasketSnapshot: %v", err)
	}
	return snap
}

func TestCounterCheckout_ShopPlanShowsTrackingQRAndParksToken(t *testing.T) {
	h := newCounterHeldHarness(t)
	seedTrackingPlan(t, h.dp, entitlement.PlanShop)

	rec := h.kioskOrder()
	body := rec.Body.String()
	snap := heldPayloadSnapshot(t, h)
	if len(snap.TrackingToken) != 32 {
		t.Fatalf("held payload must carry a 32-char tracking token, got %q", snap.TrackingToken)
	}
	if !strings.Contains(h.onlyHeld().Payload, `"tracking_token":"`+snap.TrackingToken+`"`) {
		t.Fatalf("payload JSON must carry tracking_token: %s", h.onlyHeld().Payload)
	}
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Fatalf("counter confirmation should embed the tracking QR: %s", body)
	}
	if !strings.Contains(body, "http://example.com/o/"+snap.TrackingToken+"?lang=en") {
		t.Fatalf("counter confirmation should show the tracking URL for %s: %s", snap.TrackingToken, body)
	}
	// Still no sale: the token waits in the held order.
	var n int
	_ = h.db.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&n)
	if n != 0 {
		t.Fatalf("a pay-at-counter order must not create a sale, got %d", n)
	}
}

func TestCounterCheckout_LocalPlanShowsNoQRAndMintsNoToken(t *testing.T) {
	h := newCounterHeldHarness(t)
	// No cached entitlement at all = Local.
	rec := h.kioskOrder()
	if strings.Contains(rec.Body.String(), "data:image/png;base64,") || strings.Contains(rec.Body.String(), "/o/") {
		t.Fatalf("a Local till must not show a tracking QR: %s", rec.Body.String())
	}
	if snap := heldPayloadSnapshot(t, h); snap.TrackingToken != "" {
		t.Fatalf("a Local till must not mint a tracking token, got %q", snap.TrackingToken)
	}
	if strings.Contains(h.onlyHeld().Payload, "tracking_token") {
		t.Fatalf("payload must omit tracking_token on Local: %s", h.onlyHeld().Payload)
	}
}

// A lapsed Shop subscription is Local for this feature too (fail closed).
func TestCounterCheckout_LapsedPlanShowsNoQR(t *testing.T) {
	h := newCounterHeldHarness(t)
	seedRetentionEntitlement(t, h.dp, string(entitlement.PlanShop), entitlement.StatusLapsed, time.Now())
	rec := h.kioskOrder()
	if strings.Contains(rec.Body.String(), "data:image/png;base64,") {
		t.Fatalf("a lapsed plan must not show a tracking QR: %s", rec.Body.String())
	}
	if snap := heldPayloadSnapshot(t, h); snap.TrackingToken != "" {
		t.Fatalf("a lapsed plan must not mint a tracking token, got %q", snap.TrackingToken)
	}
}

// Pay-at-kiosk on a Local till: no QR, and no token minted on the sale.
func TestSelfOrderShop_CheckoutOnLocalPlanHasNoTrackingQR(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)
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
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Order placed") {
		t.Fatalf("checkout = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "data:image/png;base64,") || strings.Contains(rec.Body.String(), "/o/") {
		t.Fatalf("a Local till must not show a tracking QR: %s", rec.Body.String())
	}
	var token string
	if err := d.DB.QueryRow(`SELECT COALESCE(tracking_token,'') FROM sales WHERE status='completed'`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if token != "" {
		t.Fatalf("a Local till must not mint a tracking token, got %q", token)
	}
}

// The full journey: the customer's link shows "waiting for payment" while
// the order is parked, the cashier resumes and tenders it, the sale adopts
// the token, and the same link now shows the sale's live status.
func TestCounterCheckout_TrackingLinkFollowsTheOrderThroughPayment(t *testing.T) {
	h := newCounterHeldHarness(t)
	seedTrackingPlan(t, h.dp, entitlement.PlanShop)
	registerOrderTracking(h.mux, h.dp)

	h.kioskOrder()
	held := h.onlyHeld()
	tok := heldPayloadSnapshot(t, h).TrackingToken

	page := h.get("/o/" + tok)
	if page.Code != http.StatusOK {
		t.Fatalf("/o/{token} for a parked order: want 200, got %d: %s", page.Code, page.Body.String())
	}
	body := page.Body.String()
	for _, want := range []string{
		"C-1",
		"Waiting for payment at the counter",
		`data-status="awaiting_payment"`,
		`hx-get="/o/` + tok + `/status?w=1"`,
		`hx-trigger="every 15s"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("parked-order page missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, held.ID) {
		t.Fatalf("the held order's internal id must not leak: %s", body)
	}
	frag := h.get("/o/" + tok + "/status")
	if frag.Code != http.StatusOK || !strings.Contains(frag.Body.String(), "Waiting for payment at the counter") ||
		!strings.Contains(frag.Body.String(), `hx-trigger="every 15s"`) {
		t.Fatalf("poll fragment for a parked order must show waiting-for-payment and keep polling: %d %s", frag.Code, frag.Body.String())
	}

	if rec := h.post("/api/pos/resume", "id="+held.ID); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d: %s", rec.Code, rec.Body.String())
	}
	// Resumed but not yet paid: neither a held row nor a sale holds the
	// token. A page that was showing "waiting for payment" keeps that state
	// and keeps polling (bounded), instead of declaring the link expired.
	grace := h.get("/o/" + tok + "/status?w=1")
	if gb := grace.Body.String(); grace.Code != http.StatusOK || !strings.Contains(gb, "Waiting for payment at the counter") ||
		!strings.Contains(gb, `/status?w=2"`) || strings.Contains(gb, "This tracking link isn") {
		t.Fatalf("resumed-not-paid poll must keep waiting: %d %s", grace.Code, gb)
	}
	if plain := h.get("/o/" + tok + "/status").Body.String(); !strings.Contains(plain, "This tracking link isn") {
		t.Fatalf("without the wait counter the not-found answer is unchanged: %s", plain)
	}
	if last := h.get("/o/" + tok + "/status?w=" + strconv.Itoa(orderTrackingResumeMaxPolls)).Body.String(); !strings.Contains(last, "This tracking link isn") || strings.Contains(last, "hx-trigger") {
		t.Fatalf("past the grace bound the page must expire and stop polling: %s", last)
	}

	if rec := h.tenderCash(); rec.Code != http.StatusOK {
		t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
	}
	h.dp.WaitForAsyncWork()

	var saleTok, displayNo string
	if err := h.db.DB.QueryRow(`SELECT COALESCE(tracking_token,''), display_no FROM sales`).Scan(&saleTok, &displayNo); err != nil {
		t.Fatalf("want exactly one sale: %v", err)
	}
	if saleTok != tok {
		t.Fatalf("paid sale tracking_token = %q, want the held order's %q", saleTok, tok)
	}
	page = h.get("/o/" + tok)
	if page.Code != http.StatusOK {
		t.Fatalf("/o/{token} after payment: want 200, got %d", page.Code)
	}
	body = page.Body.String()
	if !strings.Contains(body, "C-1") || !strings.Contains(body, "Not started") || strings.Contains(body, "Waiting for payment") {
		t.Fatalf("after payment the link must show the sale's own status: %s", body)
	}
	if h.dp.Engine.OrderTrackingToken() != "" {
		t.Fatal("the next sale must not carry the paid order's tracking token")
	}
}

// Re-parking a resumed order keeps its token, so the link keeps working.
func TestCounterCheckout_ReparkKeepsTrackingToken(t *testing.T) {
	h := newCounterHeldHarness(t)
	seedTrackingPlan(t, h.dp, entitlement.PlanShop)
	registerOrderTracking(h.mux, h.dp)

	h.kioskOrder()
	held := h.onlyHeld()
	tok := heldPayloadSnapshot(t, h).TrackingToken
	if rec := h.post("/api/pos/resume", "id="+held.ID); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := h.post("/api/pos/hold", ""); rec.Code != http.StatusOK {
		t.Fatalf("re-park = %d: %s", rec.Code, rec.Body.String())
	}
	if got := heldPayloadSnapshot(t, h).TrackingToken; got != tok {
		t.Fatalf("re-parked payload token = %q, want %q", got, tok)
	}
	if page := h.get("/o/" + tok); page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Waiting for payment") {
		t.Fatalf("re-parked order's link must still resolve: %d %s", page.Code, page.Body.String())
	}
}
