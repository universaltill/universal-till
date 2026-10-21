package pages

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#4730: a receipt printed through the browser's print dialog is the
// page itself, so a self-order customer's language pick (ut-docs#4705) rides
// along as a print-only second copy of the receipt body in their language.

// tenderWithPick tenders one sale as an English cashier with the given
// customer pick on the engine and returns the receipt screen HTML.
func tenderWithPick(t *testing.T, pick string) string {
	t.Helper()
	mux, dp := newFiscalTestDeps(t)
	pinDefaultLocale(t, "en")
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	dp.Engine.SetCustomerLocale(pick)
	rec := fiscalTender(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// splitPrintCopy returns the screen part and the print-copy part of a
// receipt response ("" copy when there is none).
func splitPrintCopy(html string) (screen, copy string) {
	i := strings.Index(html, `class="receipt-card receipt-print-copy"`)
	if i < 0 {
		return html, ""
	}
	return html[:i], html[i:]
}

func TestBrowserPrint_CustomerLanguageCopy(t *testing.T) {
	html := tenderWithPick(t, "tr")
	screen, cp := splitPrintCopy(html)
	if cp == "" {
		t.Fatalf("no print copy in the receipt: %s", html)
	}
	if !strings.Contains(html, `receipt-wrap has-print-copy`) {
		t.Fatalf("wrap lacks the has-print-copy marker")
	}
	for _, want := range []string{`lang="tr"`, `dir="ltr"`, `aria-hidden="true"`, "Fiş #", "Toplam"} {
		if !strings.Contains(cp, want) {
			t.Fatalf("print copy lacks %q: %s", want, cp)
		}
	}
	if strings.Contains(cp, "Receipt #") || strings.Contains(cp, "Total:") {
		t.Fatalf("print copy leaked the cashier's English: %s", cp)
	}
	if !strings.Contains(screen, "Receipt #") || strings.Contains(screen, "Fiş #") {
		t.Fatalf("screen card is not the cashier's English")
	}
}

func TestBrowserPrint_NoPickNoCopy(t *testing.T) {
	html := tenderWithPick(t, "")
	for _, s := range []string{"receipt-print-copy\"", "has-print-copy\""} {
		if strings.Contains(html, s) {
			t.Fatalf("no pick must emit no print copy, found %q", s)
		}
	}
}

func TestBrowserPrint_PickEqualToCashierLanguageNoCopy(t *testing.T) {
	html := tenderWithPick(t, "en")
	if strings.Contains(html, `class="receipt-card receipt-print-copy"`) || strings.Contains(html, "has-print-copy\"") {
		t.Fatalf("a pick equal to the screen language must emit no print copy")
	}
}

func TestBrowserPrint_UninstalledPickNoCopy(t *testing.T) {
	html := tenderWithPick(t, "xx")
	if strings.Contains(html, `class="receipt-card receipt-print-copy"`) {
		t.Fatalf("an uninstalled pick must emit no print copy")
	}
}

func TestBrowserPrint_RTLPickSetsDir(t *testing.T) {
	for _, lang := range []string{"fa", "ar"} {
		_, cp := splitPrintCopy(tenderWithPick(t, lang))
		if cp == "" || !strings.Contains(cp, `lang="`+lang+`"`) || !strings.Contains(cp, `dir="rtl"`) {
			t.Fatalf("%s: print copy is not rtl: %.200s", lang, cp)
		}
	}
}

// Both copies are in one page: no id may appear twice (barcodes, vouchers).
func TestBrowserPrint_NoDuplicateIDs(t *testing.T) {
	funcs := httpx.FuncsFor("en")
	html, err := renderReceipt(funcs, "R-4730", nil, nil, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, nil, "My Store",
		receiptDesign{ShowTax: true, ShowBarcode: true}, "", []receiptVoucherIssue{{Code: "GS-4730", Amount: 500}}, "ask", nil, nil, "tr", "en")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `class="receipt-card receipt-print-copy"`) {
		t.Fatal("expected a print copy")
	}
	seen := map[string]int{}
	for _, m := range regexp.MustCompile(`\sid="([^"]*)"`).FindAllStringSubmatch(html, -1) {
		seen[m[1]]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("id %q appears %d times", id, n)
		}
	}
	_, cp := splitPrintCopy(html)
	if strings.Contains(cp, ` id="`) {
		t.Fatalf("print copy must carry no ids: %s", cp)
	}
}

// The copy is keyed on renderReceipt's screenLocale: a pick equal to it
// renders no copy.
func TestBrowserPrint_ScreenLocaleParamControlsCopy(t *testing.T) {
	funcs := httpx.FuncsFor("tr")
	html, err := renderReceipt(funcs, "R-1", nil, nil, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, nil, "S", receiptDesign{}, "", nil, "", nil, nil, "tr", "tr")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, `class="receipt-card receipt-print-copy"`) {
		t.Fatal("pick equal to screen locale must not duplicate the receipt")
	}
}

// The print copy's tracking QR opens the tracking page in the customer's
// language (its CustomerCopy link); the screen card keeps the cashier's.
func TestBrowserPrint_TrackingQRFollowsPick(t *testing.T) {
	tracking := &receiptTrackingView{
		QRDataURI:    "data:image/png;base64,U0NSRUVO",
		URL:          "https://example.test/o/tok?lang=en",
		CustomerCopy: &receiptTrackingView{QRDataURI: "data:image/png;base64,Q1VTVE9NRVI=", URL: "https://example.test/o/tok?lang=tr"},
	}
	html, err := renderReceipt(httpx.FuncsFor("en"), "R-1", nil, nil, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, nil, "S", receiptDesign{}, "", nil, "", nil, tracking, "tr", "en")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(html, `class="receipt-card receipt-print-copy"`)
	if i < 0 {
		t.Fatal("expected a print copy")
	}
	screen, printCopy := html[:i], html[i:]
	if !strings.Contains(screen, "U0NSRUVO") || strings.Contains(screen, "Q1VTVE9NRVI=") {
		t.Error("screen card must carry the cashier-language tracking QR only")
	}
	if !strings.Contains(printCopy, "Q1VTVE9NRVI=") || strings.Contains(printCopy, "U0NSRUVO") {
		t.Error("print copy must carry the customer-language tracking QR only")
	}
}
