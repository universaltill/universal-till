package pages

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/print"
)

// VAT invoices + credit notes (G31, docs: architecture/invoicing.md).
// The engine lives in the host (numbering + refund coupling are
// transactional core); presentation becomes pluggable later.

const (
	keyInvoiceSellerName    = "invoice.seller_name"
	keyInvoiceSellerAddress = "invoice.seller_address"
	keyInvoiceSellerVATNo   = "invoice.seller_vat_no"
)

// invoiceSeller is the seller snapshot stored on every invoice.
type invoiceSeller struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	VATNo   string `json:"vat_no"`
}

// sellerConfig reads the seller identity; ok=false hides the whole feature.
func sellerConfig(ctx context.Context, d *common.Deps) (invoiceSeller, bool) {
	get := func(k string) string {
		v, _, _ := d.Settings.Get(ctx, k)
		return strings.TrimSpace(v)
	}
	s := invoiceSeller{
		Name:    get(keyInvoiceSellerName),
		Address: get(keyInvoiceSellerAddress),
		VATNo:   get(keyInvoiceSellerVATNo),
	}
	return s, s.Name != ""
}

// vatBand is one per-rate row of the invoice's VAT table.
type vatBand struct {
	RateBP int   `json:"rate_bp"`
	Net    int64 `json:"net"`
	Tax    int64 `json:"tax"`
	Gross  int64 `json:"gross"`
}

// vatBreakdown aggregates a sale's lines by their RECORDED tax rate —
// the sale's own tax signature, never today's settings. A whole-sale
// discount (sale_discounts, not folded into any line) is prorated across
// the bands so the invoice total equals what the customer actually paid,
// and a service charge — which since ADR-0061 carries its own VAT — is
// apportioned into the bands through pos.ApportionServiceChargeTax, the
// SAME shared function the tender path and fiscal.sign.ask use, so the
// invoice can never declare a different VAT on the charge than the sale
// actually collected. Every band's Net+Tax == Gross, and the bands' gross
// sums to sale.Total, both before and after this ADR.
func vatBreakdown(sale data.SaleDetail) []vatBand {
	inclusive := saleIsTaxInclusive(sale)
	byRate := map[int]*vatBand{}
	var grossSum int64
	for _, l := range sale.Lines {
		b, ok := byRate[l.TaxRateBP]
		if !ok {
			b = &vatBand{RateBP: l.TaxRateBP}
			byRate[l.TaxRateBP] = b
		}
		b.Gross += l.LineTotal
		b.Tax += l.TaxAmount
		b.Net += l.LineTotal - l.TaxAmount
		grossSum += l.LineTotal
	}
	out := make([]vatBand, 0, len(byRate))
	for _, b := range byRate {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RateBP < out[j].RateBP })

	if d := sale.DiscountTotal; d > 0 && grossSum > 0 {
		remaining := d
		for i := range out {
			share := d * out[i].Gross / grossSum
			if i == len(out)-1 {
				share = remaining // largest-remainder: the pennies land here
			}
			remaining -= share
			if inclusive {
				// Discount comes off the gross; re-derive net/tax at the
				// band's rate (mirrors the engine: total = subtotal − d).
				out[i].Gross -= share
				out[i].Net = out[i].Gross * 10000 / (10000 + int64(out[i].RateBP))
				out[i].Tax = out[i].Gross - out[i].Net
			} else {
				// Exclusive engine discounts the NET base and keeps line
				// tax as computed (total = subtotal − d + tax).
				out[i].Net -= share
				out[i].Gross = out[i].Net + out[i].Tax
			}
		}
	}
	// ADR-0061 Decision 2: the service charge carries VAT of its own,
	// apportioned across the sale's own rate bands (or taxed at the flat
	// basis the originating till's country plugin fixed, which rides the
	// sale row so a re-issued invoice matches the original). Folded in
	// here rather than left as an untaxed lump, so the VAT table declares
	// the charge's tax and the bands still sum to what the customer paid.
	if sale.ServiceCharge > 0 {
		lines := make([]pos.ChargeTaxLine, 0, len(sale.Lines))
		for _, l := range sale.Lines {
			// The band weights want each line's value in the sale's OWN
			// pricing mode -- gross when inclusive (the shared function
			// derives the true net itself), net when exclusive.
			net := l.LineTotal
			if !inclusive {
				net -= l.TaxAmount
			}
			lines = append(lines, pos.ChargeTaxLine{RateBP: l.TaxRateBP, Net: money.FromMinor(net)})
		}
		for _, b := range pos.ApportionServiceChargeTax(money.FromMinor(sale.ServiceCharge), lines, inclusive, sale.ServiceChargeTaxBasisBP) {
			idx := -1
			for i := range out {
				if out[i].RateBP == b.RateBP {
					idx = i
					break
				}
			}
			if idx < 0 {
				out = append(out, vatBand{RateBP: b.RateBP})
				idx = len(out) - 1
			}
			// b.Amount is in the sale's pricing mode, same as the charge:
			// inclusive -> it already contains b.Tax; exclusive -> the tax
			// rides on top.
			if inclusive {
				out[idx].Gross += b.Amount.Minor()
				out[idx].Net += b.Amount.Minor() - b.Tax.Minor()
			} else {
				out[idx].Net += b.Amount.Minor()
				out[idx].Gross += b.Amount.Minor() + b.Tax.Minor()
			}
			out[idx].Tax += b.Tax.Minor()
		}
		sort.Slice(out, func(i, j int) bool { return out[i].RateBP < out[j].RateBP })
	}
	return out
}

