package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

// ut-docs#2703 (reopened). Open orders has two tabs -- "On hold" (the
// till's own held sales) and "Pay at the counter" (self-order kiosk orders
// waiting for payment) -- and tapping a row in either does the same thing:
// the order opens on the sale screen, where the cashier can change it and
// take payment.

// Tab identifiers, as they appear in ?tab= and the templates.
const (
	openOrdersTabHold    = "hold"
	openOrdersTabCounter = "counter"
)

// pickOpenOrdersTab resolves ?tab=: an explicit choice wins; otherwise On
// hold, unless On hold is empty and a counter order is waiting -- then the
// counter tab, so the one list with something in it is what the cashier
// sees first.
func pickOpenOrdersTab(param string, hold, counter int) string {
	switch param {
	case openOrdersTabHold, openOrdersTabCounter:
		return param
	}
	if hold == 0 && counter > 0 {
		return openOrdersTabCounter
	}
	return openOrdersTabHold
}

// heldPayloadIsCounterOrder reports whether a held sale is a pay-at-counter
// kiosk order: its snapshot carries the customer's order number
// (BasketSnapshot.DisplayNo), which completeCounterOrderCheckout sets and
// only a kiosk order ever has. Read from the payload, not from a
// kiosk_counter_orders lookup, because the payload travels with the held
// sale: on the main till an order parked by a replica's kiosk has no local
// kiosk row, and it must still land in this tab. A resumed-and-re-parked
// order keeps it (the engine carries the number); voiding every line clears
// it (the order stops being that order), and then it is an ordinary hold.
func heldPayloadIsCounterOrder(payload string) bool {
	var p struct {
		DisplayNo string `json:"display_no"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return false
	}
	return strings.TrimSpace(p.DisplayNo) != ""
}

// openOrderAgeText renders how long an order has been open so it reads
// naturally at any age: "N min" under an hour, "N h" under a day, "N d"
// after that -- a forgotten legacy order read "22258 min" on the pilot
// tablet. Whole units, rounded down.
func openOrderAgeText(minutes int, locale string) string {
	switch {
	case minutes < 60:
		return fmt.Sprintf(httpx.T(locale, "open_orders.age_minutes"), minutes)
	case minutes < 24*60:
		return fmt.Sprintf(httpx.T(locale, "open_orders.age_hours"), minutes/60)
	default:
		return fmt.Sprintf(httpx.T(locale, "open_orders.age_days"), minutes/(24*60))
	}
}

// legacyCounterRows turns legacy "open" counter orders (placed before
// pay-at-counter orders were parked as held sales; they carry names and
// quantities, no prices) into rows for the counter tab. Total stays zero and
// TotalUnknown set: the price is only known once the order is opened and
// matched to the current catalog.
func legacyCounterRows(orders []data.KioskCounterOrder, locale string, now time.Time) []openOrderRow {
	rows := make([]openOrderRow, 0, len(orders))
	for _, o := range orders {
		age := elapsedMinutes(o.CreatedAt, now)
		rows = append(rows, openOrderRow{
			ID:           o.ID,
			Label:        counterOrderLabelIn(locale, o.DisplayNo, o.OrderType),
			TableLabel:   o.TableLabel,
			LineCount:    len(o.Lines),
			AgeText:      openOrderAgeText(age, locale),
			Legacy:       true,
			Items:        counterOrderItemsSummary(o.Lines, locale),
			TotalUnknown: true,
		})
	}
	return rows
}

// legacyCounterOrderHeldSale prices a legacy counter order from the CURRENT
// catalog and builds the held sale it becomes (same id). Each line is
// matched to exactly one active item by exact, case-insensitive, trimmed
// name (data.CatalogRepo.ActiveItemIDsByName); its modifiers by name within
// that item's own modifier groups; the price is the item's current
// effective price through the same resolver the sale screen uses (the
// "item:<id>" code, data.ItemIDCode), plus the modifiers' deltas -- built on
// a scratch engine so the snapshot is exactly what a basket would hold.
//
// A line is NOT added -- it goes, exactly as ordered, into the snapshot's
// AddByHand (pos.BasketSnapshot), which travels with the held sale so every
// resume of it shows the sale screen's "add by hand" notice -- when its name
// matches no active item or several (never guess which), its quantity is not
// positive, the item needs a variant choice, or its modifiers cannot be
// matched safely (matchCounterModifiers). Never dropped silently, never
// priced wrong.
func legacyCounterOrderHeldSale(ctx context.Context, d *common.Deps, order data.KioskCounterOrder) (data.HeldSale, error) {
	catalog := data.NewCatalogRepo(d.Db)
	names := make([]string, 0, len(order.Lines))
	for _, l := range order.Lines {
		names = append(names, l.Name)
	}
	matches, err := catalog.ActiveItemIDsByName(ctx, names)
	if err != nil {
		return data.HeldSale{}, err
	}
	btn := d.BtnStore
	if btn == nil {
		btn = ui.NewButtonStore(d.Db)
	}
	var cfg pos.Config
	if d.Engine != nil {
		cfg = d.Engine.Config()
	}
	eng := pos.NewServiceWithResolver(cfg, ui.PriceResolverAdapter{Store: btn})
	if order.OrderType == pos.OrderTypeTakeaway {
		eng.SetOrderType(pos.OrderTypeTakeaway)
	}
	modRepo := data.NewModifierRepo(d.Db)
	var byHand []pos.ByHandLine
	unmatched := func(l data.KioskCounterOrderLine) {
		byHand = append(byHand, pos.ByHandLine{Name: l.Name, Qty: l.Qty, Modifiers: l.Modifiers})
	}
	for _, l := range order.Lines {
		ids := matches[data.FoldItemName(l.Name)]
		// A non-positive quantity is a corrupt line, not "one": the
		// cashier decides.
		if len(ids) != 1 || l.Qty <= 0 {
			unmatched(l)
			continue
		}
		itemID := ids[0]
		variants, err := catalog.ItemVariantsForSale(ctx, itemID)
		if err != nil {
			return data.HeldSale{}, err
		}
		if len(sellableVariants(variants)) > 0 {
			unmatched(l)
			continue
		}
		base, ok := eng.ResolveBase(data.ItemIDCode(itemID))
		if !ok {
			unmatched(l)
			continue
		}
		mods, ok, err := matchCounterModifiers(ctx, modRepo, itemID, l.Modifiers)
		if err != nil {
			return data.HeldSale{}, err
		}
		if !ok {
			unmatched(l)
			continue
		}
		eng.AddLineWithModifiers(base, l.Qty, mods)
	}
	snap := eng.Snapshot()
	snap.TableID = order.TableID
	snap.TableLabel = order.TableLabel
	snap.DisplayNo = order.DisplayNo
	snap.AddByHand = byHand
	payload, err := json.Marshal(snap)
	if err != nil {
		return data.HeldSale{}, err
	}
	// The order's own placement time, in held_sales' text shape, so its
	// age on Open orders keeps counting from when the customer ordered.
	createdAt := ""
	if ts, err := time.Parse(time.RFC3339, order.CreatedAt); err == nil {
		createdAt = ts.UTC().Format("2006-01-02 15:04:05")
	}
	return data.HeldSale{
		ID:         order.ID,
		Label:      counterOrderHeldLabel(order.DisplayNo, order.OrderType),
		TotalMinor: snap.Total.Minor(),
		LineCount:  len(snap.Lines),
		Payload:    string(payload),
		TableID:    order.TableID,
		CreatedAt:  createdAt,
	}, nil
}

// matchCounterModifiers resolves a legacy line's modifier names against the
// item's own (active, sale-time) modifier groups. ok=false -- the caller then
// leaves the whole line to the cashier rather than sell it other than as
// ordered -- when:
//   - a name matches no option, or options in more than one place (the same
//     name in two groups, or twice in one: which price delta? never guess);
//   - the resulting selection breaks a group's rules the way the sale
//     screen's own picker enforces them (pos_modifiers_api.go: between
//     MinSelect and MaxSelect per group), plus Required meaning at least one
//     -- so an item whose required group the customer never chose from is
//     never added with that requirement unmet.
func matchCounterModifiers(ctx context.Context, repo *data.ModifierRepo, itemID string, names []string) ([]data.SelectedModifier, bool, error) {
	groups, err := repo.ResolveGroupsForItem(ctx, itemID)
	if err != nil {
		return nil, false, err
	}
	out := make([]data.SelectedModifier, 0, len(names))
	perGroup := map[string]int{}
	for _, n := range names {
		want := data.FoldItemName(n)
		var hits []data.SelectedModifier
		for _, g := range groups {
			for _, o := range g.Options {
				if data.FoldItemName(o.Name) == want {
					hits = append(hits, data.SelectedModifier{GroupID: g.ID, OptionID: o.ID, GroupName: g.Name, OptionName: o.Name, PriceDeltaMinor: o.PriceDeltaMinor})
				}
			}
		}
		if len(hits) != 1 {
			return nil, false, nil
		}
		out = append(out, hits[0])
		perGroup[hits[0].GroupID]++
	}
	for _, g := range groups {
		n := perGroup[g.ID]
		if n < g.MinSelect || n > g.MaxSelect || (g.Required && n == 0) {
			return nil, false, nil
		}
	}
	return out, true, nil
}

// counterOrderItemsSummary joins a counter order's lines into one
// "Name (modifiers) × Qty" list -- how a legacy pay-at-counter order (which stored no
// prices) is described on Open orders' "Pay at the counter" tab. Qty renders
// with the viewing operator's own locale digit-shape/grouping
// (ut-docs#2221, mirroring FormatQty everywhere else on-screen) -- unlike
// the kitchen ticket this same stored quantity also feeds
// (self_order_shop.go's printCounterOrderTicket), which formats separately
// and deliberately stays Latin for the printer.
func counterOrderItemsSummary(lines []data.KioskCounterOrderLine, locale string) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		name := l.Name
		if len(l.Modifiers) > 0 {
			name += " (" + strings.Join(l.Modifiers, ", ") + ")"
		}
		parts = append(parts, fmt.Sprintf("%s × %s", name, httpx.FormatQty(l.Qty, locale)))
	}
	return strings.Join(parts, ", ")
}
