package pages

import (
	"fmt"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#2410 (review finding: the original version of this test compared
// a render against itself — rep.FiscalDevice was never set to anything but
// its own zero value on both sides, so it could never fail no matter what
// buildEODDoc did with a non-nil FiscalDevice). A real regression check
// needs an independent expectation: this fixture (Day/GeneratedAt/
// SalesCount/Gross only, every other EODReport field left at its zero
// value) produces a Doc with an EMPTY Footer on origin/main — confirmed by
// running buildEODDoc(rep, ...) for this exact fixture before this card's
// FiscalDevice section existed at all: no method/tax-band/voucher/article/
// operator/order-type breakdown has any data to print, so none of those
// sections emit a line, and len(doc.Footer) == 0. That is the independent
// baseline this test asserts against, not a re-derivation of the code
// under test.
func TestBuildEODDoc_FiscalDevice_NilOmitsSection(t *testing.T) {
	rep := data.EODReport{Day: "2026-09-10", GeneratedAt: "2026-09-10T21:00:00Z", SalesCount: 3, Gross: 1000}
	// rep.FiscalDevice is left nil.
	doc := buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)

	// Baseline established on origin/main for this exact fixture (see
	// comment above): none of the other footer sections have anything to
	// print, so the footer is empty absent any FiscalDevice contribution.
	const wantFooterLen = 0
	if len(doc.Footer) != wantFooterLen {
		t.Fatalf("Footer = %q (len %d), want len %d (this fixture's sibling sections print nothing on their own)", doc.Footer, len(doc.Footer), wantFooterLen)
	}
	for _, line := range doc.Footer {
		for _, marker := range []string{"FISCAL DEVICE", "Serial", "Maker", "Z no.", "Mali fis", "Till OKC", "MATCH", "NO DEVICE ACTIVITY"} {
			if strings.Contains(line, marker) {
				t.Fatalf("nil FiscalDevice must not print any device-related footer line, found %q in line %q", marker, line)
			}
		}
	}
	out := string(print.Render(doc))
	if strings.Contains(out, "FISCAL DEVICE") {
		t.Fatalf("nil FiscalDevice must not print a FISCAL DEVICE section, got:\n%s", out)
	}
}

func TestBuildEODDoc_FiscalDevice_Populated_Match(t *testing.T) {
	// Asymmetric, distinctive counts (review finding: 2/1/3 could hide a
	// swap between fields) so a MaliFis/IadeFisi/TillOKCTenders mixup would
	// actually fail this test.
	rep := data.EODReport{
		Day: "2026-09-10", GeneratedAt: "2026-09-10T21:00:00Z", SalesCount: 3, Gross: 1000,
		FiscalDevice: &data.FiscalDeviceWindow{
			Serial: "AV0001234", Maker: "beko", ZNos: []int64{7},
			MaliFis: 2, IadeFisi: 1, BilgiFisi: 0, Other: 0, Total: 3,
			TillOKCTenders: 3,
		},
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
	for _, want := range []string{
		"FISCAL DEVICE (OKC)", "AV0001234",
		footerRow("Maker", "beko"),
		fmt.Sprintf("%-20s %d", "Mali fis", 2),
		fmt.Sprintf("%-20s %d", "Iade fisi", 1),
		fmt.Sprintf("%-20s %d", "Till OKC tenders", 3),
		"MATCH",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("Z-report missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "MISMATCH") {
		t.Fatalf("device Total == TillOKCTenders must print MATCH, not MISMATCH:\n%s", out)
	}
	// Zero Bilgi fisi / Other -> omitted entirely (same convention as
	// GUTSCHEINE/STORNOS: no permanent zero line).
	if strings.Contains(out, "Bilgi fis") {
		t.Fatalf("zero Bilgi fisi must omit that line:\n%s", out)
	}
	if strings.Contains(out, "closed mid-period") {
		t.Fatalf("a single Z no. must not print the multi-Z hint:\n%s", out)
	}
}

func TestBuildEODDoc_FiscalDevice_Mismatch(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-09-10", GeneratedAt: "2026-09-10T21:00:00Z",
		FiscalDevice: &data.FiscalDeviceWindow{
			Serial: "AV0001234", Maker: "beko", ZNos: []int64{3, 4},
			MaliFis: 2, Total: 2, TillOKCTenders: 3,
		},
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
	if !strings.Contains(out, "MISMATCH (device 2 vs till 3)") {
		t.Fatalf("device Total (2) != TillOKCTenders (3) must print the exact mismatch sentence:\n%s", out)
	}
	if !strings.Contains(out, "3, 4 (closed mid-period)") {
		t.Fatalf("multi-Z hint missing for ZNos=[3,4]:\n%s", out)
	}
}

// ut-docs#2410 review finding: a FiscalDeviceWindow with no device receipts
// AND no till OKC tenders in the window is not the same thing as "device
// and till agree" — Total==TillOKCTenders==0 would otherwise print a false
// MATCH for a window where the device plugin might simply not have run at
// all. Empty() distinguishes that case; the footer prints one explicit
// "no activity" line instead of a table + MATCH.
func TestBuildEODDoc_FiscalDevice_Empty(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-09-10", GeneratedAt: "2026-09-10T21:00:00Z",
		FiscalDevice: &data.FiscalDeviceWindow{},
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
	if !strings.Contains(out, "FISCAL DEVICE (OKC)") {
		t.Fatalf("empty FiscalDevice must still print the section heading:\n%s", out)
	}
	if !strings.Contains(out, "NO DEVICE ACTIVITY") {
		t.Fatalf("empty FiscalDevice must print NO DEVICE ACTIVITY:\n%s", out)
	}
	if strings.Contains(out, "MATCH") {
		t.Fatalf("empty FiscalDevice must not print MATCH/MISMATCH, got:\n%s", out)
	}
	if strings.Contains(out, "Serial") {
		t.Fatalf("empty FiscalDevice must not print a Serial line, got:\n%s", out)
	}
}