// issueInvoice creates an invoice (or credit note) for a sale.
func issueInvoice(ctx context.Context, d *common.Deps, sale data.SaleDetail, kind, origInvoiceID, name, address, vatNo, actor string) (data.InvoiceRow, error) {
	seller, ok := sellerConfig(ctx, d)
	if !ok {
		return data.InvoiceRow{}, fmt.Errorf("seller details not configured")
	}
	sellerJSON, _ := json.Marshal(seller)
	bands := vatBreakdown(sale)
	breakdownJSON, _ := json.Marshal(bands)
	var net, tax int64
	for _, b := range bands {
		net, tax = net+b.Net, tax+b.Tax
	}
	// ut-docs#72 / ADR-0061: GrossTotal must match the receipt/sale total or
	// the issued invoice understates what was charged. The service charge
	// used to be added here as an untaxed lump because vatBreakdown had no
	// band for it; since ADR-0061 it IS apportioned into the bands (with its
	// own VAT), so it is already inside net+tax -- adding it again here
	// would double-count it.
	gross := net + tax
	series, _, _ := d.Settings.Get(ctx, "sync.receipt_prefix")
	row, err := data.NewInvoiceRepo(d.Db).Create(ctx, data.InvoiceInput{
		Series: strings.TrimSpace(series), Kind: kind, SaleID: sale.ID,
		OriginalInvoiceID: origInvoiceID,
		CustomerName:      name, CustomerAddress: address, CustomerVATNo: vatNo,
		SellerJSON: string(sellerJSON),
		NetTotal:   net, TaxTotal: tax, GrossTotal: gross,
		VATBreakdownJSON: string(breakdownJSON),
		IssuedAt:         time.Now().UTC().Format(time.RFC3339), IssuedBy: actor,
	})
	if err != nil {
		return data.InvoiceRow{}, err
	}
	audit := "invoice_issued"
	if kind == "credit_note" {
		audit = "credit_note_issued"
	}
	_ = data.NewPOSRepo(d.Db).InsertAudit(ctx, nil, actor, "invoice", row.DisplayNo, audit,
		map[string]any{"sale": sale.ReceiptNo, "gross": row.GrossTotal},
		time.Now().UTC().Format(time.RFC3339), "")
	return row, nil
}

// maybeIssueCreditNote runs after a completed refund: if the ORIGINAL
// sale carries an invoice and no credit note exists yet, one is issued
// automatically in the same series (docs: invoicing.md).
func maybeIssueCreditNote(ctx context.Context, d *common.Deps, refundReceiptNo, originalSaleID, actor string) {
	if originalSaleID == "" {
		return
	}
	repo := data.NewInvoiceRepo(d.Db)
	orig, found, err := repo.BySale(ctx, originalSaleID, "invoice")
	if err != nil || !found {
		return
	}
	posRepo := data.NewPOSRepo(d.Db)
	refund, found, err := posRepo.GetSaleDetail(ctx, refundReceiptNo)
	if err != nil || !found {
		return
	}
	if _, exists, _ := repo.BySale(ctx, refund.ID, "credit_note"); exists {
		return
	}
	if _, err := issueInvoice(ctx, d, refund, "credit_note", orig.ID,
		orig.CustomerName, orig.CustomerAddress, orig.CustomerVATNo, actor); err != nil {
		logging.L().Errorf("credit note for %s: %v", refundReceiptNo, err)
	}
}

