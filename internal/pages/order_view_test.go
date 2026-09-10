package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pos"
)

func TestOrderViewPage_UncollectedOrderShowsCollectButton(t *testing.T) {
	mux, _, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-1", "R-0001")
	repo := data.NewPOSRepo(dbase.DB)
	if _, _, err := repo.ApplyOrderStatus(context.Background(), "R-0001", pos.OrderStatusPreparing, "u-alice", "2026-09-08T10:00:00Z",
		func(current string) bool { return pos.OrderStatusAllowed(current, pos.OrderStatusPreparing) }); err != nil {
		t.Fatalf("seed order status: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/orders/R-0001", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-testid="collect-btn"`) {
		t.Fatalf("expected a Collect button, got: %s", body)
	}
	if !strings.Contains(body, `hx-post="/api/orders/R-0001/status"`) || !strings.Contains(body, `"status":"collected"`) {
		t.Fatalf("Collect button must post through the existing one-tap endpoint, got: %s", body)
	}
	// Review finding, ut-docs#1818: the button must target the status line
	// (matching orders_list.html's own pattern), not swap itself -- else
	// the header goes stale the moment the tap succeeds.
	if !strings.Contains(body, `hx-target="#order-status-line"`) {
		t.Fatalf("Collect button must update the status line, not itself, got: %s", body)
	}
	// Review finding, ut-docs#1818: data-status must carry the ORDER status
	// (preparing), not the sale's own status (always "completed" here) --
	// the two other templates using this attribute both carry order status.
	if !strings.Contains(body, `data-status="preparing"`) {
		t.Fatalf("expected data-status to carry the order status \"preparing\", got: %s", body)
	}
	if !strings.Contains(body, `href="/refund/R-0001"`) {
		t.Fatalf("expected a secondary refund link, got: %s", body)
	}
	if !strings.Contains(body, "R-0001") {
		t.Fatalf("expected the receipt number in the page, got: %s", body)
	}
}

// ut-docs#1964: the same one-tap endpoint order_view.html's Collect button
// posts to must also carry an OOB fragment for THIS page's own narrower
// (Collect-only) actions target, so the button disappears the instant the
// tap that applied it lands — not just on this page's own reload, and not
// by reusing the list's order-actions-{receipt} id (this page never offers
// Preparing/Ready/Cancel and must not suddenly grow them).
func TestOrderViewPage_CollectTap_HidesCollectButtonViaOOB(t *testing.T) {
	mux, _, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-1", "R-0023")

	// Review finding (ut-docs#1964): assert the PAGE half of the id contract
	// too, not just the server half below. An OOB swap only does anything if
	// the rendered page actually carries the id the fragment targets — drop
	// the <span id="order-collect-{receipt}"> wrapper from order_view.html
	// and every assertion below still passes while the feature is silently
	// dead in a real browser (the swap no-ops and the Collect button stays
	// forever). Verified by reverting exactly that wrapper.
	pageRec := httptest.NewRecorder()
	mux.ServeHTTP(pageRec, httptest.NewRequest(http.MethodGet, "/orders/R-0023", nil))
	if pageRec.Code != http.StatusOK {
		t.Fatalf("page status = %d, want 200 (body %q)", pageRec.Code, pageRec.Body.String())
	}
	pageSpan := extractByID(t, pageRec.Body.String(), "order-collect-R-0023")
	if !strings.Contains(pageSpan, `data-testid="collect-btn"`) {
		t.Fatalf("the page's order-collect-{receipt} element must wrap the Collect button (the OOB swap's target), got %q", pageSpan)
	}

	rec := postOrderStatus(mux, "R-0023", "collected")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	wantSpan := `id="order-collect-R-0023"`
	if !strings.Contains(body, wantSpan) {
		t.Fatalf("fragment must carry an OOB collect span, want %q in %q", wantSpan, body)
	}
	div := extractByID(t, body, "order-collect-R-0023")
	if strings.Contains(div, "<button") {
		t.Fatalf("once collected, the Collect button must be gone from the OOB fragment, got %q", div)
	}
	// The response also always carries the list's own order-actions-{receipt}
	// OOB div (every write emits both, regardless of which page posted —
	// htmx no-ops on whichever id the current page didn't render) — but it
	// must never carry buttons this page never offered in the first place.
	actionsDiv := extractByID(t, body, "order-actions-R-0023")
	if strings.Contains(actionsDiv, "<button") {
		t.Fatalf("order-actions- OOB div must stay empty for a terminal status, got %q", actionsDiv)
	}
}

func TestOrderViewPage_CollectedOrderRedirectsToRefund(t *testing.T) {
	mux, _, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-2", "R-0002")
	repo := data.NewPOSRepo(dbase.DB)
	if _, _, err := repo.ApplyOrderStatus(context.Background(), "R-0002", pos.OrderStatusCollected, "u-alice", "2026-09-08T10:00:00Z",
		func(current string) bool { return pos.OrderStatusAllowed(current, pos.OrderStatusCollected) }); err != nil {
		t.Fatalf("seed order status: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/orders/R-0002", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/refund/R-0002" {
		t.Fatalf("expected redirect to /refund/R-0002, got %q", loc)
	}
}

func TestOrderViewPage_CancelledOrderRedirectsToOrdersList(t *testing.T) {
	mux, _, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-3", "R-0003")
	repo := data.NewPOSRepo(dbase.DB)
	if _, _, err := repo.ApplyOrderStatus(context.Background(), "R-0003", pos.OrderStatusCancelled, "u-alice", "2026-09-08T10:00:00Z",
		func(current string) bool { return pos.OrderStatusAllowed(current, pos.OrderStatusCancelled) }); err != nil {
		t.Fatalf("seed order status: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/orders/R-0003", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/orders" {
		t.Fatalf("expected redirect to /orders, got %q", loc)
	}
}

// An unknown receipt has nothing to link to, so it goes to the /journal
// LIST -- not /journal/{receipt}, which mirrors /refund/{receipt}'s own
// not-found-vs-not-completed split (refund_page.go) rather than the dead
// end a bare 404 there would be (journal_page.go answers a raw
// http.NotFound for an unknown receipt).
func TestOrderViewPage_UnknownReceiptRedirectsToJournalList(t *testing.T) {
	mux, _, _ := newOrderStatusTestDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/orders/NO-SUCH-RECEIPT", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/journal" {
		t.Fatalf("expected redirect to /journal (the list), got %q", loc)
	}
}

func TestOrderViewPage_NotCompletedSaleRedirectsToJournal(t *testing.T) {
	mux, dp, _ := newOrderStatusTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-4','R-0004','open','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/orders/R-0004", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/journal/R-0004" {
		t.Fatalf("expected redirect to /journal/R-0004, got %q", loc)
	}
}
