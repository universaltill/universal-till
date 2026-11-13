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

// ut-docs#6154 AC3: a printerless Pi kiosk's receipt offers the Settings
// link (or "ask a manager") instead of a Retry button that could only open a
// PDF-only print dialog.

func tender6154(t *testing.T, st print.DestState, remote string, user *auth.User) string {
	t.Helper()
	t.Cleanup(httpx.SnapshotStateForTests())
	mux, dp := newFiscalTestDeps(t)
	pinDefaultLocale(t, "en")
	// sales.cashier_id has a real FK to users(id): the sessions injected via
	// auth.WithUser need matching rows.
	for _, u := range []auth.User{mgrUser, cashUser} {
		if _, err := dp.Db.Exec(`INSERT INTO users(id, username, display_name, role) VALUES (?, ?, ?, ?)`, u.ID, u.ID, u.DisplayName, u.Role); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	dp.WindowCtl = common.KioskSystemdWindowController{}
	dp.SystemPrinters = fakeDest{st}
	httpx.InitBrowserPrint(func(r *http.Request) bool { return browserPrintOff(dp, r) })
	httpx.InitRailVisibility(menuPredicateChecker(dp))
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remote
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestTender_ReceiptNoBrowserPrint(t *testing.T) {
	const link = `href="/settings#settings-printer"`
	body := tender6154(t, print.DestNone, "127.0.0.1:5000", &mgrUser)
	if !strings.Contains(body, "Printer unavailable.") {
		t.Fatalf("no printer-unavailable warning: %s", body)
	}
	if !strings.Contains(body, `receipt-printer-setup`) || !strings.Contains(body, link) {
		t.Fatalf("manager: setup link missing: %s", body)
	}
	if strings.Contains(body, `data-action="print"`) {
		t.Fatal("Retry print button must be gone")
	}

	cash := tender6154(t, print.DestNone, "127.0.0.1:5000", &cashUser)
	if strings.Contains(cash, link) {
		t.Fatal("cashier must not get a link to a page it cannot open")
	}
	if !strings.Contains(cash, "Ask a manager to set up a receipt printer.") {
		t.Fatalf("cashier: ask-manager text missing: %s", cash)
	}
	if strings.Contains(cash, `data-action="print"`) {
		t.Fatal("cashier: Retry print button must be gone")
	}

	lan := tender6154(t, print.DestNone, "192.168.1.20:5000", &mgrUser)
	if !strings.Contains(lan, `data-action="print"`) || strings.Contains(lan, `receipt-printer-setup`) {
		t.Fatalf("LAN browser keeps Retry print: %s", lan)
	}
	pres := tender6154(t, print.DestPresent, "127.0.0.1:5000", &mgrUser)
	if !strings.Contains(pres, `data-action="print"`) {
		t.Fatal("OS printer present keeps Retry print")
	}
}

// The render-failure fallback string: byte-identical to the old Retry markup
// when browser print is on.
func TestReceiptPrinterWarningHTML(t *testing.T) {
	T := func(k string) string { return "<" + k + ">" }
	want := `<div class="receipt-printer-warning"><span class="receipt-printer-message">&lt;receipt.printer.unavailable&gt;</span><button class="btn secondary receipt-printer-retry" type="button" data-action="print">&lt;receipt.printer.retry&gt;</button></div>`
	if got := receiptPrinterWarningHTML(T, false, true); got != want {
		t.Fatalf("browser print on changed:\n%s", got)
	}
	got := receiptPrinterWarningHTML(T, true, true)
	if strings.Contains(got, "data-action") || !strings.Contains(got, `href="/settings#settings-printer"`) || !strings.Contains(got, "&lt;receipt.printer.set_up&gt;") {
		t.Fatalf("off+allowed: %s", got)
	}
	got = receiptPrinterWarningHTML(T, true, false)
	if strings.Contains(got, "href=") || !strings.Contains(got, "&lt;receipt.printer.ask_manager&gt;") {
		t.Fatalf("off+not allowed: %s", got)
	}
}
