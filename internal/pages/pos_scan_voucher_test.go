package pages

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
)

// Scan-to-redeem (ut-docs#1833): scanning a Gutschein barcode at the sale
// screen resolves the voucher and stashes it as a pending tender on the
// engine, with three distinct toasts covering (a) found+usable, (b) found
// but not usable (zero balance / not active), (c) not found anywhere (local
// miss + cross-till primary miss). Reuses setupScanBarcodeDeps
// (pos_scan_barcode_test.go) -- the same real /api/pos/scan wiring, just
// seeding a vouchers row instead of a catalog item.

func TestScanAPI_VoucherFoundActive_SetsPendingVoucherAndSuccessToast(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	repo := data.NewPOSRepo(d.DB)
	if err := repo.CreateVoucher(t.Context(), nil, data.Voucher{
		ID: "GS-1234", OriginalAmountMinor: 1500, BalanceMinor: 1500, Currency: "EUR",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}

	rec := postScanCode(t, mux, "GS-1234")
	if rec.Code != http.StatusOK {
		t.Fatalf("scan: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if got := dp.Engine.Basket().VoucherID; got != "GS-1234" {
		t.Fatalf("PendingVoucherID = %q, want GS-1234", got)
	}
	if got := dp.Engine.Basket().VoucherBalance; got != money.FromMinor(1500) {
		t.Fatalf("PendingVoucherBalance = %v, want 1500", got)
	}

	body := rec.Body.String()
	wantMsg := fmt.Sprintf(httpx.T("en", "pos.toast.voucher_scan_found"), "GS-1234", money.FromMinor(1500).String())
	if !strings.Contains(body, wantMsg) {
		t.Fatalf("expected success toast %q in response, got: %s", wantMsg, body)
	}
	if !strings.Contains(body, `id="toast-message"`) || strings.Contains(body, `class="pos-notice error"`) {
		t.Fatalf("expected a non-error toast, got: %s", body)
	}
}

func TestScanAPI_VoucherZeroBalance_NotUsableToastAndNoPendingVoucher(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	repo := data.NewPOSRepo(d.DB)
	if err := repo.CreateVoucher(t.Context(), nil, data.Voucher{
		ID: "GS-EMPTY", OriginalAmountMinor: 1000, BalanceMinor: 0, Currency: "EUR",
		Status: "redeemed", CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}

	rec := postScanCode(t, mux, "GS-EMPTY")
	if rec.Code != http.StatusOK {
		t.Fatalf("scan: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if got := dp.Engine.Basket().VoucherID; got != "" {
		t.Fatalf("PendingVoucherID = %q, want empty (voucher not usable)", got)
	}

	body := rec.Body.String()
	wantMsg := html.EscapeString(fmt.Sprintf(httpx.T("en", "pos.toast.voucher_scan_unusable"), "GS-EMPTY"))
	if !strings.Contains(body, wantMsg) {
		t.Fatalf("expected not-usable toast %q in response, got: %s", wantMsg, body)
	}
	if !strings.Contains(body, `class="pos-notice error"`) {
		t.Fatalf("expected an error-level toast, got: %s", body)
	}
}

func TestScanAPI_VoucherInactiveStatus_NotUsableToast(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	repo := data.NewPOSRepo(d.DB)
	if err := repo.CreateVoucher(t.Context(), nil, data.Voucher{
		ID: "GS-VOID", OriginalAmountMinor: 1000, BalanceMinor: 1000, Currency: "EUR",
		Status: "void", CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}

	rec := postScanCode(t, mux, "GS-VOID")
	if rec.Code != http.StatusOK {
		t.Fatalf("scan: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().VoucherID; got != "" {
		t.Fatalf("PendingVoucherID = %q, want empty (void voucher)", got)
	}
	wantMsg := html.EscapeString(fmt.Sprintf(httpx.T("en", "pos.toast.voucher_scan_unusable"), "GS-VOID"))
	if !strings.Contains(rec.Body.String(), wantMsg) {
		t.Fatalf("expected not-usable toast %q in response, got: %s", wantMsg, rec.Body.String())
	}
}

func TestScanAPI_VoucherNotFound_NotFoundToastAndNoFallthrough(t *testing.T) {
	mux, dp, _ := setupScanBarcodeDeps(t)

	rec := postScanCode(t, mux, "GS-NOPE")
	if rec.Code != http.StatusOK {
		t.Fatalf("scan: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().VoucherID; got != "" {
		t.Fatalf("PendingVoucherID = %q, want empty", got)
	}

	body := rec.Body.String()
	wantMsg := fmt.Sprintf(httpx.T("en", "pos.toast.voucher_scan_not_found"), "GS-NOPE")
	if !strings.Contains(body, wantMsg) {
		t.Fatalf("expected not-found toast %q in response, got: %s", wantMsg, body)
	}
	// Must never fall through to the generic item-not-found toast, the
	// promo-applied toast, or a scan-to-refund redirect.
	if strings.Contains(body, httpx.T("en", "pos.toast.item_not_found")) {
		t.Fatalf("voucher-shaped code must not fall through to item_not_found: %s", body)
	}
	if rec.Header().Get("HX-Redirect") != "" {
		t.Fatalf("voucher-shaped code must not fall through to scan-to-refund, got redirect %q", rec.Header().Get("HX-Redirect"))
	}
	if len(dp.Engine.Basket().Lines) != 0 {
		t.Fatalf("no basket line may be added for a voucher-shaped code")
	}
}

// TestScanAPI_VoucherCrossTillFallback_ResolvesFromPrimary (ut-docs#1668):
// a voucher this till has never locally seen (issued at a different till)
// still resolves on a REPLICA with a reachable primary, via the exact same
// fetchVoucherFromPrimary path GET /api/vouchers/{id} already uses -- same
// fake-primary shape as tables_claim_proxy_test.go / order_status_proxy_test.go.
func TestScanAPI_VoucherCrossTillFallback_ResolvesFromPrimary(t *testing.T) {
	mux, dp, _ := setupScanBarcodeDeps(t)

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/sync/vouchers/GS-REMOTE" {
			t.Errorf("unexpected primary call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"id":"GS-REMOTE","holder_label":"","original_amount":3000,"balance":3000,"currency":"EUR","voucher_type":"multi_purpose","status":"active","issued_sale_id":"","created_at":"2026-09-01T10:00:00Z"},"error":null}`)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	rec := postScanCode(t, mux, "GS-REMOTE")
	if rec.Code != http.StatusOK {
		t.Fatalf("scan: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().VoucherID; got != "GS-REMOTE" {
		t.Fatalf("PendingVoucherID = %q, want GS-REMOTE (resolved from primary)", got)
	}
	if got := dp.Engine.Basket().VoucherBalance; got != money.FromMinor(3000) {
		t.Fatalf("PendingVoucherBalance = %v, want 3000", got)
	}
	wantMsg := fmt.Sprintf(httpx.T("en", "pos.toast.voucher_scan_found"), "GS-REMOTE", money.FromMinor(3000).String())
	if !strings.Contains(rec.Body.String(), wantMsg) {
		t.Fatalf("expected success toast %q in response, got: %s", wantMsg, rec.Body.String())
	}
}
