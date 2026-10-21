package pages

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#4714: the receipt screen's Print button (manual print, or the
// "ask" prompt) hands a self-order customer's language pick (ut-docs#4705)
// to POST /api/print/receipt/{no}, so a manual print matches the auto-print.
// The journal's reprint sends nothing and stays the shop's copy.

// manualPrintDevice points the receipt printer at a Windows-1254 device
// file (it renders both the shop's English and Turkish) and returns it.
func manualPrintDevice(t *testing.T, dp *common.Deps) string {
	t.Helper()
	dev := filepath.Join(t.TempDir(), "receipt-printer")
	if err := os.WriteFile(dev, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.SetMany(context.Background(), map[string]string{
		keyPrinterMode:    "device",
		keyPrinterDevice:  dev,
		keyPrinterCharset: "win1254",
	}); err != nil {
		t.Fatal(err)
	}
	return dev
}

// manualPrint POSTs the print endpoint with form body and returns the
// status plus the bytes the printer received.
func manualPrint(t *testing.T, form string) (int, []byte, *common.Deps) {
	t.Helper()
	mux, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "s-4714", "R-4714", "sale", "", 120, 0, 0)
	pinDefaultLocale(t, "en")
	dev := manualPrintDevice(t, dp)
	var req *http.Request
	if form == "" {
		req = httptest.NewRequest(http.MethodPost, "/api/print/receipt/R-4714", nil)
	} else {
		req = httptest.NewRequest(http.MethodPost, "/api/print/receipt/R-4714", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	raw, _ := os.ReadFile(dev)
	return rec.Code, raw, dp
}

func TestManualPrint_CustomerLocalePrintsInCustomersLanguage(t *testing.T) {
	code, raw, dp := manualPrint(t, "customer_locale=tr")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	fis, _ := charmap.Windows1254.NewEncoder().String("Fiş ")
	if !bytes.Contains(raw, []byte(fis)) || bytes.Contains(raw, []byte("Receipt ")) {
		t.Fatalf("manual print is not the Turkish receipt: %q", raw)
	}
	var data string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'receipt_reprint'`).Scan(&data); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if !strings.Contains(data, `"customer_locale":"tr"`) {
		t.Fatalf("audit = %s, want customer_locale tr", data)
	}
}

func TestManualPrint_NoFieldStaysShopLanguage(t *testing.T) {
	code, raw, dp := manualPrint(t, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !bytes.Contains(raw, []byte("Receipt ")) {
		t.Fatalf("journal reprint is not the shop's English copy: %q", raw)
	}
	var data string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'receipt_reprint'`).Scan(&data); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if strings.Contains(data, "customer_locale") {
		t.Fatalf("audit = %s, must not name a locale when none was sent", data)
	}
}

func TestManualPrint_UnknownCustomerLocaleFallsBackToShop(t *testing.T) {
	for _, v := range []string{"xx", "../etc", " ", "tr/../../x", "TR"} {
		code, raw, _ := manualPrint(t, "customer_locale="+url.QueryEscape(v))
		if code != http.StatusOK {
			t.Fatalf("%q: status = %d, want 200 (an unknown locale is never an error)", v, code)
		}
		if !bytes.Contains(raw, []byte("Receipt ")) {
			t.Fatalf("%q: not the shop's English copy: %q", v, raw)
		}
	}
}

func TestRenderReceipt_PrintButtonsCarryCustomerLocale(t *testing.T) {
	funcs := httpx.FuncsFor("en")
	for _, policy := range []string{"", "ask"} {
		with, err := renderReceipt(funcs, "123", nil, nil, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, nil, "My Store", receiptDesign{}, "", nil, policy, nil, nil, "de", "en")
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if policy == "ask" {
			want = 2 // the prompt's Print and the action row's
		}
		if got := strings.Count(with, `data-customer-locale="de"`); got != want {
			t.Fatalf("policy %q: %d buttons carry the pick, want %d", policy, got, want)
		}
		without, err := renderReceipt(funcs, "123", nil, nil, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, nil, "My Store", receiptDesign{}, "", nil, policy, nil, nil, "", "en")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(without, "data-customer-locale") {
			t.Fatalf("policy %q: no pick must emit no attribute", policy)
		}
	}
}

// The cashier tender of a resumed pay-at-counter order: completeTender
// resets the engine, so the pick is read before it and lands on the receipt
// screen's Print button.
func TestCashierTender_ReceiptPrintButtonCarriesCustomerLocale(t *testing.T) {
	for _, tc := range []struct{ pick, want string }{{"de", `data-customer-locale="de"`}, {"", ""}} {
		mux, dp := newFiscalTestDeps(t)
		if _, err := dp.Engine.Scan("ABC"); err != nil {
			t.Fatal(err)
		}
		dp.Engine.SetCustomerLocale(tc.pick)
		rec := fiscalTender(t, mux)
		if rec.Code != http.StatusOK {
			t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if tc.want == "" {
			if strings.Contains(body, "data-customer-locale") {
				t.Fatalf("pick %q: attribute emitted", tc.pick)
			}
			continue
		}
		if !strings.Contains(body, tc.want) {
			t.Fatalf("pick %q: Print button lacks %s: %s", tc.pick, tc.want, body)
		}
	}
}
