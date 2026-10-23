package pages

import (
	"context"
	"fmt"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// cloudApplyChannelOrder is cloudsync.Hooks.ApplyChannelOrder (ADR-0169 §9,
// ut-docs#4768): record a channel order and move stock. The order is NOT a
// till sale: nothing here touches sales, payments, the drawer, the Z report
// or the fiscal journal. Idempotent on the order's revision. Checkout never
// waits on it.
//
// Any error returned here is treated as transient by cloudsync (the directive
// stays pending and is retried); only the decoder's refusals are final.
//
// Stock going negative, a line with no matching item and a total that
// disagrees with the lines are applied and recorded, then raised as Problems
// (the shop must see them; the stock really left at the channel) — they
// never refuse the directive.
func cloudApplyChannelOrder(ctx context.Context, d *common.Deps, o cloudsync.ChannelOrderDirective) (string, error) {
	in := o.ChannelOrderInput // carries TotalMismatch to the repo
	in.ActorID = "system"
	// The till's own currency setting (what its sales and receipts use), not
	// the cloud's cache of the shop currency. Recomputed here on every
	// revision, so the flag follows a currency change.
	in.CurrencyMismatch = httpx.ActiveCurrency().Code != in.Currency
	locs, err := data.NewCatalogRepo(d.Db).ReadLookup(ctx, "stock_locations")
	if err != nil {
		return "", err
	}
	if len(locs) == 0 {
		return "", fmt.Errorf("no stock location configured")
	}
	in.LocationID = locs[0].ID

	res, err := data.NewChannelOrdersRepo(d.Db).Apply(ctx, in)
	if err != nil {
		return "", err
	}
	if res.Duplicate {
		return "channel order " + in.OrderNumber + " revision " + fmt.Sprint(in.Revision) + " already applied", nil
	}
	log := logging.L()
	if o.TotalMismatch {
		log.Warnf("channel order %s (%s): the total does not match its lines; recorded as received", in.OrderNumber, in.PluginKey)
	}
	if in.CurrencyMismatch {
		log.Warnf("channel order %s (%s): currency %s differs from the shop currency; recorded as received", in.OrderNumber, in.PluginKey, in.Currency)
	}
	for _, id := range res.Unmatched {
		log.Warnf("channel order %s (%s): line %s matches no item here; recorded without moving stock", in.OrderNumber, in.PluginKey, id)
	}
	for _, n := range res.NegativeStock {
		label := n.SKU
		if label == "" {
			label = fmt.Sprintf("%s (item %s)", n.Name, n.ItemID)
		}
		log.Warnf("channel order %s (%s): stock for %s is now negative (%g)", in.OrderNumber, in.PluginKey, label, n.OnHand)
	}
	// stock.adjusted for connectors, as the local and cloud adjustments
	// publish it; best-effort, after the commit.
	for _, m := range res.Moves {
		reason := "sale"
		if m.Return {
			reason = "refund"
		}
		publishStockAdjusted(ctx, d, plugins.StockAdjustedEvent{
			ItemID: m.ItemID, VariantID: m.VariantID, SKU: m.SKU, DeltaQty: m.Delta, Reason: reason, Location: m.LocationID,
		})
	}
	// The levels linked tills pull moved (ADR-0114 §2); nil-safe.
	d.NudgeLink(fleetlink.ScopeStock)
	return fmt.Sprintf("channel order %s recorded (revision %d)", in.OrderNumber, in.Revision), nil
}
