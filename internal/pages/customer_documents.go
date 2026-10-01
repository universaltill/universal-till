package pages

// The customer-document gate's one call site in internal/pages (ADR-0124,
// ut-docs#3169). Every surface that produces a document a customer sees
// or receives asks customerDocumentsSuppressed on the server:
//
//   - printerConfigChecked forces the effective receipt policy to "never"
//     (after the receipt.policy.ask clamp, so no plugin answer re-enables
//     it), which stops the tender and refund auto-print;
//   - printReceipt, the shared print choke point every receipt print path
//     reaches, refuses with errCustomerDocumentsSuppressed;
//   - POST /api/print/receipt/{receiptNo} and POST /api/receipt-designer/test
//     answer 451 with the localized refusal (writeCustomerDocumentsRefused);
//   - the tender response renders partials/sale_recorded.html instead of
//     the receipt view;
//   - the sell screen shows a persistent banner.
//
// Invoices, credit notes and the self-order kiosk are ut-docs#3170. Any
// customer-document surface built later must call this helper too
// (ADR-0124 §3, last row).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// errCustomerDocumentsSuppressed is printReceipt's refusal when the gate
// says suppressed. Callers map it to the localized refusal, and it is NOT a
// printer failure: it must never set the /orders print-failed warning.
var errCustomerDocumentsSuppressed = errors.New("customer documents are withheld: shadow mode in a market that forbids them (ADR-0124)")

// customerDocumentsSuppressed reports whether this shop may not produce
// customer documents right now. It is the only caller of
// fiscal.CustomerDocuments. No network and no plugin call: a sale never
// waits on it.
//
// The country comes from the in-memory runtime state. Accepted residual
// risk (ADR-0124 §2, decided at the #3169 Architect step): common.LoadState
// falls back to "GB" when store.country cannot be read, so a till whose
// settings store failed at load would read as an allowed market. That is
// the same fallback every per-country rule already inherits; moving this
// gate onto a second, erroring settings read would make every print path
// depend on it, and an unreadable settings store already breaks checkout
// loudly. An owner who declares a different country escapes this gate the
// same way they escape every other per-country rule.
func customerDocumentsSuppressed(ctx context.Context, d *common.Deps) bool {
	if d == nil {
		return false
	}
	var countries fiscal.CountryDocumentsReader
	if d.Db != nil {
		countries = data.NewCountrySettingsRepo(d.Db)
	}
	country := d.CurrentState().Country
	dec, err := fiscal.CustomerDocuments(ctx, country, data.BuiltinShadowDocumentsForbidden, countries)
	if err != nil {
		// ADR-0124 §2 step 4: an allowed market keeps its documents when
		// the column can't be read; a forbidden market never gets here.
		log.Printf("pages.customerDocumentsSuppressed: %v (documents stay allowed for %q)", err, country)
	}
	return dec == fiscal.DocumentsSuppressedShadow
}

// writeCustomerDocumentsRefused answers a refused document request: 451
// (Unavailable For Legal Reasons) — a status the receipt partial's print
// buttons never treat as "fall back to the browser print" — with the
// localized refusal as an HTML fragment, which journal_detail's hx-post
// swaps into its message slot (app.js force-swaps a non-empty text/html
// 4xx fragment into its own hx-target).
func writeCustomerDocumentsRefused(w http.ResponseWriter, r *http.Request) {
	locale := httpx.ResolveLocale(w, r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnavailableForLegalReasons)
	fmt.Fprintf(w, `<span class="muted" data-testid="shadow-documents-refused">✗ %s</span>`,
		template.HTMLEscapeString(httpx.T(locale, "shadow_documents.refused")))
}

// renderSaleRecorded renders the staff confirmation the tender response
// shows instead of the receipt in a suppressed market: the receipt number
// and the reason, with no lines, totals, fiscal blocks or print action.
func renderSaleRecorded(funcs template.FuncMap, receiptNo string) (string, error) {
	t, err := httpx.ClonedTemplate("pages.renderSaleRecorded", "sale_recorded.html", funcs,
		"ui/partials/sale_recorded.html",
		"ui/partials/receipt_auto_reset.html",
	)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "sale_recorded", map[string]any{"ReceiptNo": receiptNo}); err != nil {
		return "", err
	}
	return buf.String(), nil
}
