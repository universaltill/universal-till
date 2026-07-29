package pages

import (
	"testing"

	"github.com/universaltill/universal-till/internal/pos"
)

// Compile-time proof the hook still satisfies the seam pos wires it into.
var _ pos.TaxRateAsker = (*pluginTaxRateAsker)(nil)

// With no plugin subscribed to tax.rate.ask, the asker must decline (0, false)
// so the POS engine falls back to the item's own tax code — core has no
// built-in country tax rules, this is entirely a plugin's job.
func TestAskTaxRateBP_NoSubscribers(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	asker := &pluginTaxRateAsker{db: db}
	rate, ok := asker.AskTaxRateBP(pos.BasketLine{
		ItemID:    "itm1",
		TaxCodeID: "tax_std",
		TaxRateBP: 2000,
	}, "eat_in")

	if ok {
		t.Fatalf("expected no plugin override, got ok=true")
	}
	if rate != 0 {
		t.Fatalf("expected rate 0 when declining, got %d", rate)
	}
}
