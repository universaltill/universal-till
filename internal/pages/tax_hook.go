package pages

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// taxRateAskEvent is the generic "compute a tax rate override" hook
// (EventBus.Ask). Any installed plugin — country-specific tax rules are
// entirely a plugin's job, core has none built in (see pos.TaxRateAsker) —
// may subscribe to it. The ".ask" suffix is what makes wasm_runtime.go
// dispatch it as a blocking, value-returning hook rather than fire-and-forget.
const taxRateAskEvent = "tax.rate.ask"

// taxRateAskPayload is the event payload a subscribing plugin receives.
type taxRateAskPayload struct {
	ItemID    string `json:"item_id"`
	TaxCodeID string `json:"tax_code_id"`
	TaxRateBP int    `json:"tax_rate_bp"`
	OrderType string `json:"order_type"`
}

// taxRateAskResponse is the JSON a plugin writes to stdout to answer.
type taxRateAskResponse struct {
	RateBP int `json:"rate_bp"`
}

// pluginTaxRateAsker implements pos.TaxRateAsker by asking installed
// plugins via the event bus — internal/pos itself never depends on the
// plugin subsystem, this is the seam where "does any plugin have an
// opinion on this line's tax rate" is answered.
type pluginTaxRateAsker struct {
	db *sql.DB
}

func (a *pluginTaxRateAsker) AskTaxRateBP(l pos.BasketLine, orderType string) (int, bool) {
	bus := plugins.SharedBus(a.db)
	if !bus.HasSubscribers(taxRateAskEvent) {
		return 0, false
	}
	resp, ok, err := bus.Ask(context.Background(), taxRateAskEvent, taxRateAskPayload{
		ItemID:    l.ItemID,
		TaxCodeID: l.TaxCodeID,
		TaxRateBP: l.TaxRateBP,
		OrderType: orderType,
	})
	if err != nil || !ok {
		return 0, false
	}
	var parsed taxRateAskResponse
	if json.Unmarshal(resp, &parsed) != nil || parsed.RateBP <= 0 {
		return 0, false
	}
	return parsed.RateBP, true
}
