package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pos"
)

// TestScanHandler_ReceiptRouting is ut-docs#1818: where a scanned receipt
// barcode lands now depends on the order's own lifecycle status, not just
// on whether the receipt exists.
func TestScanHandler_ReceiptRouting(t *testing.T) {
	tests := []struct {
		name         string
		saleStatus   string
		orderStatus  string // "" = never tracked
		wantRedirect string
		wantToastKey string
	}{
		{
			name:         "uncollected_tracked_order_opens_the_order_view",
			saleStatus:   "completed",
			orderStatus:  pos.OrderStatusPreparing,
			wantRedirect: "/orders/",
		},
		{
			name:         "collected_order_still_opens_refund",
			saleStatus:   "completed",
			orderStatus:  pos.OrderStatusCollected,
			wantRedirect: "/refund/",
		},
		{
			name:         "never_tracked_completed_sale_opens_refund",
			saleStatus:   "completed",
			orderStatus:  "",
			wantRedirect: "/refund/",
		},
		{
			name:         "cancelled_order_says_so_plainly",
			saleStatus:   "completed",
			orderStatus:  pos.OrderStatusCancelled,
			wantToastKey: "This order was cancelled",
		},
		{
			name:         "sale_that_never_completed_says_so_plainly",
			saleStatus:   "open",
			orderStatus:  "",
			wantToastKey: "This sale is not completed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux, dp := newPOSTestDeps(t)
			receiptNo := "R-SCAN-" + tc.name
			if _, err := dp.Db.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
VALUES (?, ?, ?, 'sale', 'GBP', 100, 0, 0, 100, datetime('now'))`, "sale-"+receiptNo, receiptNo, tc.saleStatus); err != nil {
				t.Fatalf("seed sale: %v", err)
			}
			if tc.orderStatus != "" {
				repo := data.NewPOSRepo(dp.Db)
				applied, found, err := repo.ApplyOrderStatus(context.Background(), receiptNo, tc.orderStatus, "u-test", "2026-09-08T10:00:00Z",
					func(current string) bool { return pos.OrderStatusAllowed(current, tc.orderStatus) })
				if err != nil || !found || !applied {
					t.Fatalf("seed order status: applied=%v found=%v err=%v", applied, found, err)
				}
			}

			rec := posPostForm(mux, "/api/pos/scan", "code="+receiptNo)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}

			redirect := rec.Header().Get("HX-Redirect")
			if tc.wantRedirect != "" {
				if !strings.HasPrefix(redirect, tc.wantRedirect) || !strings.HasSuffix(redirect, receiptNo) {
					t.Fatalf("expected HX-Redirect prefix %q with receipt %q, got %q", tc.wantRedirect, receiptNo, redirect)
				}
				return
			}
			if redirect != "" {
				t.Fatalf("expected no redirect, got HX-Redirect %q", redirect)
			}
			if !strings.Contains(rec.Body.String(), tc.wantToastKey) {
				t.Fatalf("expected toast %q in response, got: %s", tc.wantToastKey, rec.Body.String())
			}
		})
	}
}

// TestScanHandler_ReceiptRouting_ReplicaUsesPrimaryOrderStatus is a review
// finding (ut-docs#1818): order_status_events is deliberately NOT part of
// the LAN-sync journal (sync_admin_repo.go's exclusion list), so on a real
// replica the local table this handler used to read straight from is
// always empty regardless of the order's real status -- an earlier draft
// of this fix silently fell back to /refund/{receipt} for every order on
// every replica, exactly the dangerous case #1818 exists to prevent. This
// seeds the sale LOCALLY (so ReceiptExists/GetSaleDetail succeed, as they
// would on a real replica that took this sale itself) but applies the
// order-status change ONLY on a fake primary, never locally -- proving the
// routing decision actually comes from the primary proxy, not a
// local-table read that happens to be right for another reason.
func TestScanHandler_ReceiptRouting_ReplicaUsesPrimaryOrderStatus(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	receiptNo := "R-REPLICA-1"
	if _, err := dp.Db.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
VALUES ('sale-replica-1', ?, 'completed', 'sale', 'GBP', 100, 0, 0, 100, datetime('now'))`, receiptNo); err != nil {
		t.Fatalf("seed sale: %v", err)
	}
	// Deliberately NOT calling repo.ApplyOrderStatus here -- the local
	// order_status_events table for this receipt stays empty, as it would
	// on a real replica that never wrote this event itself.

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"receipt_no":%q,"order_type":"","status":"ready","status_updated_at":"2026-09-08T10:00:00Z","created_at":"2026-09-08T09:55:00Z","kitchen_print_failed_at":"","receipt_print_failed_at":""}],"error":null}`, receiptNo)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-test")

	rec := posPostForm(mux, "/api/pos/scan", "code="+receiptNo)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	want := "/orders/" + receiptNo
	if got := rec.Header().Get("HX-Redirect"); got != want {
		t.Fatalf("replica must route by the PRIMARY's order status, not its own empty local table: want redirect %q, got %q", want, got)
	}
}

// TestScanHandler_UnknownCodeMissPathUnchanged is ut-docs#1818's explicit
// "no behaviour change on the miss path" acceptance criterion: a code that
// isn't a receipt, a product, a promo or a customer barcode must still hit
// the plain "item not found" toast.
func TestScanHandler_UnknownCodeMissPathUnchanged(t *testing.T) {
	mux, _ := newPOSTestDeps(t)
	rec := posPostForm(mux, "/api/pos/scan", "code=NO-SUCH-CODE-AT-ALL")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Redirect") != "" {
		t.Fatalf("expected no redirect for an unknown code, got %q", rec.Header().Get("HX-Redirect"))
	}
	if !strings.Contains(rec.Body.String(), "Item not found") {
		t.Fatalf("expected the unchanged item-not-found toast, got: %s", rec.Body.String())
	}
}
