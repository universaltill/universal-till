package pages

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// The guest_order directive's till side (ut-docs#4337, ADR-0159 §5): an
// order a guest placed on the cloud table page, applied on the main till
// as a pay-at-counter held order for that table — the same order the LAN
// table-QR checkout parks, through the same routine (placeCounterOrder).
//
// The payload was written by an anonymous party, so nothing in it is
// trusted: cloudsync only checked its shape and bounds; this hook
// re-resolves the table token, every item, variant and option against
// this till's own database, and prices, taxes and service-charges the
// order on a FRESH basket engine built by the ADR-0103 session factory
// (SessionBasketManager.NewDetached) — never the cashier's engine or the
// walk-up kiosk's (ADR-0020). This file registers no route, so
// guard-kiosk-engine.sh doesn't scan it;
// TestGuestOrderDirectiveFile_NeverNamesTheSharedEngines pins the same
// rule here. Any refusal writes nothing.

// maxReportedGuestTables bounds the device report's guest_tables
// (ADR-0159 §1); maxGuestTableLabelBytes clips each label.
const (
	maxReportedGuestTables  = 500
	maxGuestTableLabelBytes = 64
)

// errGuestOrderNoFactory: the till was built without a session basket
// factory (a bare test harness) — an internal error, never a refusal.
var errGuestOrderNoFactory = errors.New("guest order: no self-order basket factory")

// cloudGuestOrder applies one decoded guest_order. A refusal is returned in
// the outcome (cloudsync posts it as "failed" with the code) and nothing is
// written; a non-nil error is an internal failure (a DB read failed) that
// leaves the directive pending for the next check-in.
func cloudGuestOrder(ctx context.Context, d *common.Deps, o cloudsync.GuestOrder) (cloudsync.GuestOrderOutcome, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		// cloudsync already skips guest_order on an additional till; this
		// is the boundary's own fail-closed half.
		return cloudsync.GuestOrderOutcome{}, err
	}
	currency := d.CurrentState().Currency

	// Idempotency (ADR-0159 §5): a re-delivery whose first apply parked
	// the order but whose stored result was lost (cloudsync keeps one
	// first) is answered from the order itself, never placed again.
	counterRepo := data.NewKioskCounterOrdersRepo(d.Db)
	if prior, ok, err := guestOrderAlreadyPlaced(ctx, d, counterRepo, o.ID, currency); err != nil || ok {
		return prior, err
	}

	table, found, err := data.NewPOSRepo(d.Db).TableByGuestToken(ctx, o.TableToken)
	if err != nil {
		return cloudsync.GuestOrderOutcome{}, err
	}
	if !found || !table.Enabled {
		return refuseGuestOrder(cloudsync.GuestOrderUnknownTable), nil
	}

	eng, ok := d.SelfOrderSessions.NewDetached()
	if !ok {
		return cloudsync.GuestOrderOutcome{}, errGuestOrderNoFactory
	}
	enabledIDs, err := data.NewSettingsRepo(d.Db).EnabledBarcodeSymbologies(ctx)
	if err != nil {
		return cloudsync.GuestOrderOutcome{}, fmt.Errorf("guest order: barcode symbologies: %w", err)
	}
	type resolvedLine struct {
		base     pos.BasketLine
		qty      int
		selected []data.SelectedModifier
	}
	// Every line is resolved before any is added, so one bad line refuses
	// the whole order with the engine untouched.
	lines := make([]resolvedLine, 0, len(o.Lines))
	for _, l := range o.Lines {
		base, selected, refusal, err := resolveGuestOrderLine(ctx, d, eng, enabledIDs, l)
		if err != nil {
			return cloudsync.GuestOrderOutcome{}, err
		}
		if refusal != "" {
			return refuseGuestOrder(refusal), nil
		}
		lines = append(lines, resolvedLine{base: base, qty: l.Qty, selected: selected})
	}
	// The till prices the order: AddLineWithModifiers folds each option's
	// till-side price delta into the till-resolved base price, exactly as
	// the kiosk's /api/self-order/scan-with-modifiers does. The fresh
	// engine's order type is the dine-in default, which a table-bound order
	// keeps (the cloud's order_type was never read).
	for _, l := range lines {
		eng.AddLineWithModifiers(l.base, float64(l.qty), l.selected)
	}
	eng.SetTable(table.ID, table.Label)
	if eng.TableID() != table.ID {
		return cloudsync.GuestOrderOutcome{}, fmt.Errorf("guest order %s: table %s did not bind", o.ID, table.ID)
	}
	snap := eng.Snapshot()
	if len(snap.Lines) == 0 {
		return refuseGuestOrder(cloudsync.GuestOrderItemUnavailable), nil
	}
	placed, err := placeCounterOrder(ctx, d, counterOrderRequest{
		Snap:         snap,
		OrderType:    eng.OrderType(),
		Offline:      false,
		GuestOrderID: o.ID,
		AsyncTicket:  true,
	})
	if err != nil {
		// A unique-index race on guest_order_id means another apply of the
		// same order won: answer with that one.
		if prior, ok, perr := guestOrderAlreadyPlaced(ctx, d, counterRepo, o.ID, currency); perr == nil && ok {
			return prior, nil
		}
		return cloudsync.GuestOrderOutcome{}, err
	}
	logging.L().Infof("cloudsync: guest order %s parked as %s for table %s", o.ID, placed.Order.DisplayNo, table.Label)
	return cloudsync.GuestOrderOutcome{
		DisplayNo:  placed.Order.DisplayNo,
		TotalMinor: placed.Snap.Total.Minor(),
		Currency:   currency,
	}, nil
}

