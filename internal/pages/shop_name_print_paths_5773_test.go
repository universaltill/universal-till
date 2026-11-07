package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5773 (Tester): the print paths the Dev tests did not reach — Z
// print, Z reprint, the printer test print, the receipt designer's sample
// print and the voucher-slip reprint — each print no shop-name row on an
// unnamed shop (stored placeholder "My Store") and the real name once set.
// Each path is driven through a real fake network printer, so swapping its
// shopNameOrEmpty call back to a placeholder fails here.

const escposDoubleOn = "\x1d!\x11" // print.cmdDoubleOn: opens the shop-name row

func setPrinter5773(t *testing.T, dp *common.Deps, addr, storeName string) {
	t.Helper()
	for k, v := range map[string]string{keyPrinterMode: "network", keyPrinterAddress: addr, keyPrinterCharset: "utf8", common.KeyStoreName: storeName} {
		if err := dp.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
}

// assertJob5773 checks the one ESC/POS job a print path sent.
func assertJob5773(t *testing.T, what string, jobs []string, named string) {
	t.Helper()
	if len(jobs) != 1 {
		t.Fatalf("%s: printed %d jobs, want 1", what, len(jobs))
	}
	job := jobs[0]
	if named == "" {
		if strings.Contains(job, escposDoubleOn) || strings.Contains(job, "My Store") || strings.Contains(job, "this shop") {
			t.Fatalf("%s: unnamed shop printed a name row or placeholder:\n%q", what, job)
		}
		return
	}
	if !strings.Contains(job, escposDoubleOn+named+"\n") {
		t.Fatalf("%s: named shop lost its name row:\n%q", what, job)
	}
}

func post5773(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPrinterTestPrint_UnnamedShopPrintsNoNameRow(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPrintAPITestDeps(t)
	for _, name := range []string{"", "Café Linde"} {
		printer := newFakeKitchenPrinter(t)
		setPrinter5773(t, dp, printer.addr, map[string]string{"": "My Store"}[name]+name)
		if rec := post5773(mux, "/api/print/test"); rec.Code != http.StatusOK {
			t.Fatalf("test print %d: %s", rec.Code, rec.Body.String())
		}
		assertJob5773(t, "test print", printer.Settle(1), name)
	}
}

func TestReceiptDesignerSamplePrint_UnnamedShopPrintsNoNameRow(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReceiptDesignerTestDeps(t)
	for _, name := range []string{"", "Café Linde"} {
		printer := newFakeKitchenPrinter(t)
		setPrinter5773(t, dp, printer.addr, map[string]string{"": "My Store"}[name]+name)
		if rec := post5773(mux, "/api/receipt-designer/test"); rec.Code != http.StatusOK {
			t.Fatalf("designer sample print %d: %s", rec.Code, rec.Body.String())
		}
		assertJob5773(t, "designer sample print", printer.Settle(1), name)
	}
}

func TestEODPrintAndReprint_UnnamedShopPrintsNoNameRow(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newEODAPITestMux(t)
	printer := newFakeKitchenPrinter(t)
	setPrinter5773(t, dp, printer.addr, "My Store")
	rep, _, err := generateEOD(t.Context(), dp, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	assertJob5773(t, "Z print", printer.Settle(1), "")

	for _, name := range []string{"", "Café Linde"} {
		printer := newFakeKitchenPrinter(t)
		setPrinter5773(t, dp, printer.addr, map[string]string{"": "My Store"}[name]+name)
		if rec := post5773(mux, "/api/reports/eod/print/"+url.PathEscape(rep.To)); rec.Code != http.StatusOK {
			t.Fatalf("Z reprint %d: %s", rec.Code, rec.Body.String())
		}
		assertJob5773(t, "Z reprint", printer.Settle(1), name)
	}
}

func TestVoucherSlipReprint_UnnamedShopHasNoName(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	v := data.Voucher{ID: "v1", Currency: "GBP", OriginalAmountMinor: 1000, BalanceMinor: 1000}
	for _, c := range []struct{ store, want string }{{"My Store", ""}, {"", ""}, {"Café Linde", "Café Linde"}} {
		if err := dp.Settings.Set(t.Context(), common.KeyStoreName, c.store); err != nil {
			t.Fatal(err)
		}
		cfg := printerConfig(t.Context(), dp)
		if got := voucherSlipFor(t.Context(), dp, v, cfg, time.Now()).StoreName; got != c.want {
			t.Fatalf("store.name %q: slip StoreName = %q, want %q", c.store, got, c.want)
		}
	}
}

// AC6, GET half: the designer page's first-render preview (the POST refresh
// is TestReceiptDesigner_UnnamedShopPreviewHasNoNameRow).
func TestReceiptDesignerPage_UnnamedShopPreviewHasNoNameRow(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReceiptDesignerTestDeps(t)
	for _, c := range []struct{ store, want string }{{"My Store", ""}, {"Café Linde", "Café Linde"}} {
		if err := dp.Settings.Set(t.Context(), common.KeyStoreName, c.store); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/receipt-designer", nil))
		_, pre, ok := strings.Cut(rec.Body.String(), `id="rd-preview"`)
		if rec.Code != http.StatusOK || !ok {
			t.Fatalf("GET /receipt-designer %d, preview found %v", rec.Code, ok)
		}
		_, pre, _ = strings.Cut(pre, ">")
		first, _, _ := strings.Cut(pre, "\n")
		if c.want == "" {
			if strings.Contains(pre[:200], "My Store") || strings.Contains(pre[:200], "this shop") || strings.TrimSpace(first) == "" {
				t.Fatalf("unnamed preview starts with a name/blank row: %q", pre[:120])
			}
		} else if strings.TrimSpace(first) != c.want {
			t.Fatalf("named preview first line %q, want %q", first, c.want)
		}
	}
}
