package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// newOrderStatusTestDeps wires the one-tap order-status surface against a
// real migrated SQLite DB (the sales columns + order_status_events journal
// come from migrations/033_order_status.sql) and a live broadcaster, same
// harness shape as kitchen_print_test.go.
func newOrderStatusTestDeps(t *testing.T) (*http.ServeMux, *common.Deps, *db.DB) {
	t.Helper()
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "orders.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })

	dp := &common.Deps{Db: dbase.DB, OrderStatus: pos.NewOrderStatusBroadcaster()}
	mux := http.NewServeMux()
	registerOrderStatus(mux, dp)
	return mux, dp, dbase
}

func seedOrderStatusTestSale(t *testing.T, dbase *db.DB, saleID, receiptNo string) {
	t.Helper()
	if _, err := dbase.DB.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES (?,?,'completed','sale','GBP',370,0,0,370,datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatalf("seed sale: %v", err)
	}
}

func postOrderStatus(mux *http.ServeMux, receiptNo, status string) *httptest.ResponseRecorder {
	form := url.Values{"status": {status}}
	req := httptest.NewRequest(http.MethodPost, "/api/orders/"+receiptNo+"/status", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// One tap marks the order preparing: 200, fragment carries the new status +
// who/when, the journal has the event, and subscribers hear about it.
func TestOrderStatusPost_AppliesForwardMove(t *testing.T) {
	mux, dp, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-1", "R-0001")

	ch, cancel := dp.OrderStatus.Subscribe()
	defer cancel()

	rec := postOrderStatus(mux, "R-0001", "preparing")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Preparing") {
		t.Fatalf("fragment must show the new status label, got %q", body)
	}
	// Who: no auth middleware in tests → actor "system", whose display name
	// ("System", seeded by migrations/003_system_user.sql) the fragment shows.
	if !strings.Contains(body, "System") {
		t.Fatalf("fragment must show who changed it, got %q", body)
	}

	var status string
	if err := dbase.DB.QueryRow(`SELECT order_status FROM sales WHERE receipt_no='R-0001'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "preparing" {
		t.Fatalf("stored status = %q, want preparing", status)
	}
	var events int
	if err := dbase.DB.QueryRow(`SELECT COUNT(*) FROM order_status_events WHERE receipt_no='R-0001'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("want 1 journal event, got %d", events)
	}

	select {
	case ev := <-ch:
		if ev.ReceiptNo != "R-0001" || ev.Status != "preparing" {
			t.Fatalf("broadcast = %+v, want R-0001/preparing", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("applied status change never broadcast")
	}
}

// A stale backward move (offline multi-till catch-up) is silently dropped:
// still 200 — NOT an error — the fragment shows the unchanged current
// status, nothing is journaled, nothing is broadcast.
func TestOrderStatusPost_StaleBackwardMoveSilentlyDropped(t *testing.T) {
	mux, dp, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-1", "R-0002")
	if rec := postOrderStatus(mux, "R-0002", "ready"); rec.Code != http.StatusOK {
		t.Fatalf("setup move: %d %q", rec.Code, rec.Body.String())
	}

	ch, cancel := dp.OrderStatus.Subscribe()
	defer cancel()

	rec := postOrderStatus(mux, "R-0002", "preparing")
	if rec.Code != http.StatusOK {
		t.Fatalf("stale move must not error: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "Ready") {
		t.Fatalf("fragment must keep showing the current status (Ready), got %q", body)
	}

	var status string
	if err := dbase.DB.QueryRow(`SELECT order_status FROM sales WHERE receipt_no='R-0002'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "ready" {
		t.Fatalf("status regressed to %q, must stay ready", status)
	}
	var events int
	if err := dbase.DB.QueryRow(`SELECT COUNT(*) FROM order_status_events WHERE receipt_no='R-0002'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("dropped write must not journal: want 1 event, got %d", events)
	}
	select {
	case ev := <-ch:
		t.Fatalf("dropped write must not broadcast, got %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestOrderStatusPost_CancelAllowedUntilCollected(t *testing.T) {
	mux, _, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-1", "R-0003")
	if rec := postOrderStatus(mux, "R-0003", "collected"); rec.Code != http.StatusOK {
		t.Fatalf("setup move: %d", rec.Code)
	}
	rec := postOrderStatus(mux, "R-0003", "cancelled")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel-after-collected is a silent no-op, not an error: got %d", rec.Code)
	}
	var status string
	if err := dbase.DB.QueryRow(`SELECT order_status FROM sales WHERE receipt_no='R-0003'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "collected" {
		t.Fatalf("collected order must not become cancelled, got %q", status)
	}
}

func TestOrderStatusPost_UnknownStatusRejected(t *testing.T) {
	mux, _, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-1", "R-0004")
	rec := postOrderStatus(mux, "R-0004", "bogus")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var events int
	if err := dbase.DB.QueryRow(`SELECT COUNT(*) FROM order_status_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("rejected input must not journal, got %d events", events)
	}
}

func TestOrderStatusPost_UnknownReceiptNotFound(t *testing.T) {
	mux, _, _ := newOrderStatusTestDeps(t)
	rec := postOrderStatus(mux, "R-NOPE", "preparing")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// The minimal placeholder list: recent orders with their current status and
// one-tap buttons posting to the status endpoint.
func TestOrdersListFragment_ShowsStatusAndButtons(t *testing.T) {
	mux, _, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-1", "R-0005")
	if rec := postOrderStatus(mux, "R-0005", "preparing"); rec.Code != http.StatusOK {
		t.Fatalf("setup move: %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/ui/orders", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "R-0005") {
		t.Fatalf("list must show the receipt, got %q", body)
	}
	if !strings.Contains(body, "Preparing") {
		t.Fatalf("list must show the current status label, got %q", body)
	}
	if !strings.Contains(body, `/api/orders/R-0005/status`) {
		t.Fatalf("list must carry one-tap buttons posting to the status endpoint, got %q", body)
	}
}

// ut-docs#517a: a sale whose latest kitchen/receipt print attempt failed
// must carry a visible warning in the orders list — a paid kiosk order must
// never be silently lost to an out-of-paper printer.
func TestOrdersListFragment_ShowsPrintFailureWarnings(t *testing.T) {
	mux, dp, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-1", "R-0006")
	seedOrderStatusTestSale(t, dbase, "sale-2", "R-0007")

	repo := data.NewPOSRepo(dp.Db)
	if err := repo.SetKitchenPrintFailed(t.Context(), "R-0006", "2026-08-12T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetReceiptPrintFailed(t.Context(), "R-0006", "2026-08-12T10:01:00Z"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/ui/orders", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Kitchen print failed") {
		t.Fatalf("list must warn about the failed kitchen print, got %q", body)
	}
	if !strings.Contains(body, "Receipt print failed") {
		t.Fatalf("list must warn about the failed receipt print, got %q", body)
	}
	// Exactly one order failed — the warning must not leak onto healthy rows.
	if got := strings.Count(body, "Kitchen print failed"); got != 2 { // span text + title attr
		t.Fatalf("kitchen warning rendered %d times, want 2 (one row's span text + title)", got)
	}
}

// The /orders page must actually POLL (ut-docs#517a) — the fragment swaps
// itself with outerHTML, so the polling trigger has to live on the
// fragment's own root (the pending_pairings.html pattern) or it would fire
// once on page load and never again.
func TestOrdersListFragment_RearmsPolling(t *testing.T) {
	mux, _, _ := newOrderStatusTestDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/ui/orders", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-get="/ui/orders"`) || !strings.Contains(body, `hx-trigger="every 15s"`) {
		t.Fatalf("fragment root must re-arm the 15s poll after each swap, got %q", body)
	}
}

func TestOrdersPage_Renders(t *testing.T) {
	mux, _, _ := newOrderStatusTestDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/orders", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `hx-get="/ui/orders`) {
		t.Fatalf("page must load the orders list fragment, got %q", rec.Body.String())
	}
}
