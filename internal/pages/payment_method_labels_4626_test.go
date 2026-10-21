package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#4626: wherever a payment's method id is shown to a person (on-screen
// receipt, journal detail, reports payments tab) a built-in id reads as its
// translated name; a plugin's own method id is left as-is.

func TestOnScreenReceipt_PaymentMethodTranslated(t *testing.T) {
	chdirRoot(t)
	lines := []pos.SaleLineInput{{Name: "Apple", Qty: 1, UnitPrice: 1000}}
	payments := []pos.PaymentInput{
		{MethodID: "cash", Amount: 500},
		{MethodID: "gift", Amount: 300},
		{MethodID: "sumup", Amount: 200},
	}
	html, err := renderReceipt(httpx.FuncsFor("fa"), "R1", lines, payments, 1000, 0, 1000, false, 0, "amount", 0, nil, false, false, false, false, nil, nil, "My Store", receiptDesign{ShowTax: true}, "", nil, "", nil, nil, "")
	if err != nil {
		t.Fatalf("renderReceipt: %v", err)
	}
	for _, want := range []string{"<strong>نقدی</strong>", "<strong>کارت هدیه</strong>", "<strong>sumup</strong>"} {
		if !strings.Contains(html, want) {
			t.Errorf("receipt missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "<strong>cash</strong>") || strings.Contains(html, "<strong>gift</strong>") {
		t.Errorf("raw built-in id leaked onto the receipt:\n%s", html)
	}
}

func TestJournalDetail_PaymentMethodTranslated(t *testing.T) {
	mux, d := newJournalMux(t)
	seedJournalPageSale(t, d, "sale-pm", "R-PM-1", "sale")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/journal/R-PM-1?lang=fa", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/journal/R-PM-1 = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<strong>نقدی</strong>") {
		t.Errorf("journal detail payment must read the fa cash label:\n%s", body)
	}
	if strings.Contains(body, "<strong>cash</strong>") {
		t.Errorf("raw id leaked in journal detail payments")
	}
}

func TestReportsPaymentsTab_MethodTranslated(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO sales(id, receipt_no, status, sale_type, tender_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
		VALUES ('s-rp', 'R-RP', 'completed', 'sale', 'cash', 'GBP', 300, 0, 0, 300, datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO payments(id, sale_id, method_id, amount, currency, paid_at)
		VALUES ('s-rp-p', 's-rp', 'cash', 300, 'GBP', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec := getReportsTab(t, mux, "payments", "?lang=fa")
	if rec.Code != http.StatusOK {
		t.Fatalf("payments tab = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<strong>نقدی</strong>") {
		t.Errorf("payments tab must show the fa cash label:\n%s", body)
	}
	if strings.Contains(body, "<strong>cash</strong>") {
		t.Errorf("raw id leaked in payments tab")
	}
}
