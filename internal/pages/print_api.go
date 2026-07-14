package pages

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// Printer settings keys (docs: architecture/receipt-printing.md).
const (
	keyPrinterMode    = "printer.mode"     // off | network | device
	keyPrinterAddress = "printer.address"  // host[:port]
	keyPrinterDevice  = "printer.device"   // /dev/usb/lp0
	keyPrinterCharset = "printer.charset"  // utf8 | ascii
	keyPrinterAuto    = "printer.auto_print"
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
		Mode:      get(keyPrinterMode, "off"),
		Address:   get(keyPrinterAddress, ""),
		Device:    get(keyPrinterDevice, ""),
		Charset:   get(keyPrinterCharset, "utf8"),
		AutoPrint: get(keyPrinterAuto, "true") == "true",
	}
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
	locale := "en" // receipts print with latin digits; RTL needs bitmap mode (spec)
	money := func(minor int64) string { return httpx.FormatMoney(minor, locale) }

	doc := print.Doc{
		StoreName: storeNameOrDefault(ctx, d),
		Meta: []string{
			"Receipt " + detail.ReceiptNo,
			detail.CreatedAt,
		},
		// Scan-to-refund (G28): the receipt number as a barcode.
		Barcode: detail.ReceiptNo,
		Charset: cfg.Charset,
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
		doc.Lines = append(doc.Lines, print.Line{Name: l.Name, Qty: qty, Amount: money(l.LineTotal)})
	}
	doc.Totals = []print.KV{{Label: "Subtotal", Amount: money(detail.Subtotal)}}
	if detail.DiscountTotal != 0 {
		doc.Totals = append(doc.Totals, print.KV{Label: "Discount", Amount: "-" + money(detail.DiscountTotal)})
	}
	doc.Totals = append(doc.Totals,
		print.KV{Label: "Tax", Amount: money(detail.TaxTotal)},
		print.KV{Label: "TOTAL", Amount: money(detail.Total), Strong: true},
	)
	for _, p := range detail.Payments {
		doc.Payments = append(doc.Payments, print.KV{Label: strings.ToUpper(p.Method[:1]) + p.Method[1:], Amount: money(p.Amount)})
		if p.ChangeGiven > 0 {
			doc.Payments = append(doc.Payments, print.KV{Label: "Change", Amount: money(p.ChangeGiven)})
		}
		if p.Method == "cash" {
			doc.KickDrawer = true
		}
	}
	doc.Footer = []string{"Thank you!"}
	return doc, nil
}

// printReceiptAsync prints a completed sale without ever blocking checkout:
// fired as a goroutine after tender, failures are logged + audited only.
func printReceiptAsync(d *common.Deps, receiptNo string, actorID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cfg := printerConfig(ctx, d)
		if !cfg.Enabled() || !cfg.AutoPrint {
			return
		}
		if err := printReceipt(ctx, d, receiptNo); err != nil {
			posRepo := data.NewPOSRepo(d.Db)
			_ = posRepo.InsertAudit(ctx, nil, actorID, "sale", receiptNo, "print_failed",
				map[string]any{"error": err.Error()}, time.Now().UTC().Format(time.RFC3339), "")
		}
	}()
}

func printReceipt(ctx context.Context, d *common.Deps, receiptNo string) error {
	cfg := printerConfig(ctx, d)
	tr, err := print.NewTransport(cfg)
	if err != nil {
		return err
	}
	if tr == nil {
		return fmt.Errorf("no printer configured")
	}
	doc, err := buildReceiptDoc(ctx, d, receiptNo)
	if err != nil {
		return err
	}
	return tr.Print(ctx, print.Render(doc))
}

// registerPrintAPI mounts printer settings, test print and reprint.
func registerPrintAPI(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	// Printer settings (manager): mode/address/device/charset/auto-print.
	mux.HandleFunc("POST /api/settings/printer", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		mode := strings.TrimSpace(r.Form.Get("mode"))
		if mode != "off" && mode != "network" && mode != "device" {
			http.Error(w, "mode must be off, network or device", http.StatusBadRequest)
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
		auto := "false"
		if r.Form.Get("autoPrint") == "on" || r.Form.Get("autoPrint") == "1" {
			auto = "true"
		}
		_ = d.Settings.Set(r.Context(), keyPrinterAuto, auto)
		w.WriteHeader(http.StatusNoContent)
	})

	// Test print — the moment of truth for printer setup.
	mux.HandleFunc("POST /api/print/test", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		cfg := printerConfig(ctx, d)
		tr, err := print.NewTransport(cfg)
		if err == nil && tr == nil {
			err = fmt.Errorf("printer is off — pick network or device first")
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
			err = tr.Print(ctx, print.Render(doc))
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "settings.printer.test_failed")+": "+err.Error())
			return
		}
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "settings.printer.test_ok"))
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
