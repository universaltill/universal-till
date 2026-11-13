package pages

import (
	"html/template"
	"net/http"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// browserPrintOff (ut-docs#6154): true only when the page is for the Pi
// kiosk's own screen and the device definitely has no OS print destination
// — the browser's dialog could only "Save as PDF" to a home directory
// nobody can reach. Any doubt (probe error/timeout/not yet run, LAN
// browser, non-Pi till) → false: today's behaviour. Cheap checks first, so
// a LAN request never touches the probe cache. Render-time only: never on
// the tender/commit path.
func browserPrintOff(d *common.Deps, r *http.Request) bool {
	if d == nil || r == nil || d.SystemPrinters == nil {
		return false
	}
	if !isPiKioskAppliance(d.WindowCtl) || !isLoopbackAddr(r.RemoteAddr) {
		return false
	}
	return d.SystemPrinters.State() == print.DestNone
}

// receiptPrinterWarningHTML is receipt.html's warning block for a failed
// render (no template available) — same browser-print-off rule: on a
// printerless Pi kiosk the Retry button gives way to the Settings link (or,
// for a viewer who can't open Settings, the ask-a-manager text).
func receiptPrinterWarningHTML(T func(string) string, browserPrintOff, allowSettings bool) string {
	esc := template.HTMLEscapeString
	msg := `<span class="receipt-printer-message">` + esc(T("receipt.printer.unavailable")) + `</span>`
	var act string
	switch {
	case !browserPrintOff:
		act = `<button class="btn secondary receipt-printer-retry" type="button" data-action="print">` + esc(T("receipt.printer.retry")) + `</button>`
	case allowSettings:
		act = `<a class="btn secondary receipt-printer-setup" data-testid="receipt-printer-setup" href="/settings#settings-printer">` + esc(T("receipt.printer.set_up")) + `</a>`
	default:
		act = `<span class="receipt-printer-message" data-testid="receipt-printer-ask-manager">` + esc(T("receipt.printer.ask_manager")) + `</span>`
	}
	return `<div class="receipt-printer-warning">` + msg + act + `</div>`
}
