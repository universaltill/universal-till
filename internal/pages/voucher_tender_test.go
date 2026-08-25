package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// /api/pos/tender voucher wiring (ut-docs#1008), through the REAL handler +
// pos.CompleteSale: issue via the issue_vouchers field (voucher-only sale,
// empty basket), then redeem via a payment's voucher_id — asserting the
// liability rows land and the balance debits. Fixture shape mirrors
// TestPOSTenderSplitPayments (ui_smoke_test.go).
func newVoucherTenderDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, stubResolver{
		"ABC": {SKU: "ABC", Name: "Test Item", Qty: 1, PriceCents: 250, ItemID: "itm1", TaxRateBP: 2000},
	})

	setStore := settings.NewStore(db)
	state := common.LoadState(t.Context(), setStore, &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}})
	pm, err := plugins.Init(t.Context(), &config.Config{}, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	dp := &common.Deps{
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Engine:   engine,
		Pm:       pm,
		Settings: setStore,
	}
	t.Cleanup(dp.WaitForAsyncWork)

	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	registerVoucherAPI(mux, dp)
	return mux, dp
}

func postTenderJSON(t *testing.T, mux *http.ServeMux, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPOSTender_VoucherIssueAndRedeem(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)

	// 1. Voucher-only sale: empty basket, one 15.00 voucher, paid in cash.
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"cash","amount":1500}],"issue_vouchers":[{"amount":1500,"code":"GS-T1","holder_label":"Sample Holder"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("voucher-only tender failed: code %d body %s", rec.Code, rec.Body.String())
	}
	var balance int64
	var status string
	if err := dp.Db.QueryRow(`SELECT balance, status FROM vouchers WHERE id = 'GS-T1'`).Scan(&balance, &status); err != nil {
		t.Fatalf("voucher row after issue: %v", err)
	}
	if balance != 1500 || status != "active" {
		t.Fatalf("issued voucher balance=%d status=%q, want 1500/'active'", balance, status)
	}
	var subtotal, taxTotal, total int64
	if err := dp.Db.QueryRow(`SELECT subtotal, tax_total, total FROM sales`).Scan(&subtotal, &taxTotal, &total); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if subtotal != 0 || taxTotal != 0 || total != 1500 {
		t.Fatalf("voucher-only sale figures subtotal=%d tax=%d total=%d, want 0/0/1500", subtotal, taxTotal, total)
	}

	// 2. Redeem against real goods: 2.50 net + 20% exclusive = 3.00.
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	rec = postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-T1"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("redemption tender failed: code %d body %s", rec.Code, rec.Body.String())
	}
	if err := dp.Db.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-T1'`).Scan(&balance); err != nil {
		t.Fatalf("voucher row after redemption: %v", err)
	}
	if balance != 1200 {
		t.Fatalf("balance after 3.00 redemption = %d, want 1200", balance)
	}
	var redCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'GS-T1' AND type = 'redemption'`).Scan(&redCount); err != nil {
		t.Fatalf("count redemptions: %v", err)
	}
	if redCount != 1 {
		t.Fatalf("redemption rows = %d, want 1", redCount)
	}

	// 3. The balance is queryable through the API with the stable id.
	req := httptest.NewRequest(http.MethodGet, "/api/vouchers/GS-T1", nil)
	apiRec := httptest.NewRecorder()
	mux.ServeHTTP(apiRec, req)
	if apiRec.Code != http.StatusOK {
		t.Fatalf("GET /api/vouchers/GS-T1 = %d", apiRec.Code)
	}
	body := apiRec.Body.String()
	for _, want := range []string{`"id":"GS-T1"`, `"balance":1200`, `"holder_label":"Sample Holder"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("balance payload missing %s: %s", want, body)
		}
	}
}

func TestPOSTender_VoucherOverspendLocalizedRejection(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)

	rec := postTenderJSON(t, mux, `{"payments":[{"method":"cash","amount":100}],"issue_vouchers":[{"amount":100,"code":"GS-T2"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("issue tender failed: code %d body %s", rec.Code, rec.Body.String())
	}

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	// 3.00 due, voucher holds 1.00 -> refused in place: the handler renders
	// the basket back (200, same surface as the insufficient-stock
	// rejection) with the localized voucher toast, and nothing is debited.
	rec = postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-T2"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("overspend rejection should render the basket with a toast (200), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Voucher balance does not cover") {
		t.Fatalf("overspend rejection missing the localized voucher toast: %s", rec.Body.String())
	}
	var saleCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales WHERE tender_type = 'voucher'`).Scan(&saleCount); err != nil {
		t.Fatalf("count voucher sales: %v", err)
	}
	if saleCount != 0 {
		t.Fatalf("refused redemption persisted %d sale(s), want 0", saleCount)
	}
	var balance int64
	if err := dp.Db.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-T2'`).Scan(&balance); err != nil {
		t.Fatalf("voucher row: %v", err)
	}
	if balance != 100 {
		t.Fatalf("balance after refused overspend = %d, want 100", balance)
	}
}