func refuseGuestOrder(code string) cloudsync.GuestOrderOutcome {
	return cloudsync.GuestOrderOutcome{Refusal: code}
}

// guestOrderAlreadyPlaced answers a guest order this till already parked:
// its C-number and, while the held sale is still parked, its total (a
// resumed or paid order's held row is gone; its total then reads 0 — the
// cloud has normally stored the first result long before).
func guestOrderAlreadyPlaced(ctx context.Context, d *common.Deps, counterRepo *data.KioskCounterOrdersRepo, guestOrderID, currency string) (cloudsync.GuestOrderOutcome, bool, error) {
	prior, ok, err := counterRepo.FindByGuestOrderID(ctx, guestOrderID)
	if err != nil || !ok {
		return cloudsync.GuestOrderOutcome{}, false, err
	}
	out := cloudsync.GuestOrderOutcome{DisplayNo: prior.DisplayNo, Currency: currency}
	held, found, err := data.NewHeldSalesRepo(d.Db).Get(ctx, prior.ID)
	if err != nil {
		return cloudsync.GuestOrderOutcome{}, false, err
	}
	if found {
		out.TotalMinor = held.TotalMinor
	} else if prior.Status == data.KioskCounterOrderStatusHeld {
		// A "held" counter row with no held sale is either a cashier
		// mid-resume or an orphan left by a crash/failed cleanup between
		// Create and the park. Re-placing could duplicate the first, so the
		// order is answered as placed — but loudly, so staff can find it
		// (review of ut-docs#4337; follow-up card tracks orphan recovery).
		logging.L().Errorf("guest order %s: counter order %s (%s) has no parked held sale — check Open orders; if it is missing, take the order at the counter", guestOrderID, prior.ID, prior.DisplayNo)
	}
	return out, true, nil
}