// buildInvoiceDoc renders an invoice/credit note for the thermal printer.
func buildInvoiceDoc(ctx context.Context, d *common.Deps, inv data.InvoiceRow, sale data.SaleDetail) print.Doc {
	locale := httpx.DefaultLocale()
	T := func(k string) string { return httpx.T(locale, k) }
	var seller invoiceSeller
	_ = json.Unmarshal([]byte(inv.SellerJSON), &seller)
	var bands []vatBand
	_ = json.Unmarshal([]byte(inv.VATBreakdownJSON), &bands)

	// A VAT/TRN-registered seller issues a "Tax Invoice" (FTA/UAE, EU, Turkey
	// all require that wording); an unregistered seller issues a plain invoice.
	title := T("invoice.doc_title")
	if seller.VATNo != "" {
		title = T("invoice.doc_tax_title")
	}
	if inv.Kind == "credit_note" {
		title = T("invoice.doc_credit_title")
	}
	header := []string{seller.Name}
	if seller.Address != "" {
		header = append(header, seller.Address)
	}
	if seller.VATNo != "" {
		header = append(header, T("invoice.vat_no")+": "+seller.VATNo)
	}
	header = append(header, "")
	header = append(header, T("invoice.bill_to")+": "+inv.CustomerName)
	if inv.CustomerAddress != "" {
		header = append(header, inv.CustomerAddress)
	}
	if inv.CustomerVATNo != "" {
		header = append(header, T("invoice.vat_no")+": "+inv.CustomerVATNo)
	}

	meta := []string{inv.DisplayNo, T("invoice.for_sale") + " " + sale.ReceiptNo, inv.IssuedAt}
	if inv.Kind == "credit_note" && inv.OriginalInvoiceID != "" {
		meta = append(meta, T("invoice.credits")+" "+inv.OriginalInvoiceID)
	}

	doc := print.Doc{
		StoreName: title,
		Header:    header,
		Meta:      meta,
		Barcode:   inv.DisplayNo,
		Charset:   printerConfig(ctx, d).Charset,
	}
	for _, l := range sale.Lines {
		doc.Lines = append(doc.Lines, print.Line{
			Name:   l.Name,
			Qty:    formatQty(l.Qty),
			Amount: httpx.FormatMoney(l.LineTotal, locale),
		})
	}
	for _, b := range bands {
		doc.Totals = append(doc.Totals, print.KV{
			Label:  fmt.Sprintf("%s %.2f%%: %s + %s", T("invoice.vat"), float64(b.RateBP)/100, httpx.FormatMoney(b.Net, locale), httpx.FormatMoney(b.Tax, locale)),
			Amount: httpx.FormatMoney(b.Gross, locale),
		})
	}
	doc.Totals = append(doc.Totals,
		print.KV{Label: T("invoice.net"), Amount: httpx.FormatMoney(inv.NetTotal, locale)},
		print.KV{Label: T("invoice.tax"), Amount: httpx.FormatMoney(inv.TaxTotal, locale)},
		print.KV{Label: T("invoice.total"), Amount: httpx.FormatMoney(inv.GrossTotal, locale)},
	)
	return doc
}

