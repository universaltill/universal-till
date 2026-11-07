package pages

import (
	"context"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// shift.opened / shift.closed (ut-docs#5770; contract: ut-docs
// reference/contracts/shift-events.md). Published best-effort by the shift
// handlers after the transaction has committed and the response is written.

const (
	shiftOpenedEvent = plugins.ShiftOpenedEvent
	shiftClosedEvent = plugins.ShiftClosedEvent
)

// Test seams.
var publishShiftOpened = func(ctx context.Context, bus *plugins.EventBus, ev plugins.ShiftOpenedPayload) (string, error) {
	return bus.PublishShiftOpened(ctx, ev)
}

var publishShiftClosed = func(ctx context.Context, bus *plugins.EventBus, ev plugins.ShiftClosedPayload) (string, error) {
	return bus.PublishShiftClosed(ctx, ev)
}

// dispatchShiftOpened: HasSubscribers first. Zero subscribers costs one map
// lookup and nothing else: no payload, no Publish, no event_published row.
func dispatchShiftOpened(ctx context.Context, d *common.Deps, s pos.OpenedShift) {
	bus := plugins.SharedBus(d.Db)
	if !bus.HasSubscribers(shiftOpenedEvent) {
		return
	}
	sendShiftOpened(ctx, bus, plugins.ShiftOpenedPayload{
		ShiftID: s.ShiftID, RegisterID: s.RegisterID, CashierID: s.CashierID,
		Currency: d.CurrentState().Currency, OpeningCash: s.OpeningCash, OpenedAt: s.OpenedAt,
	})
}

// sendShiftOpened is split out so its defer/recover never touches the
// zero-plugin path.
func sendShiftOpened(ctx context.Context, bus *plugins.EventBus, ev plugins.ShiftOpenedPayload) {
	defer logging.RecoverAndLog("pages.shiftOpened")
	// The shift is committed: a client that hangs up after the 200 must not
	// cancel the permission check / enqueue.
	ctx = context.WithoutCancel(ctx)
	if _, err := publishShiftOpened(ctx, bus, ev); err != nil {
		logging.L().Warnf("shifts: publish %s for %s: %v", shiftOpenedEvent, ev.ShiftID, err) // id only
	}
}

// dispatchShiftClosed: same shape as dispatchShiftOpened.
func dispatchShiftClosed(ctx context.Context, d *common.Deps, s pos.ClosedShift) {
	bus := plugins.SharedBus(d.Db)
	if !bus.HasSubscribers(shiftClosedEvent) {
		return
	}
	sendShiftClosed(ctx, bus, plugins.ShiftClosedPayload{
		ShiftID: s.ShiftID, RegisterID: s.RegisterID, CashierID: s.CashierID,
		Currency: d.CurrentState().Currency, OpenedAt: s.OpenedAt, ClosedAt: s.ClosedAt,
		OpeningCash: s.OpeningCash, ClosingCash: s.ClosingCash, ExpectedCash: s.ExpectedCash,
		Variance: s.Variance, Skim: s.Skim, NewFloat: s.NewFloat,
	})
}

func sendShiftClosed(ctx context.Context, bus *plugins.EventBus, ev plugins.ShiftClosedPayload) {
	defer logging.RecoverAndLog("pages.shiftClosed")
	ctx = context.WithoutCancel(ctx)
	if _, err := publishShiftClosed(ctx, bus, ev); err != nil {
		logging.L().Warnf("shifts: publish %s for %s: %v", shiftClosedEvent, ev.ShiftID, err) // id only
	}
}
