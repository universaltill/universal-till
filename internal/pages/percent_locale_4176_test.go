package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#4176: every percent a shop owner or customer reads -- the
// sale-screen discount label, the on-screen receipt, the invoice page, the
// printed invoice and the printed receipt's VAT lines -- follows the
// locale's decimal separator: 7,50% on a comma-locale till, 7.50% on en.

// pinDefaultLocale sets the shop (store) locale the print paths read for
// one test and restores the previous value afterwards.
func pinDefaultLocale(t *testing.T, locale string) {
	t.Helper()
	prev := httpx.DefaultLocale()
	httpx.SetDefaultLocale(locale)
	t.Cleanup(func() { httpx.SetDefaultLocale(prev) })
}

func TestPercentLocale_SaleScreenDiscountLabel(t *testing.T) {
	mux, dp := newPayButtonLocaleDigitsTestDeps(t)
	if _, err := dp.Engine.Scan("PLAIN"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	dp.Engine.SetDiscountPercent(750)
	for lang, want := range map[string]string{
		"en": "(7.50%)",
		"de": "(7,50\u00a0%)",
		"fa": "(۷٫۵۰٪)",
		"tr": "(%7,50)",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/basket?lang="+lang, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", lang, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, want) {
			t.Fatalf("%s: expected the discount label to carry %q", lang, want)
		}
	}
}

func TestPercentLocale_OnScreenReceiptDiscount(t *testing.T) {
	chdirRoot(t)
	lines := []pos.SaleLineInput{{Name: "Apple", Qty: 1, UnitPrice: 1000, TaxRateBasisPoints: 0}}
	payments := []pos.PaymentInput{{MethodID: "cash", Amount: 925}}
	for lang, want := range map[string]string{"en": "(7.50%)", "de": "(7,50\u00a0%)"} {
		html, err := renderReceipt(httpx.FuncsFor(lang), "R1", lines, payments, 1000, 0, 925, false, 75, "percent", 750, nil, false, false, false, false, nil, nil, "My Store", receiptDesign{ShowTax: true}, "", nil, "", nil, nil, "", "en")
		if err != nil {
			t.Fatalf("%s: renderReceipt: %v", lang, err)
		}
		if !strings.Contains(html, want) {
			t.Fatalf("%s: expected %q on the receipt, got: %s", lang, want, html)
		}
	}
}

func TestPercentLocale_InvoicePage(t *testing.T) {
	mux, dp := newInvoiceTestDeps(t)
	ctx := context.Background()
	setSeller(t, dp)
	seedMixedRateSale(t, dp, "sale-mix", "R-MIX", time.Now().UTC().Format(time.RFC3339))
	sale, _, err := data.NewPOSRepo(dp.Db).GetSaleDetail(ctx, "R-MIX")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := issueInvoice(ctx, dp, sale, "invoice", "", "Jane Doe", "", "", "user1")
	if err != nil {
		t.Fatal(err)
	}
	for lang, c := range map[string]struct{ want, notWant string }{
		"en": {"20.00%", "20,00%"},
		"de": {"20,00\u00a0%", "20.00%"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/invoice/"+inv.DisplayNo+"?lang="+lang, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", lang, rec.Code)
		}
		body := rec.Body.String()
		// The band table's Rate cell and each line's detail row.
		if n := strings.Count(body, c.want); n < 2 {
			t.Fatalf("%s: expected %q in the band table and the line detail (got %d)", lang, c.want, n)
		}
		if strings.Contains(body, c.notWant) {
			t.Fatalf("%s: found %q, the other locale's separator", lang, c.notWant)
		}
	}
}

func TestPercentLocale_PrintedInvoice(t *testing.T) {
	_, dp := newInvoiceTestDeps(t)
	ctx := context.Background()
	setSeller(t, dp)
	seedMixedRateSale(t, dp, "sale-mix", "R-MIX", time.Now().UTC().Format(time.RFC3339))
	sale, _, err := data.NewPOSRepo(dp.Db).GetSaleDetail(ctx, "R-MIX")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := issueInvoice(ctx, dp, sale, "invoice", "", "Jane Doe", "", "", "user1")
	if err != nil {
		t.Fatal(err)
	}
	for locale, want := range map[string]string{"en": "20.00%", "de": "20,00 %"} {
		pinDefaultLocale(t, locale)
		doc := buildInvoiceDoc(ctx, dp, inv, sale)
		band := false
		for _, kv := range doc.Totals {
			if strings.Contains(kv.Label, want+":") {
				band = true
			}
		}
		if !band {
			t.Fatalf("%s: expected a VAT band label with %q, got %+v", locale, want, doc.Totals)
		}
		sub := false
		for _, l := range doc.Lines {
			if strings.HasSuffix(l.Sub, " "+want) {
				sub = true
			}
		}
		if !sub {
			t.Fatalf("%s: expected a line detail ending in %q, got %+v", locale, want, doc.Lines)
		}
	}
}

