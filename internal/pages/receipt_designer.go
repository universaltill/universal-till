package pages

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// designFromForm reads UNSAVED designer values (live preview posts these).
func designFromForm(r *http.Request) receiptDesign {
	rd := receiptDesign{
		Footer:      strings.TrimSpace(r.Form.Get("footer")),
		ShowSKU:     r.Form.Get("show_sku") == "on",
		ShowTax:     r.Form.Get("show_tax") == "on",
		ShowBarcode: r.Form.Get("show_barcode") == "on",
		ShowLogo:    r.Form.Get("show_logo") == "on",
	}
	for _, f := range []string{"header1", "header2", "header3"} {
		if v := strings.TrimSpace(r.Form.Get(f)); v != "" {
			rd.Header = append(rd.Header, v)
		}
	}
	return rd
}

// sampleReceiptDoc builds the designer's example ticket from a design.
func sampleReceiptDoc(storeName string, rd receiptDesign) print.Doc {
	line := func(name, sku, qty, amount string) print.Line {
		if rd.ShowSKU && sku != "" {
			name += " [" + sku + "]"
		}
		return print.Line{Name: name, Qty: qty, Amount: amount}
	}
	doc := print.Doc{
		StoreName: storeName,
		Header:    rd.Header,
		Meta:      []string{"Receipt 000000123", time.Now().Format("2006-01-02 15:04")},
		Lines: []print.Line{
			line("Apples", "SKU-001", "2", "£4.32"),
			line("Orange Juice 1L", "SKU-002", "1", "£2.64"),
		},
		Payments: []print.KV{{Label: "Cash", Amount: "£10.00"}, {Label: "Change", Amount: "£3.04"}},
	}
	if rd.ShowTax {
		doc.Totals = []print.KV{
			{Label: "Subtotal", Amount: "£5.80"},
			{Label: "Tax", Amount: "£1.16"},
		}
	}
	doc.Totals = append(doc.Totals, print.KV{Label: "TOTAL", Amount: "£6.96", Strong: true})
	if rd.ShowBarcode {
		doc.Barcode = "000000123"
	}
	if rd.Footer != "" {
		doc.Footer = []string{rd.Footer}
	}
	return doc
}

// registerReceiptDesigner mounts the designer page, live preview, save and
// test print (docs: architecture/receipt-designer.md, G29).
func registerReceiptDesigner(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("GET /receipt-designer", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
			return
		}
		rd := receiptDesignFromSettings(r.Context(), d)
		header := make([]string, 3)
		copy(header, rd.Header)
		logoStat, logoErr := os.Stat(receiptLogoPath())
		logoV := int64(0)
		if logoErr == nil {
			logoV = logoStat.ModTime().Unix()
		}
		httpx.Render("ui/pages/receipt_designer.html", map[string]any{
			"title":     "Receipt designer",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"D":         rd,
			"H1":        header[0], "H2": header[1], "H3": header[2],
			"HasLogo": logoErr == nil,
			"LogoV":   logoV,
			"Preview": print.RenderText(sampleReceiptDoc(storeNameOrDefault(r.Context(), d), rd)),
		})(w, r)
	})

	mux.HandleFunc("POST /api/receipt-designer/preview", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		rd := designFromForm(r)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(print.RenderText(sampleReceiptDoc(storeNameOrDefault(r.Context(), d), rd))))
	})

	mux.HandleFunc("POST /api/receipt-designer/save", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		set := func(key, val string) { _ = d.Settings.Set(r.Context(), key, val) }
		set(keyReceiptHeader1, strings.TrimSpace(r.Form.Get("header1")))
		set(keyReceiptHeader2, strings.TrimSpace(r.Form.Get("header2")))
		set(keyReceiptHeader3, strings.TrimSpace(r.Form.Get("header3")))
		set(keyReceiptFooter, strings.TrimSpace(r.Form.Get("footer")))
		set(keyReceiptShowSKU, fmt.Sprintf("%t", r.Form.Get("show_sku") == "on"))
		set(keyReceiptShowTax, fmt.Sprintf("%t", r.Form.Get("show_tax") == "on"))
		set(keyReceiptShowBarcode, fmt.Sprintf("%t", r.Form.Get("show_barcode") == "on"))
		set(keyReceiptShowLogo, fmt.Sprintf("%t", r.Form.Get("show_logo") == "on"))
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "settings", "receipt", "receipt_design_saved",
			nil, time.Now().UTC().Format(time.RFC3339), "")
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "designer.receipt.saved"))
	})

	// Logo upload (manager): PNG/JPEG/GIF, validated by actually encoding
	// it; remove=1 deletes. The raster prints only when show_logo is on.
	mux.HandleFunc("POST /api/receipt-designer/logo", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			http.Error(w, "invalid upload", http.StatusBadRequest)
			return
		}
		if r.FormValue("remove") == "1" {
			_ = os.Remove(receiptLogoPath())
			_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "settings", "receipt", "receipt_logo_removed",
				nil, time.Now().UTC().Format(time.RFC3339), "")
			fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "designer.receipt.logo_removed"))
			return
		}
		file, _, err := r.FormFile("logo")
		if err != nil {
			http.Error(w, "logo file required", http.StatusBadRequest)
			return
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, 4<<20))
		if err != nil || print.RasterLogo(raw) == nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "designer.receipt.logo_invalid"))
			return
		}
		// The logo dir is never pre-created on a fresh install (unlike
		// item images, plugin uploads, and backups, which each MkdirAll
		// their own target dir) -- without this the FIRST logo upload
		// ever attempted on a new till fails with ENOENT.
		if err := os.MkdirAll(filepath.Dir(receiptLogoPath()), 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(receiptLogoPath(), raw, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "settings", "receipt", "receipt_logo_uploaded",
			map[string]any{"bytes": len(raw)}, time.Now().UTC().Format(time.RFC3339), "")
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "designer.receipt.logo_saved"))
	})

	mux.HandleFunc("POST /api/receipt-designer/test", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		rd := designFromForm(r)
		cfg := printerConfig(r.Context(), d)
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !cfg.Enabled() {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "settings.printer.test_failed"))
			return
		}
		doc := sampleReceiptDoc(storeNameOrDefault(r.Context(), d), rd)
		doc.Charset = cfg.Charset
		doc.Logo = receiptLogoRaster(rd)
		// PrintDoc picks ESC/POS (thermal) or plain text via lp (a regular
		// printer like an HP) based on the configured printer type.
		if perr := print.PrintDoc(r.Context(), cfg, doc); perr != nil {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "settings.printer.test_failed"))
			return
		}
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "settings.printer.test_ok"))
	})
}
