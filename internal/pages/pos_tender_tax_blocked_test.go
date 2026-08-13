package pages

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#368: a tender attempt on a basket containing a line whose tax
// authority is broken must be refused with a clear translated message —
// never recorded at the silently-wrong base rate. The block is per-line and
// non-modal: removing the blocked line lets the rest of the basket complete.

// tenderBlockedAsker blocks exactly one tax code — standing in for the
// plugin-backed asker while the plugin owning that code is broken.
type tenderBlockedAsker struct{ blockedTaxCode string }

func (f tenderBlockedAsker) AskTaxRateBP(l pos.BasketLine, orderType string) (int, bool, bool) {
	return 0, false, l.TaxCodeID == f.blockedTaxCode
}

func countSalesRows(t *testing.T, dp *common.Deps) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&n); err != nil {
		t.Fatalf("count sales: %v", err)
	}
	return n
}

func TestTenderHandler_FailsClosedOnBlockedTaxLine(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	dp.Engine.SetTaxRateAsker(tenderBlockedAsker{blockedTaxCode: "tax_std"})

	// Two lines: the stub-resolved ABC (no tax code — never blocked) and a
	// second line carrying the blocked tax code (distinct ItemID, or
	// mergeResolved would fold it into the first line).
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	dp.Engine.AddLineWithModifiers(pos.BasketLine{
		SKU: "BLK", Name: "Blocked Widget", PriceCents: 200, ItemID: "itm2", TaxCodeID: "tax_std", TaxRateBP: 2000,
	}, 1, nil)

	salesBefore := countSalesRows(t, dp)

	rec := posPostForm(mux, "/api/pos/tender", "method=cash&amount=0")
	if rec.Code != 200 {
		t.Fatalf("expected the toast-rendered basket (200), got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Blocked Widget") || !strings.Contains(body, "tax plugin is being repaired") {
		t.Fatalf("expected the translated tax-unavailable toast naming the blocked line, got: %s", body)
	}
	if got := countSalesRows(t, dp); got != salesBefore {
		t.Fatalf("a blocked tender must record NO sale: sales %d -> %d", salesBefore, got)
	}
	// The basket survives (nothing was consumed) — the operator can fix it.
	if got := len(dp.Engine.Basket().Lines); got != 2 {
		t.Fatalf("basket must be untouched by the refusal, got %d lines", got)
	}

	// Narrow, per-line: removing the blocked line lets the rest complete.
	for _, l := range dp.Engine.Basket().Lines {
		if l.TaxCodeID == "tax_std" {
			dp.Engine.RemoveLine(l.LineKey)
		}
	}
	rec = posPostForm(mux, "/api/pos/tender", "method=cash&amount=0")
	if rec.Code != 200 {
		t.Fatalf("expected 200 for the unblocked remainder, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countSalesRows(t, dp); got != salesBefore+1 {
		t.Fatalf("the unblocked remainder must complete as a sale: sales %d -> %d", salesBefore, got)
	}
}
