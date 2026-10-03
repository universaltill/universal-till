package pages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// The print_report hook (ut-docs#2537): the owner printed a cloud report
// on THIS till. cloudsync.Tick only hands it a directive addressed to this
// till's own device id, already decoded and bounded
// (cloudsync.decodePrintReport). It lives here, not in internal/cloudsync,
// for the same reason every other hook does: this package owns the printer
// configuration (printerConfigChecked), the store name and the receipt/EOD
// doc builders, and cloudsync stays free of internal/print and settings
// reads.
//
// The till is a renderer here, not a report engine (ut-docs ADR-0095 Notes
// "print_report"): every figure arrives pre-formatted with Latin digits,
// and nothing is recomputed or reformatted. The layout is the EOD report's
// (buildEODDoc): store name, meta lines, headline figures as Totals, then a
// headed table as footer rows (footerRow clips the label, never the
// figures). Printed-report vocabulary — the titles, column heads and the
// "+N more" line — is fixed English, never T(), the same convention
// buildEODDoc's own sections follow.

// printReportLayouts are the title and column heads of the kinds ut-cloud
// sends (claims.PrintReportKinds); the heads name the values in the order
// ut-cloud's shapePrintReport writes them. A kind not listed here (a newer
// cloud) prints titled by its id, without column heads.
var printReportLayouts = map[string]struct {
	title string
	heads []string
}{
	"overview": {"SALES OVERVIEW", []string{"Hour", "Sales", "Net"}},
	"tills":    {"SALES BY TILL", []string{"Till", "Sales", "Net"}},
	"cashiers": {"SALES BY CASHIER", []string{"Staff", "Sales", "Net"}},
	"items":    {"SALES BY ITEM", []string{"Item", "Qty", "Share"}},
	"payments": {"PAYMENTS", []string{"Method", "Amount", "Tips"}},
	"vat":      {"VAT", []string{"Rate", "Net", "VAT"}},
}

// printReportColWidth right-aligns each value column: wide enough for a
// six-figure amount with its symbol, so two columns leave the label 20 of
// the roll's 42.
const printReportColWidth = 10

// printReportPrintTimeout bounds one print, as the EOD print's.
const printReportPrintTimeout = 15 * time.Second

// errNoReceiptPrinter: print_report fails rather than reporting "applied"
// when nothing would come out (print.PrintDoc treats mode off as success).
var errNoReceiptPrinter = errors.New("no receipt printer is configured on this till")

// cloudPrintReport prints r on this till's receipt printer and reports
// "printed", or fails with the reason the owner sees in my.
func cloudPrintReport(ctx context.Context, d *common.Deps, r cloudsync.PrintReport) (string, error) {
	cfg, err := printerConfigChecked(ctx, d)
	if err != nil {
		return "", fmt.Errorf("could not read the printer settings: %w", err)
	}
	if !cfg.Enabled() {
		return "", errNoReceiptPrinter
	}
	doc := buildPrintReportDoc(r, storeNameOrDefault(ctx, d), cfg.Charset)
	pctx, cancel := context.WithTimeout(ctx, printReportPrintTimeout)
	defer cancel()
	if err := print.PrintDoc(pctx, cfg, doc); err != nil {
		return "", fmt.Errorf("print failed: %w", err)
	}
	return "printed", nil
}

// buildPrintReportDoc lays r out for the 80mm receipt printer. Pure.
func buildPrintReportDoc(r cloudsync.PrintReport, storeName, charset string) print.Doc {
	layout, known := printReportLayouts[r.ReportKind]
	title := layout.title
	if !known {
		title = strings.ToUpper(r.ReportKind)
	}
	now := time.Now()
	doc := print.Doc{
		StoreName: storeName,
		Meta: []string{
			title,
			r.PeriodLabel,
			// Same "Generated <date> <time>" shape as buildEODDoc, Latin
			// digits for the same ESC/POS reason; fits one 42-column row.
			"Cloud report, printed " + httpx.FormatDateLatin(now, httpx.DefaultLocale()) + " " + now.Format("15:04"),
		},
		Charset: charset,
	}
	for _, k := range r.KPIs {
		doc.Totals = append(doc.Totals, print.KV{Label: k.Label, Amount: k.Value})
	}
	if len(r.Rows) > 0 {
		if known {
			doc.Footer = append(doc.Footer, footerRow(layout.heads[0], printReportCols(layout.heads[1:])))
		}
		for _, row := range r.Rows {
			doc.Footer = append(doc.Footer, footerRow(row.Label, printReportCols(row.Values)))
		}
	}
	if r.TruncatedCount > 0 {
		// Fixed vocabulary, as buildEODDoc's "+N more (see on-screen
		// report)": the full list is in the cloud manager.
		doc.Footer = append(doc.Footer, fmt.Sprintf("+%d more (see cloud manager)", r.TruncatedCount))
	}
	return doc
}

// printReportCols right-aligns each value in a printReportColWidth column
// (a longer value keeps its full width; footerRow then clips the label).
// The first column keeps its padding, so a clipped label never runs into
// the first figure and the columns line up under their heads.
func printReportCols(values []string) string {
	var b strings.Builder
	for i, v := range values {
		if i > 0 {
			b.WriteByte(' ')
		}
		if pad := printReportColWidth - utf8.RuneCountInString(v); pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
		b.WriteString(v)
	}
	return b.String()
}
