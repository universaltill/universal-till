package pages

import (
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#6152: with no receipt printer the gift slip goes through the
// browser's print dialog (voucher page: a handed-out fragment; sale
// receipt: print-only markup).

func TestVoucherSlipBrowser_NoPrinterHandsOutSlip(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	seedActive(t, dp, "GV-A", "Anna")
	setSettings(t, dp, map[string]string{keyPrinterMode: "off", keyReceiptHeader1: "Header line one"})
	rec := vPrintSlip(t, mux, "GV-A", &vManager)
	if rec.Code != http.StatusOK || rec.Header().Get("X-UT-Response") != "print-slip" {
		t.Fatalf("status %d hdr %q body %s", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		httpx.T("en", "vouchers.print_slip.browser_opened"), `id="voucher-slip-print"`, `hx-swap-oob="true"`, `aria-hidden="true"`,
		httpx.T("en", "catalog.item_type.gift_voucher"), httpx.FormatMoneyDisplay(2500, "en"),
		fmt.Sprintf(httpx.T("en", "basket.gift_voucher.for"), "Anna"), "GV-A", `<svg class="receipt-barcode"`,
		strings.TrimSuffix(httpx.T("en", "receipt.vouchers.reprinted"), "%s"), "Header line one",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<img") {
		t.Error("browser slip carries a logo")
	}
	rows := slipAuditRows(t, dp, "GV-A")
	if len(rows) != 1 || rows[0]["ok"] != true || rows[0]["via"] != "browser" || rows[0]["actor"] != vManager.ID {
		t.Fatalf("audit = %v", rows)
	}
}

func TestVoucherSlipBrowser_TwoPressesTwoAuditRows(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	seedActive(t, dp, "GV-A", "")
	setSettings(t, dp, map[string]string{keyPrinterMode: "off"})
	vPrintSlip(t, mux, "GV-A", &vManager)
	vPrintSlip(t, mux, "GV-A", &vManager)
	rows := slipAuditRows(t, dp, "GV-A")
	if len(rows) != 2 || rows[0]["via"] != "browser" || rows[1]["via"] != "browser" {
		t.Fatalf("audit = %v", rows)
	}
}

func TestVoucherSlipBrowser_SuppressedRefusesBeforeBrowser(t *testing.T) {
	h := newShadowDocsHarness(t, "PT", receiptPolicyAlways)
	registerVouchers(h.mux, h.dp)
	seedActive(t, h.dp, "GV-A", "")
	setSettings(t, h.dp, map[string]string{keyPrinterMode: "off"})
	rec := h.do(http.MethodPost, "/vouchers/GV-A/print-slip", "")
	if rec.Code != http.StatusUnavailableForLegalReasons || !strings.Contains(rec.Body.String(), `data-testid="shadow-documents-refused"`) {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "voucher-slip-print") || rec.Header().Get("X-UT-Response") != "" {
		t.Error("slip handed out in a suppressed market")
	}
	rows := slipAuditRows(t, h.dp, "GV-A")
	if len(rows) != 1 || rows[0]["refused"] != "shadow_customer_documents" || rows[0]["via"] != nil {
		t.Fatalf("audit = %v", rows)
	}
}

func TestVoucherSlipBrowser_NotPrintableNoSlip(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(dp.Db)
	vSeed(t, repo, data.Voucher{ID: "GV-VOID", OriginalAmountMinor: 500, Status: "void", CreatedAt: "2026-10-01T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GV-ZERO", OriginalAmountMinor: 500, Status: "active", CreatedAt: "2026-10-01T10:00:00Z"})
	setSettings(t, dp, map[string]string{keyPrinterMode: "off"})
	for _, id := range []string{"GV-VOID", "GV-ZERO"} {
		rec := vPrintSlip(t, mux, id, &vManager)
		if rec.Code != http.StatusConflict || strings.Contains(rec.Body.String(), "voucher-slip-print") || len(slipAuditRows(t, dp, id)) != 0 {
			t.Errorf("%s: status %d audit %v", id, rec.Code, slipAuditRows(t, dp, id))
		}
	}
}

func TestVoucherSlipBrowser_ForbiddenNoSlip(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	seedActive(t, dp, "GV-A", "")
	setSettings(t, dp, map[string]string{keyPrinterMode: "off"})
	cashier := auth.User{ID: "c1", Role: "cashier"}
	noID := auth.User{ID: "", Role: "manager"}
	for name, u := range map[string]*auth.User{"anon": nil, "cashier": &cashier, "manager without id": &noID} {
		rec := vPrintSlip(t, mux, "GV-A", u)
		if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "voucher-slip-print") {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	if len(slipAuditRows(t, dp, "GV-A")) != 0 {
		t.Error("forbidden request audited")
	}
}

func TestVoucherSlipBrowser_PrinterPathUnchanged(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	seedActive(t, dp, "GV-A", "Anna")
	slipDevice(t, dp, "")
	rec := vPrintSlip(t, mux, "GV-A", &vManager)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), httpx.T("en", "vouchers.print_slip.done")) {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-UT-Response") != "" || strings.Contains(rec.Body.String(), "voucher-slip-print") {
		t.Error("printer path handed out a browser slip")
	}
	rows := slipAuditRows(t, dp, "GV-A")
	if len(rows) != 1 || rows[0]["ok"] != true {
		t.Fatalf("audit = %v", rows)
	}
	if _, has := rows[0]["via"]; has {
		t.Error("thermal audit row carries via")
	}
}

func TestVoucherSlipBrowser_RTLShopLocale(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	seedActive(t, dp, "GV-A", "")
	setSettings(t, dp, map[string]string{keyPrinterMode: "off"})
	pinDefaultLocale(t, "ar")
	body := vPrintSlip(t, mux, "GV-A", &vManager).Body.String()
	if !strings.Contains(body, `dir="rtl"`) || !strings.Contains(body, `lang="ar"`) || !strings.Contains(body, httpx.T("ar", "catalog.item_type.gift_voucher")) {
		t.Errorf("slip is not rtl/ar:\n%s", body)
	}
}

func TestVoucherSlipBrowser_UnencodableCodeTextOnly(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	code := "GV-é"
	if httpx.Code128SVG(code) != "" {
		t.Skip("code is encodable")
	}
	seedActive(t, dp, code, "")
	setSettings(t, dp, map[string]string{keyPrinterMode: "off"})
	rec := vPrintSlip(t, mux, code, &vManager)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<svg") || !strings.Contains(rec.Body.String(), "GV-é") {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
}

func TestReceiptBrowserSlips_Mapping(t *testing.T) {
	vs := []receiptVoucherIssue{
		{Code: "A1", Amount: 2500, Holder: "Anna"},
		{Code: "B2", Amount: 1000},
		{Code: "C3", Amount: 500, TopUp: true, Balance: 1500, HasBalance: true},
		{Code: "D4", Amount: 700, TopUp: true},
	}
	got := receiptBrowserSlips("en", "My Store", []string{"H1"}, nil, vs)
	if len(got) != 4 {
		t.Fatalf("got %d slips", len(got))
	}
	for i, s := range got {
		if s.Code != vs[i].Code || s.StoreName != "My Store" || len(s.Header) != 1 || s.Note != "" {
			t.Errorf("slip %d = %+v", i, s)
		}
	}
	if got[0].Label != httpx.T("en", "catalog.item_type.gift_voucher") || got[0].Amount != httpx.FormatMoneyDisplay(2500, "en") ||
		got[0].Holder != fmt.Sprintf(httpx.T("en", "basket.gift_voucher.for"), "Anna") || got[0].Balance != "" {
		t.Errorf("issued with holder = %+v", got[0])
	}
	if got[1].Holder != "" {
		t.Errorf("holder row without a holder: %+v", got[1])
	}
	if got[2].Label != httpx.T("en", "receipt.vouchers.top_up") || got[2].Amount != "+"+httpx.FormatMoneyDisplay(500, "en") ||
		got[2].Balance != fmt.Sprintf(httpx.T("en", "receipt.vouchers.new_balance"), httpx.FormatMoneyDisplay(1500, "en")) {
		t.Errorf("top-up with balance = %+v", got[2])
	}
	if got[3].Balance != "" {
		t.Errorf("top-up without balance has a balance row: %+v", got[3])
	}
}

func TestReceiptPrintLocale(t *testing.T) {
	for _, c := range []struct{ cust, screen, want string }{
		{"", "en", "en"}, {"en", "en", "en"}, {"xx", "en", "en"}, {"tr", "en", "tr"},
	} {
		if got := receiptPrintLocale(c.cust, c.screen); got != c.want {
			t.Errorf("receiptPrintLocale(%q,%q) = %q, want %q", c.cust, c.screen, got, c.want)
		}
	}
}

// slipsTender sells two gift vouchers (one with a holder) and a top-up of
// GS-1, with the printer off and printer.voucher_slip as given.
func slipsTender(t *testing.T, slipSetting, pick string, withVouchers bool) string {
	t.Helper()
	mux, dp := newTopUpTestDeps(t)
	pinDefaultLocale(t, "en")
	topUpSeedVoucher(t, dp, "GS-1", 1000)
	setSettings(t, dp, map[string]string{keyPrinterMode: "off", keyPrinterVoucherSlip: slipSetting})
	dp.Engine.SetCustomerLocale(pick)
	if withVouchers {
		gv := data.ItemIDCode("itm-gv")
		if rec := gvSubmit(mux, gv, "3000", "7771234567890", "&gv_holder=Sam"); rec.Code != http.StatusOK {
			t.Fatalf("gv1: %d %s", rec.Code, rec.Body.String())
		}
		if rec := gvSubmit(mux, gv, "1000", "7771234567891", ""); rec.Code != http.StatusOK {
			t.Fatalf("gv2: %d %s", rec.Code, rec.Body.String())
		}
		if rec := vTopUp(t, mux, "GS-1", "25", &vManager, true); rec.Code != http.StatusOK {
			t.Fatalf("top-up: %d %s", rec.Code, rec.Body.String())
		}
	} else if rec := posPostForm(mux, "/api/pos/scan", "code=PLAIN"); rec.Code != http.StatusOK {
		t.Fatalf("scan: %d", rec.Code)
	}
	rec := cashTender(mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestTenderReceipt_VoucherSlipsWhenSettingOn(t *testing.T) {
	body := slipsTender(t, "true", "", true)
	if n := strings.Count(body, `data-testid="receipt-voucher-slips"`); n != 1 {
		t.Fatalf("slip wrappers = %d:\n%s", n, body)
	}
	i := strings.Index(body, `data-testid="receipt-voucher-slips"`)
	wrap := body[strings.LastIndex(body[:i], "<div"):]
	if !strings.Contains(wrap[:200], `aria-hidden="true"`) {
		t.Error("slip wrapper is not aria-hidden")
	}
	if n := strings.Count(wrap, `data-testid="voucher-slip-paper"`); n != 3 {
		t.Fatalf("slips = %d, want 3", n)
	}
	for _, code := range []string{"7771234567890", "7771234567891", "GS-1"} {
		if !strings.Contains(wrap, code) {
			t.Errorf("slip for %s missing", code)
		}
	}
	if strings.Index(wrap, "7771234567890") > strings.Index(wrap, "7771234567891") {
		t.Error("slips not in sale order")
	}
	for _, want := range []string{fmt.Sprintf(httpx.T("en", "basket.gift_voucher.for"), "Sam"), httpx.T("en", "receipt.vouchers.top_up"),
		"+" + httpx.FormatMoneyDisplay(2500, "en"), fmt.Sprintf(httpx.T("en", "receipt.vouchers.new_balance"), httpx.FormatMoneyDisplay(3500, "en"))} {
		if !strings.Contains(wrap, strings.ReplaceAll(html.EscapeString(want), "+", "&#43;")) { // html/template escapes "+"
			t.Errorf("slips lack %q", want)
		}
	}
	if strings.Contains(wrap, strings.TrimSuffix(httpx.T("en", "receipt.vouchers.reprinted"), "%s")) {
		t.Error("sale slip carries a Reprinted line")
	}
	seen := map[string]int{}
	for _, m := range regexp.MustCompile(`\sid="([^"]*)"`).FindAllStringSubmatch(body, -1) {
		seen[m[1]]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("id %q appears %d times", id, n)
		}
	}
}

func TestTenderReceipt_VoucherSlipsSettingOffNoSlips(t *testing.T) {
	if body := slipsTender(t, "false", "", true); strings.Contains(body, "voucher-slips-print") || strings.Contains(body, "voucher-slip-paper") {
		t.Error("slip markup with the setting off")
	}
}

func TestTenderReceipt_VoucherSlipsNoVouchersNoSlips(t *testing.T) {
	if body := slipsTender(t, "true", "", false); strings.Contains(body, "voucher-slips-print") {
		t.Error("slip markup without vouchers")
	}
}

func TestTenderReceipt_VoucherSlipsFollowCustomerLanguage(t *testing.T) {
	body := slipsTender(t, "true", "tr", true)
	i := strings.Index(body, `class="voucher-slips-print"`)
	if i < 0 {
		t.Fatalf("no slips:\n%s", body)
	}
	wrap := body[i:]
	if !strings.Contains(wrap[:120], `lang="tr"`) || !strings.Contains(wrap, httpx.T("tr", "receipt.vouchers.top_up")) {
		t.Errorf("slips not in tr: %.400s", wrap)
	}
	if strings.Contains(wrap, httpx.T("en", "receipt.vouchers.top_up")) {
		t.Error("slips leaked the cashier's English")
	}
}

func TestTenderReceipt_VoucherSlipsSuppressedNoSlips(t *testing.T) {
	h := newShadowDocsHarness(t, "PT", receiptPolicyAlways)
	setSettings(t, h.dp, map[string]string{keyPrinterVoucherSlip: "true"})
	rec, _ := h.sell()
	if strings.Contains(rec.Body.String(), "voucher-slip-paper") {
		t.Error("slip markup in a suppressed market")
	}
}