// resolveGuestOrderLine re-validates one line against the till's own
// catalogue and resolves it the way the kiosk does: the item's kiosk code
// (kioskItemCode) and the shared resolveAndValidateModifiers, fed the
// variant and options the guest chose. The checks before that call only
// classify a failure into the ADR-0159 §5 codes; resolveAndValidateModifiers
// stays the authority on membership and pricing. refusal "" with a nil
// error is a good line.
func resolveGuestOrderLine(ctx context.Context, d *common.Deps, eng *pos.Service, enabledIDs []string, l cloudsync.GuestOrderLine) (pos.BasketLine, []data.SelectedModifier, string, error) {
	const unavailable, bad = cloudsync.GuestOrderItemUnavailable, cloudsync.GuestOrderBadOrder
	catalog := data.NewCatalogRepo(d.Db)
	item, found, err := catalog.GetItem(ctx, l.ItemID)
	if err != nil {
		return pos.BasketLine{}, nil, "", err
	}
	// The cloud offers what the till's kiosk offers minus what a phone
	// can't sell (ADR-0159 §3): never a gift voucher (#4214), never a
	// weighed item (no scale on a phone).
	if !found || !item.IsActive || item.ItemType == catalogtypes.ItemTypeGiftVoucher || item.IsWeighed {
		return pos.BasketLine{}, nil, unavailable, nil
	}
	barcodes, err := catalog.ItemBarcodesFor(ctx, item.ID)
	if err != nil {
		return pos.BasketLine{}, nil, "", err
	}
	code := kioskItemCode(item.SKU, barcodes, enabledIDs)
	if code == "" {
		return pos.BasketLine{}, nil, unavailable, nil
	}

	variants, err := catalog.ItemVariantsFor(ctx, item.ID)
	if err != nil {
		return pos.BasketLine{}, nil, "", err
	}
	variants = sellableVariants(variants)
	switch {
	case l.VariantID == "" && len(variants) > 0:
		return pos.BasketLine{}, nil, bad, nil // the guest page must have asked
	case l.VariantID != "":
		member := false
		for _, v := range variants {
			if v.ID == l.VariantID {
				member = true
				break
			}
		}
		if !member {
			return pos.BasketLine{}, nil, unavailable, nil
		}
	}

	groups, err := data.NewModifierRepo(d.Db).ResolveGroupsForItem(ctx, item.ID)
	if err != nil {
		return pos.BasketLine{}, nil, "", err
	}
	groupOf := map[string]string{}
	for _, g := range groups {
		for _, opt := range g.Options {
			groupOf[opt.ID] = g.ID
		}
	}
	form := url.Values{}
	if l.VariantID != "" {
		form.Set("variantId", l.VariantID)
	}
	for _, optID := range l.ModifierOptionIDs {
		gid, ok := groupOf[optID]
		if !ok {
			return pos.BasketLine{}, nil, unavailable, nil // not one of this item's options (any more)
		}
		form.Add("mod_"+gid, optID)
	}
	for _, g := range groups {
		if n := len(form["mod_"+g.ID]); n < g.MinSelect || n > g.MaxSelect {
			return pos.BasketLine{}, nil, bad, nil
		}
	}

	base, selected, userMsg, err := resolveAndValidateModifiers(ctx, d, eng, httpx.DefaultLocale(), code, item.ID, form)
	if err != nil {
		if userMsg == "" {
			return pos.BasketLine{}, nil, "", err
		}
		// The catalogue moved between the checks above and this read (or
		// the code resolves to another item): the line is no longer valid.
		return pos.BasketLine{}, nil, unavailable, nil
	}
	if base.IsGiftVoucherItem() || base.QtyFromCode {
		return pos.BasketLine{}, nil, unavailable, nil
	}
	return base, selected, "", nil
}

// remoteGuestTablesReport is the device report's guest_tables (ADR-0159
// §1): [{token, label}] for up to maxReportedGuestTables ENABLED tables,
// labels clipped to maxGuestTableLabelBytes on a rune boundary. A table
// with no token yet gets one here (first need). ok=false on a read error:
// the field is then left off the report, and the cloud keeps the set it
// has rather than being told the shop has no tables. Main till only — the
// caller adds it in DeviceExtra's main-till block, and buildSyncRequest
// drops it from a replica's report as well.
func remoteGuestTablesReport(ctx context.Context, d *common.Deps) ([]map[string]any, bool) {
	tables, err := data.NewPOSRepo(d.Db).GuestTables(ctx, maxReportedGuestTables)
	if err != nil {
		logging.L().Warnf("cloudsync: guest tables not reported this check-in: %v", err)
		return nil, false
	}
	out := make([]map[string]any, 0, len(tables))
	for _, t := range tables {
		out = append(out, map[string]any{"token": t.Token, "label": clipGuestTableLabel(t.Label)})
	}
	return out, true
}

// clipGuestTableLabel cuts a table label to maxGuestTableLabelBytes bytes
// without splitting a UTF-8 sequence.
func clipGuestTableLabel(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxGuestTableLabelBytes {
		return s
	}
	cut := maxGuestTableLabelBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