func registerInvoices(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)
	invRepo := data.NewInvoiceRepo(d.Db)

	// Seller identity (manager). Configuring it turns the feature on.
	// ut-docs#866: checkOrElevate/InsertAuditElevated (#557/#796) — same
	// mechanism as settings_page.go's own sites. ParseForm moved ahead of
	// the gate solely so override_pin is readable (ut-docs#796 convention).
	mux.HandleFunc("POST /api/settings/invoice", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sellerName := strings.TrimSpace(r.Form.Get("seller_name"))
		sellerAddress := strings.TrimSpace(r.Form.Get("seller_address"))
		sellerVATNo := strings.TrimSpace(r.Form.Get("seller_vat_no"))
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, "/api/settings/invoice", "#invoice-settings-msg",
				httpx.T(locale, "elevation.summary.invoice_seller"),
				[]elevationHiddenField{
					{Name: "seller_name", Value: sellerName},
					{Name: "seller_address", Value: sellerAddress},
					{Name: "seller_vat_no", Value: sellerVATNo},
				}, elev)
			return
		}
		_ = d.Settings.Set(r.Context(), keyInvoiceSellerName, sellerName)
		_ = d.Settings.Set(r.Context(), keyInvoiceSellerAddress, sellerAddress)
		_ = d.Settings.Set(r.Context(), keyInvoiceSellerVATNo, sellerVATNo)
		settingsAudit(r, posRepo, elev, "settings", "invoice", "invoice_seller_updated", nil)
		settingsRespondSaved(w, r, elev)
	})

	// Issue an invoice for a completed sale (idempotent: an existing
	// invoice is returned, never duplicated).
	mux.HandleFunc("POST /api/invoices/issue", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		locale := httpx.ResolveLocale(w, r)
		receiptNo := strings.TrimSpace(r.Form.Get("receipt_no"))
		name := strings.TrimSpace(r.Form.Get("customer_name"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if receiptNo == "" || name == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "invoice.customer_required"))
			return
		}
		sale, found, err := posRepo.GetSaleDetail(r.Context(), receiptNo)
		if err != nil || !found || sale.SaleType != "sale" || sale.Status != "completed" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "invoice.not_invoiceable"))
			return
		}
		if existing, ok, _ := invRepo.BySale(r.Context(), sale.ID, "invoice"); ok {
			fmt.Fprintf(w, `<span>✓ %s <a href="/invoice/%s"><strong>%s</strong></a></span>`,
				httpx.T(locale, "invoice.already_issued"), existing.DisplayNo, existing.DisplayNo)
			return
		}
		inv, err := issueInvoice(r.Context(), d, sale, "invoice", "",
			name, strings.TrimSpace(r.Form.Get("customer_address")),
			strings.TrimSpace(r.Form.Get("customer_vat_no")), getSessionUserID(r))
		if err != nil {
			logging.L().Errorf("[invoice] issue: %v", err)
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "invoice.error.issue_failed"))
			return
		}
		// Best-effort thermal print, same posture as receipts. Tracked on
		// d.AsyncWork (ut-docs#425), same reasoning as printReceiptAsync.
		d.AsyncWork.Add(1)
		go func() {
			defer d.AsyncWork.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cfg := printerConfig(ctx, d)
			if !cfg.Enabled() {
				return
			}
			_ = print.PrintDoc(ctx, cfg, buildInvoiceDoc(ctx, d, inv, sale))
		}()
		fmt.Fprintf(w, `<span>✓ %s <a href="/invoice/%s"><strong>%s</strong></a></span>`,
			httpx.T(locale, "invoice.issued"), inv.DisplayNo, inv.DisplayNo)
	})

	// Invoice register (manager): date-range list + accountant CSV.
	mux.HandleFunc("GET /invoices", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "reports") {
			http.Redirect(w, r, "/journal", http.StatusSeeOther)
			return
		}
		if _, on := sellerConfig(r.Context(), d); !on {
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
			return
		}
		from := strings.TrimSpace(r.URL.Query().Get("from"))
		to := strings.TrimSpace(r.URL.Query().Get("to"))
		list, err := invRepo.List(r.Context(), from, to)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "invoice.error.server", "invoice", err)
			return
		}
		var net, tax, gross int64
		for _, it := range list {
			sign := int64(1)
			if it.Kind == "credit_note" {
				sign = -1
			}
			net += sign * it.NetTotal
			tax += sign * it.TaxTotal
			gross += sign * it.GrossTotal
		}
		httpx.Render("ui/pages/invoices.html", map[string]any{
			"title":     "Invoices",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"Items":     list,
			"From":      from,
			"To":        to,
			"NetSum":    net,
			"TaxSum":    tax,
			"GrossSum":  gross,
		})(w, r)
	})

	// Accountant handoff: the same range as CSV (credit notes negative).
	mux.HandleFunc("GET /api/invoices/export", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "reports") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		from := strings.TrimSpace(r.URL.Query().Get("from"))
		to := strings.TrimSpace(r.URL.Query().Get("to"))
		list, err := invRepo.List(r.Context(), from, to)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "invoice.error.server", "invoice", err)
			return
		}
		decimals := httpx.ActiveCurrency().Decimals
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition",
			`attachment; filename="invoices-`+time.Now().UTC().Format("2006-01-02")+`.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"Invoice", "Kind", "Issued", "Customer", "Customer VAT no",
			"Receipt", "Net", "VAT", "Gross"})
		for _, it := range list {
			sign := int64(1)
			if it.Kind == "credit_note" {
				sign = -1
			}
			// CustomerName/CustomerVATNo are free-typed by whoever issues an
			// invoice. DisplayNo/ReceiptNo LOOK system-generated but aren't
			// fully: both embed sync.receipt_prefix (data/invoice_repo.go,
			// pos_repo.go's nextReceiptNo), a setting writable as free text
			// via the generic /api/settings/upsert with no allowlist — so
			// they need csvSafe too (ut-docs#195 review). Kind/IssuedAt are
			// genuine Go literals/RFC3339 timestamps; the amounts are
			// legitimately signed (a credit note's gross is negative), so
			// blanket-sanitizing the whole row would corrupt them into text
			// instead of numbers — csvSafe stays field-scoped, not row-wide.
			_ = cw.Write([]string{csvSafe(it.DisplayNo), it.Kind, it.IssuedAt, csvSafe(it.CustomerName),
				csvSafe(it.CustomerVATNo), csvSafe(it.ReceiptNo),
				minorToDecimal(sign*it.NetTotal, decimals),
				minorToDecimal(sign*it.TaxTotal, decimals),
				minorToDecimal(sign*it.GrossTotal, decimals)})
		}
		cw.Flush()
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "invoice", "-", "invoices_exported",
			map[string]any{"rows": len(list), "from": from, "to": to},
			time.Now().UTC().Format(time.RFC3339), "")
	})

	// On-screen invoice — the browser-printable "PDF" of v1.
	mux.HandleFunc("GET /invoice/{display_no}", func(w http.ResponseWriter, r *http.Request) {
		inv, found, err := invRepo.ByDisplayNo(r.Context(), r.PathValue("display_no"))
		if err != nil || !found {
			http.NotFound(w, r)
			return
		}
		sale, _, err := posRepo.GetSaleDetailByID(r.Context(), inv.SaleID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "invoice.error.server", "invoice", err)
			return
		}
		var seller invoiceSeller
		_ = json.Unmarshal([]byte(inv.SellerJSON), &seller)
		var raw []vatBand
		_ = json.Unmarshal([]byte(inv.VATBreakdownJSON), &raw)
		type bandView struct {
			Rate            string
			Net, Tax, Gross int64
		}
		bands := make([]bandView, 0, len(raw))
		for _, b := range raw {
			bands = append(bands, bandView{
				Rate: fmt.Sprintf("%.2f%%", float64(b.RateBP)/100),
				Net:  b.Net, Tax: b.Tax, Gross: b.Gross,
			})
		}
		origDisplay := ""
		if inv.OriginalInvoiceID != "" {
			if o, ok, _ := invRepo.ByID(r.Context(), inv.OriginalInvoiceID); ok {
				origDisplay = o.DisplayNo
			}
		}
		httpx.Render("ui/pages/invoice.html", map[string]any{
			"title":        inv.DisplayNo,
			"theme":        d.CurrentState().Theme,
			"menuItems":    d.MenuSnapshot(),
			"Inv":          inv,
			"Sale":         sale,
			"Seller":       seller,
			"Bands":        bands,
			"OrigDisplay":  origDisplay,
			"IsCreditNote": inv.Kind == "credit_note",
		})(w, r)
	})
}

func formatQty(q float64) string {
	if q == float64(int64(q)) {
		return fmt.Sprintf("%d", int64(q))
	}
	return fmt.Sprintf("%.3g", q)
}
