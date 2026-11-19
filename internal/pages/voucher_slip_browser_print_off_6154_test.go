package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#6154 AC5: on a printerless Pi kiosk the voucher page offers no
// Print voucher slip button (it would open a PDF-only dialog) and the POST
// hands out no slip and writes no audit row.

func voucherOffMux(t *testing.T, st print.DestState) (*http.ServeMux, *common.Deps) {
	t.Helper()
	t.Cleanup(httpx.SnapshotStateForTests())
	mux, dp := newVoucherImportTestMux(t)
	dp.WindowCtl = common.KioskSystemdWindowController{}
	dp.SystemPrinters = fakeDest{st}
	httpx.InitBrowserPrint(func(r *http.Request) bool { return browserPrintOff(dp, r) })
	seedActive(t, dp, "GV-A", "Anna")
	setSettings(t, dp, map[string]string{keyPrinterMode: "off"})
	return mux, dp
}

func voucherReq(t *testing.T, mux *http.ServeMux, method, path, remote string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remote
	req.Header.Set("HX-Request", "true")
	req = auth.WithUser(req, vManager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestVoucherSlip_BrowserPrintOff_PageHidesButton(t *testing.T) {
	mux, _ := voucherOffMux(t, print.DestNone)
	body := voucherReq(t, mux, http.MethodGet, "/vouchers/GV-A?lang=en", "127.0.0.1:5000").Body.String()
	for _, want := range []string{`data-testid="voucher-slip-no-printer-hint"`, `href="/settings#settings-printer"`,
		"This till has no printer, so it can&#39;t print voucher slips.", `id="voucher-slip-msg"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, bad := range []string{`data-testid="voucher-slip-browser-hint"`, `data-testid="voucher-print-slip"`, `id="voucher-slip-busy"`} {
		if strings.Contains(body, bad) {
			t.Errorf("page must not contain %q", bad)
		}
	}
	// LAN browser keeps today's behaviour.
	lan := voucherReq(t, mux, http.MethodGet, "/vouchers/GV-A?lang=en", "192.168.1.20:5000").Body.String()
	if !strings.Contains(lan, `data-testid="voucher-print-slip"`) || strings.Contains(lan, "voucher-slip-no-printer-hint") {
		t.Error("LAN browser must keep the Print voucher slip button")
	}
}

func TestVoucherSlip_BrowserPrintOff_PostRefuses(t *testing.T) {
	mux, dp := voucherOffMux(t, print.DestNone)
	rec := voucherReq(t, mux, http.MethodPost, "/vouchers/GV-A/print-slip?lang=en", "127.0.0.1:5000")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "This till has no printer, so no slip was printed.") {
		t.Errorf("body: %s", rec.Body.String())
	}
	if rec.Header().Get("X-UT-Response") != "" || strings.Contains(rec.Body.String(), "voucher-slip-print") {
		t.Error("a slip was handed out")
	}
	if rows := slipAuditRows(t, dp, "GV-A"); len(rows) != 0 {
		t.Fatalf("audit rows = %v", rows)
	}
	// non-loopback: today's browser hand-out
	lan := voucherReq(t, mux, http.MethodPost, "/vouchers/GV-A/print-slip?lang=en", "192.168.1.20:5000")
	if lan.Code != http.StatusOK || lan.Header().Get("X-UT-Response") != "print-slip" {
		t.Fatalf("LAN: status %d hdr %q", lan.Code, lan.Header().Get("X-UT-Response"))
	}
}
