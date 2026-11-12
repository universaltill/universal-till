package pages

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#5410: a thermal receipt/Z-report never prints the Arabic dirham
// sign on a code page that lacks it (it printed '?'): the amounts carry the
// ISO code, with Latin digits and no bidi controls.
func TestBuildEODDoc_CurrencyCodeWhenSymbolUnprintable(t *testing.T) {
	defer httpx.SnapshotStateForTests()()
	httpx.InitCurrency("AED")
	httpx.InitCurrencyDisplay("symbol") // even the explicit symbol mode falls back
	httpx.SetDefaultLocale("ar")

	doc := buildEODDoc(labelsEODReport(), "Shop", "cp858", eodArticlePrintAll, 0)
	lines := eodPrintedLines(doc)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "AED 1,234.56") {
		t.Fatalf("Z-report has no 'AED 1,234.56' gross; lines:\n%s", joined)
	}
	if strings.Contains(joined, "د.إ") {
		t.Fatalf("Z-report carries the unprintable symbol:\n%s", joined)
	}
	for _, kv := range doc.Totals {
		for _, r := range kv.Amount {
			if r > 0x7f || r == '⁦' || r == '⁩' {
				t.Fatalf("total %q: non-ASCII %q in a currency amount", kv.Amount, r)
			}
		}
	}

	// utf8 passes bytes through, but a thermal head has no Arabic glyphs:
	// the code is printed there too (review of ut-docs#5410).
	doc = buildEODDoc(labelsEODReport(), "Shop", "utf8", eodArticlePrintAll, 0)
	if j := strings.Join(eodPrintedLines(doc), "\n"); !strings.Contains(j, "AED 1,234.56") || strings.Contains(j, "د.إ") {
		t.Fatalf("utf8 printer in symbol mode must print the code:\n%s", j)
	}

	// auto mode: the registry says AED prefers the code.
	httpx.InitCurrencyDisplay("auto")
	doc = buildEODDoc(labelsEODReport(), "Shop", "utf8", eodArticlePrintAll, 0)
	if j := strings.Join(eodPrintedLines(doc), "\n"); !strings.Contains(j, "AED 1,234.56") {
		t.Fatalf("auto mode should print the AED code:\n%s", j)
	}
}

// The sale receipt follows the same rule: AED on a cp858 printer prints the
// code with Latin digits, and no currency byte is above 0x7F.
func TestBuildReceiptDoc_CurrencyCodeOnCP858(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	setSeller(t, dp)
	seedMixedRateSale(t, dp, "sale-mix", "R-MIX", "2026-09-30T10:00:00Z")
	pinDefaultLocale(t, "en")
	defer httpx.SnapshotStateForTests()()
	httpx.InitCurrency("AED")
	httpx.InitCurrencyDisplay("symbol")
	if err := dp.Settings.Set(ctx, keyPrinterCharset, "cp858"); err != nil {
		t.Fatal(err)
	}

	doc, err := buildReceiptDoc(ctx, dp, "R-MIX")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Totals) == 0 {
		t.Fatal("receipt has no totals")
	}
	for _, kv := range doc.Totals {
		if strings.Contains(kv.Amount, "د.إ") {
			t.Fatalf("total %q carries the unprintable symbol", kv.Amount)
		}
	}
	found := false
	for _, kv := range doc.Totals {
		if strings.HasPrefix(strings.TrimPrefix(kv.Amount, "-"), "AED ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no 'AED ' amount among totals %+v", doc.Totals)
	}
}
