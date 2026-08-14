package pages

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// ADR-0048 Decision 3 requires the override marker on the receipt the sale
// PRINTS, not only on the on-screen copy rendered at tender — the printed
// slip is what the customer keeps and the shop files, and web/help's
// fiscal-compliance topic promises the sale is marked "on its receipt" in
// all four shipped locales. buildReceiptDoc is a wholly separate render
// path from renderReceipt (ESC/POS via print.Doc, not the HTML template),
// so it needs its own coverage.
func fiscalPrintI18n(t *testing.T) {
	t.Helper()
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
}

func markFiscalOverride(t *testing.T, dp *common.Deps, saleID string) {
	t.Helper()
	if err := data.NewPOSRepo(dp.Db).InsertAudit(context.Background(), nil, "own1", "sale", saleID,
		"unsigned_override", map[string]any{"actor": "own1", "reason": "dongle failed"},
		time.Now().UTC().Format(time.RFC3339), ""); err != nil {
		t.Fatalf("seed unsigned_override audit: %v", err)
	}
}

func TestBuildReceiptDoc_UnsignedOverrideMarkerIsPrinted(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	fiscalPrintI18n(t)
	seedReceiptSale(t, dp, "sale-ov", "R-OV1", "sale", "", 120, 0, 0)
	markFiscalOverride(t, dp, "sale-ov")

	// Note what is deliberately NOT set up here: no fiscal.tse_override_*
	// settings at all. This IS the reprint-after-expiry case — the marker
	// must come from the sale's own audit trail, never from the current
	// override-window state.
	doc, err := buildReceiptDoc(context.Background(), dp, "R-OV1")
	if err != nil {
		t.Fatal(err)
	}
	marker := httpx.T("en", "receipt.fiscal.unsigned_override")
	if marker == "" || marker == "receipt.fiscal.unsigned_override" {
		t.Fatal("receipt.fiscal.unsigned_override missing from en.json")
	}
	joined := strings.Join(doc.Footer, " ")
	if joined != marker && !strings.Contains(joined, marker) {
		t.Fatalf("printed receipt must carry the override marker.\n got footer: %q\nwant text: %q", doc.Footer, marker)
	}
	// print.Doc's renderers CLIP footer lines at print.Width instead of
	// wrapping them, so an unwrapped sentence would print with its tail cut
	// off — losing the "no TSE signature" half, which is the whole point.
	for _, line := range doc.Footer {
		if n := utf8.RuneCountInString(line); n > print.Width {
			t.Fatalf("footer line would be clipped by the printer (%d > %d runes): %q", n, print.Width, line)
		}
	}
}

func TestBuildReceiptDoc_NoMarkerForAnOrdinarySale(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	fiscalPrintI18n(t)
	// Two sales: one taken during a window, one not. The ordinary sale must
	// never inherit the marker from its neighbour (SaleHasAuditAction is
	// keyed per sale, not per shop).
	seedReceiptSale(t, dp, "sale-ov", "R-OV1", "sale", "", 120, 0, 0)
	markFiscalOverride(t, dp, "sale-ov")
	seedReceiptSale(t, dp, "sale-ok", "R-OK1", "sale", "", 120, 0, 0)

	doc, err := buildReceiptDoc(context.Background(), dp, "R-OK1")
	if err != nil {
		t.Fatal(err)
	}
	marker := httpx.T("en", "receipt.fiscal.unsigned_override")
	if strings.Contains(strings.Join(doc.Footer, " "), marker) {
		t.Fatalf("a sale taken outside any override window must not be marked, got footer %q", doc.Footer)
	}
}
