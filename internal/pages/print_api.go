package pages

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/print"
)

// Printer settings keys (docs: architecture/receipt-printing.md).
const (
	keyPrinterMode    = "printer.mode"    // off | network | device
	keyPrinterAddress = "printer.address" // host[:port]
	keyPrinterDevice  = "printer.device"  // /dev/usb/lp0
	keyPrinterCharset = "printer.charset" // utf8 | ascii
	keyPrinterAuto    = "printer.auto_print"
	keyPrinterKitchen = "printer.kitchen_addr" // kitchen printer host[:port] or device path
)

// printerConfig reads the printer.* settings into a print.Config.
func printerConfig(ctx context.Context, d *common.Deps) print.Config {
	get := func(key, def string) string {
		if v, ok, _ := d.Settings.Get(ctx, key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}
	return print.Config{
		Mode:           get(keyPrinterMode, "off"),
		Address:        get(keyPrinterAddress, ""),
		Device:         get(keyPrinterDevice, ""),
		Charset:        get(keyPrinterCharset, "utf8"),
		AutoPrint:      get(keyPrinterAuto, "true") == "true",
		KitchenAddress: get(keyPrinterKitchen, ""),
	}
}

// receiptDesign mirrors the receipt.* settings (docs: receipt-designer.md).
type receiptDesign struct {
	Header      []string // up to 3 lines under the shop name
	Footer      string
	ShowSKU     bool
	ShowTax     bool // subtotal+tax rows; off = TOTAL only
	ShowBarcode bool
	ShowLogo    bool // uploaded logo as a GS v 0 raster above the name
}

const (
	keyReceiptHeader1     = "receipt.header1"
	keyReceiptHeader2     = "receipt.header2"
	keyReceiptHeader3     = "receipt.header3"
	keyReceiptFooter      = "receipt.footer"
	keyReceiptShowSKU     = "receipt.show_sku"
	keyReceiptShowTax     = "receipt.show_tax"
	keyReceiptShowBarcode = "receipt.show_barcode"
	keyReceiptShowLogo    = "receipt.show_logo"
)

// receiptLogoPath is where the designer stores the uploaded logo — the
// stable per-user data directory, NOT the release tree: a self-update
// replaces the release tree wholesale (see internal/pages/static_page.go's
// fallbackFS doc comment), which silently deleted a shop's uploaded logo
// when this was "web/public/assets/logo/receipt-logo.png". A function, not
// a const: paths.DataDir() resolves at runtime (paths.Init runs during
// config load, after this package's consts would already be evaluated).
func receiptLogoPath() string { return paths.Data("public", "assets", "logo", "receipt-logo.png") }

// receiptLogoRaster loads and encodes the uploaded logo when the design
// wants it; nil (no logo) on any failure — never block a receipt.
func receiptLogoRaster(rd receiptDesign) []byte {
	if !rd.ShowLogo {
		return nil
	}
	raw, err := os.ReadFile(receiptLogoPath())
	if err != nil {
		return nil
	}
	return print.RasterLogo(raw)
}

// receiptDesignFromSettings loads the saved design with friendly defaults.
func receiptDesignFromSettings(ctx context.Context, d *common.Deps) receiptDesign {
	get := func(key, def string) string {
		if v, ok, _ := d.Settings.Get(ctx, key); ok {
			return strings.TrimSpace(v)
		}
		return def
	}
	rd := receiptDesign{
		Footer:      get(keyReceiptFooter, "Thank you!"),
		ShowSKU:     get(keyReceiptShowSKU, "false") == "true",
		ShowTax:     get(keyReceiptShowTax, "true") != "false",
		ShowBarcode: get(keyReceiptShowBarcode, "true") != "false",
		ShowLogo:    get(keyReceiptShowLogo, "false") == "true",
	}
	for _, k := range []string{keyReceiptHeader1, keyReceiptHeader2, keyReceiptHeader3} {
		if v := get(k, ""); v != "" {
			rd.Header = append(rd.Header, v)
		}
	}
	return rd
}

// buildReceiptDoc assembles the printable receipt for a completed sale.
func buildReceiptDoc(ctx context.Context, d *common.Deps, receiptNo string) (print.Doc, error) {
	detail, ok, err := data.NewPOSRepo(d.Db).GetSaleDetail(ctx, receiptNo)
	if err != nil {
		return print.Doc{}, err
	}
	if !ok {
		return print.Doc{}, fmt.Errorf("receipt %s not found", receiptNo)
	}
	cfg := printerConfig(ctx, d)
	rd := receiptDesignFromSettings(ctx, d)
	locale := "en" // receipts print with latin digits; RTL needs bitmap mode (spec)
	money := func(minor int64) string { return httpx.FormatMoney(minor, locale) }

	doc := print.Doc{
		StoreName: storeNameOrDefault(ctx, d),
		Logo:      receiptLogoRaster(rd),
		Header:    rd.Header,
		Meta: []string{
			"Receipt " + detail.ReceiptNo,
			detail.CreatedAt,
		},
		Charset: cfg.Charset,
	}
	if rd.ShowBarcode {
		// Scan-to-refund (G28): the receipt number as a barcode.
		doc.Barcode = detail.ReceiptNo
	}
	if detail.SaleType == "return" {
		meta := []string{"*** REFUND ***"}
		if orig, ok, _ := data.NewPOSRepo(d.Db).OriginalReceiptFor(ctx, detail.ID); ok {
			meta = append(meta, "For receipt "+orig)
		}
		doc.Meta = append(meta, doc.Meta...)
	}
	for _, l := range detail.Lines {
		qty := strconv.FormatFloat(l.Qty, 'f', -1, 64)
		name := l.Name
		if rd.ShowSKU && l.SKU != "" {
			name += " [" + l.SKU + "]"
		}
		doc.Lines = append(doc.Lines, print.Line{Name: name, Qty: qty, Amount: money(l.LineTotal)})
	}
	if rd.ShowTax {
		doc.Totals = []print.KV{{Label: "Subtotal", Amount: money(detail.Subtotal)}}
	}
	if detail.DiscountTotal != 0 {
		doc.Totals = append(doc.Totals, print.KV{Label: "Discount", Amount: "-" + money(detail.DiscountTotal)})
	}
	if rd.ShowTax {
		doc.Totals = append(doc.Totals, print.KV{Label: "Tax", Amount: money(detail.TaxTotal)})
	}
	if detail.ServiceCharge != 0 {
		doc.Totals = append(doc.Totals, print.KV{Label: "Service Charge", Amount: money(detail.ServiceCharge)})
	}
	doc.Totals = append(doc.Totals, print.KV{Label: "TOTAL", Amount: money(detail.Total), Strong: true})
	for _, p := range detail.Payments {
		doc.Payments = append(doc.Payments, print.KV{Label: strings.ToUpper(p.Method[:1]) + p.Method[1:], Amount: money(p.Amount)})
		if p.ChangeGiven > 0 {
			doc.Payments = append(doc.Payments, print.KV{Label: "Change", Amount: money(p.ChangeGiven)})
		}
		if p.TipAmount > 0 {
			doc.Payments = append(doc.Payments, print.KV{Label: "Tip", Amount: money(p.TipAmount)})
		}
		if p.Method == "cash" {
			doc.KickDrawer = true
		}
	}
	// ADR-0048: mark the printed receipt if this sale was taken during an
	// active TSE-override window. Derived from the sale's own audit row
	// (written by completeTender at tender time), not from current fiscal
	// settings — printing (and especially a reprint) can happen well after
	// the override window that was active when the sale completed, so
	// re-reading current settings here would give a stale or wrong answer.
	if hasOverride, auditErr := data.NewPOSRepo(d.Db).HasAuditEntry(ctx, "sale", detail.ID, "unsigned_override"); auditErr == nil && hasOverride {
		doc.Meta = append(doc.Meta, "Recorded during a documented TSE outage, under a time-limited owner override.")
	}
	// ADR-0044 proceed-and-declare (ut-docs#675): same audit-row derivation
	// as the override line above — the sale's own unsigned_fiscal_signing
	// marker, written by completeTender at tender time, decides the notice;
	// a reprint long after the outage still shows what happened to THIS sale.
	if hasSignGap, auditErr := data.NewPOSRepo(d.Db).HasAuditEntry(ctx, "sale", detail.ID, "unsigned_fiscal_signing"); auditErr == nil && hasSignGap {
		doc.Meta = append(doc.Meta, "TSE signing was unavailable when this sale was recorded; signing is retried automatically.")
	}
	if rd.Footer != "" {
		doc.Footer = []string{rd.Footer}
	}
	return doc, nil
}

// Test seams for the "printer hung until the deadline" regression
// (TestAsyncPrintFailureIsRecordedWhenPrintCtxExpired). Production always runs
// the real functions with the real 15s budget; a test shortens the budget and
// substitutes a print that blocks until the budget runs out, which is the one
// sequence that used to lose the failure silently. Vars, not consts/direct
// calls, purely for that — nothing outside _test.go ever reassigns them.
var (
	printAsyncTimeout = 15 * time.Second
	printReceiptFn    = printReceipt
	printKitchenFn    = printKitchen
)

// recordPrintFailureCtx returns the short-lived context used to WRITE a print
// failure down (audit row + the /orders warning flag, ut-docs#517a).
//
// It deliberately does not inherit the print attempt's own context. A printer
// that is out of paper, unplugged mid-write or hung does not fail fast: both
// transports block until the print context's deadline (deviceTransport selects
// on ctx.Done(); networkTransport hands the deadline to SetWriteDeadline), so
// by the time printReceipt/printKitchen returns that error, its context is
// already expired — and every write made with it would be dropped by
// database/sql before touching the DB. Reusing it would silently lose exactly
// the failures this feature exists to surface.
func recordPrintFailureCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// printReceiptAsync prints a completed sale without ever blocking checkout:
// fired as a goroutine after tender, failures are logged + audited and
// flagged on the sale (ut-docs#517a) so /orders can surface them.
// Tracked on d.AsyncWork (ut-docs#425) so a caller tearing down shared state
// (a test closing Db, graceful shutdown) can wait for it to actually finish
// instead of racing it — see AsyncWork's doc comment on common.Deps.
func printReceiptAsync(d *common.Deps, receiptNo string, actorID string) {
	d.AsyncWork.Add(1)
	go func() {
		defer d.AsyncWork.Done()
		ctx, cancel := context.WithTimeout(context.Background(), printAsyncTimeout)
		defer cancel()
		cfg := printerConfig(ctx, d)
		if !cfg.Enabled() || !cfg.AutoPrint {
			// No attempt (printing off/manual) — must neither overwrite a
			// real prior failure nor falsely clear one.
			return
		}
		posRepo := data.NewPOSRepo(d.Db)
		if err := printReceiptFn(ctx, d, receiptNo); err != nil {
			wctx, wcancel := recordPrintFailureCtx()
			defer wcancel()
			_ = posRepo.InsertAudit(wctx, nil, actorID, "sale", receiptNo, "print_failed",
				map[string]any{"error": err.Error()}, time.Now().UTC().Format(time.RFC3339), "")
			_ = posRepo.SetReceiptPrintFailed(wctx, receiptNo, time.Now().UTC().Format(time.RFC3339))
			return
		}
		// Success clears any stale flag from an earlier failed attempt.
		_ = posRepo.SetReceiptPrintFailed(ctx, receiptNo, "")
	}()
}

func printReceipt(ctx context.Context, d *common.Deps, receiptNo string) error {
	cfg := printerConfig(ctx, d)
	if !cfg.Enabled() {
		return fmt.Errorf("no printer configured")
	}
	doc, err := buildReceiptDoc(ctx, d, receiptNo)
	if err != nil {
		return err
	}
	// PrintDoc renders ESC/POS for a thermal printer or plain text via lp for a
	// regular (system) printer, per the configured type.
	return print.PrintDoc(ctx, cfg, doc)
}

// clampCopies bounds a requested label copy count to [1, 50] — never zero
// (a blank/unparseable form field must still print something) and never
// unbounded (a typo like "999" must not queue an absurd print job).
func clampCopies(n int) int {
	if n < 1 {
		return 1
	}
	if n > 50 {
		return 50
	}
	return n
}

// registerPrintAPI mounts printer settings, test print and reprint.
func registerPrintAPI(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	// Printer settings (manager): mode/address/device/charset/auto-print.
	mux.HandleFunc("POST /api/settings/printer", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "settings") {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		mode := strings.TrimSpace(r.Form.Get("mode"))
		if mode != "off" && mode != "network" && mode != "device" && mode != "system" {
			http.Error(w, "mode must be off, network, device or system", http.StatusBadRequest)
			return
		}
		charset := strings.TrimSpace(r.Form.Get("charset"))
		if charset != "ascii" {
			charset = "utf8"
		}
		_ = d.Settings.Set(r.Context(), keyPrinterMode, mode)
		_ = d.Settings.Set(r.Context(), keyPrinterAddress, strings.TrimSpace(r.Form.Get("address")))
		_ = d.Settings.Set(r.Context(), keyPrinterDevice, strings.TrimSpace(r.Form.Get("device")))
		_ = d.Settings.Set(r.Context(), keyPrinterCharset, charset)
		_ = d.Settings.Set(r.Context(), keyPrinterKitchen, strings.TrimSpace(r.Form.Get("kitchenAddr")))
		auto := "false"
		if r.Form.Get("autoPrint") == "on" || r.Form.Get("autoPrint") == "1" {
			auto = "true"
		}
		_ = d.Settings.Set(r.Context(), keyPrinterAuto, auto)
		w.WriteHeader(http.StatusNoContent)
	})

	// Test print — the moment of truth for printer setup.
	mux.HandleFunc("POST /api/print/test", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "settings") {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		cfg := printerConfig(ctx, d)
		var err error
		if !cfg.Enabled() {
			err = fmt.Errorf("printer is off — pick a printer type first")
		}
		locale := httpx.ResolveLocale(w, r)
		if err == nil {
			doc := print.Doc{
				StoreName: storeNameOrDefault(ctx, d),
				Meta:      []string{"TEST PRINT", time.Now().Format("2006-01-02 15:04:05")},
				Totals:    []print.KV{{Label: "Printer", Amount: "OK", Strong: true}},
				Footer:    []string{"Universal Till"},
				Charset:   cfg.Charset,
			}
			err = print.PrintDoc(ctx, cfg, doc)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "settings.printer.test_failed")+": "+err.Error())
			return
		}
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "settings.printer.test_ok"))
	})

	// Product/shelf labels (docs: receipt-printing.md § G9). Any operator —
	// labelling shelves is normal staff work.
	mux.HandleFunc("POST /api/print/labels", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("item_id"))
		raw, _ := strconv.Atoi(strings.TrimSpace(r.Form.Get("copies")))
		copies := clampCopies(raw)
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fail := func(status int, key string) {
			w.WriteHeader(status)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, key))
		}
		// A variant label carries the VARIANT's price + barcode — a shelf
		// label for "Apples Large" must scan as the large apples.
		var label data.ItemLabel
		var found bool
		var err error
		if variantID := strings.TrimSpace(r.Form.Get("variant_id")); variantID != "" {
			var vl data.VariantLabel
			vl, found, err = data.NewCatalogRepo(d.Db).GetVariantLabel(r.Context(), variantID)
			label = data.ItemLabel{Name: vl.Name, PriceMinor: vl.PriceMinor, Code: vl.Code}
		} else {
			label, found, err = data.NewCatalogRepo(d.Db).GetItemLabel(r.Context(), itemID)
		}
		if err != nil || !found {
			fail(http.StatusNotFound, "catalog.labels.no_item")
			return
		}
		if strings.TrimSpace(label.Code) == "" {
			fail(http.StatusBadRequest, "catalog.labels.no_code")
			return
		}
		cfg := printerConfig(r.Context(), d)
		tr, terr := print.NewTransport(cfg)
		if terr != nil || tr == nil {
			fail(http.StatusBadGateway, "settings.printer.test_failed")
			return
		}
		one := print.RenderLabel(label.Name, httpx.FormatMoney(label.PriceMinor, "en"), label.Code, cfg.Charset)
		job := make([]byte, 0, len(one)*copies)
		for range copies {
			job = append(job, one...)
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := tr.Print(ctx, job); err != nil {
			fail(http.StatusBadGateway, "settings.printer.test_failed")
			return
		}
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "item", itemID, "labels_printed",
			map[string]any{"copies": copies, "code": label.Code}, time.Now().UTC().Format(time.RFC3339), "")
		fmt.Fprintf(w, `<span>✓ %s (%d)</span>`, httpx.T(locale, "catalog.labels.done"), copies)
	})

	// Reprint a receipt from the journal.
	mux.HandleFunc("POST /api/print/receipt/{receiptNo}", func(w http.ResponseWriter, r *http.Request) {
		receiptNo := r.PathValue("receiptNo")
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		err := printReceipt(ctx, d, receiptNo)
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "sale", receiptNo, "receipt_reprint",
			map[string]any{"ok": err == nil}, now, "")
		// Keep the /orders warning honest (ut-docs#517a), same as the manual
		// kitchen reprint. Without this the receipt warning would be
		// UNCLEARABLE: a receipt only auto-prints once, at tender, so the
		// reprint here is the only later attempt a shop can make — and the
		// manual tells them to make it ("Fix the printer and print that
		// order again — a successful print clears the warning").
		if err != nil {
			_ = posRepo.SetReceiptPrintFailed(r.Context(), receiptNo, now)
		} else {
			_ = posRepo.SetReceiptPrintFailed(r.Context(), receiptNo, "")
		}
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "settings.printer.test_failed"))
			return
		}
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "journal.reprinted"))
	})
}