func TestPercentLocale_PrintedReceiptVATLines(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	setSeller(t, dp)
	seedMixedRateSale(t, dp, "sale-mix", "R-MIX", "2026-09-30T10:00:00Z")
	// en stays byte-for-byte what it was; tr is a core locale, so the
	// label is translated too, not just the separator.
	for locale, want := range map[string]string{
		"en": "Total incl. VAT 20.00%",
		"tr": "KDV dahil toplam %20,00",
	} {
		pinDefaultLocale(t, locale)
		doc, err := buildReceiptDoc(ctx, dp, "R-MIX")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, kv := range doc.Totals {
			if kv.Label == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: expected a %q totals row, got %+v", locale, want, doc.Totals)
		}
	}
}

// ut-docs#4176 review: the Reports -> Tax tab's band rate sits beside
// locale-formatted money, so it follows the locale's separator too. Keeps
// the tab's short shape (7.5%, 20%) rather than FormatPercentBP's two
// decimals.
func TestPercentLocale_ReportsTaxTab(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at,completed_at) VALUES('s1','R001','completed','sale','GBP',1075,0,75,1075,datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id,sale_id,line_no,item_id,name_snapshot,quantity,unit_price,line_discount,tax_rate_bp,tax_amount,total_before_tax,total_after_tax) VALUES('s1-l1','s1',1,'itm1','Thing',1,1000,0,750,75,1000,1075)`); err != nil {
		t.Fatal(err)
	}
	for lang, c := range map[string]struct{ want, notWant string }{
		"en": {"7.5%", "7,5%"},
		"de": {"7,5\u00a0%", "7.5%"},
	} {
		rec := getReportsTab(t, mux, "tax", "?days=14&lang="+lang)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", lang, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, c.want) || strings.Contains(body, c.notWant) {
			t.Fatalf("%s: expected the band rate as %q, got: %s", lang, c.want, body)
		}
	}
}

// ut-docs#4176 review: the printed receipt's "<label> <rate>" row must fit
// the 42-column ESC/POS line next to its amount in every core locale, or
// print.kvRow clips the label and the rate -- the only thing telling the
// VAT bands apart -- is what falls off (the first ar wording did).
func TestPercentLocale_PrintedVATLabelFitsReceiptWidth(t *testing.T) {
	const amountAllowance = 11 // "£12.345,67" plus the 1-column gap
	for _, locale := range []string{"en", "ar", "fa", "tr"} {
		label := httpx.T(locale, "receipt.total_incl_vat") + " " + httpx.FormatPercentBP(2000, locale)
		if n := utf8.RuneCountInString(label); n+amountAllowance > print.Width {
			t.Errorf("%s: %q is %d columns; with an amount it overflows the %d-column receipt row", locale, label, n, print.Width)
		}
	}
}

// ut-docs#4281: the admin tax-codes table shows the rate in the locale's
// separator and sign placement, while data-rate (read by the edit JS) stays
// the raw machine value.
func TestPercentLocale_TaxCodesTable(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newTaxCodesTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tax_codes(id, name, rate_basis_points, is_active, takeaway_rate_basis_points) VALUES ('tax_81','Eight One',810,1,350)`); err != nil {
		t.Fatal(err)
	}
	for lang, c := range map[string]struct{ rate, takeaway string }{
		"en": {"<td>8.1%</td>", "<td>3.5%</td>"},
		"de": {"<td>8,1\u00a0%</td>", "<td>3,5\u00a0%</td>"},
		"tr": {"<td>%8,1</td>", "<td>%3,5</td>"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog/tax-codes?lang="+lang, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", lang, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{c.rate, c.takeaway, `data-rate="8.1"`, `data-takeaway="3.5"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s: expected %q in the table, got: %s", lang, want, body)
			}
		}
	}
}
