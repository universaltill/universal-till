package pages

import (
	"context"
	"log"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// wireAbandonedTenderReversal registers the ut-docs#6602 handler on both
// engines. Idempotent; called from every register func whose routes can
// reset a basket.
func wireAbandonedTenderReversal(d *common.Deps) {
	if d == nil {
		return
	}
	if d.Engine != nil {
		d.Engine.SetAbandonedTenderHandler(abandonedTenderHandler(d))
	}
	if d.KioskEngine != nil {
		d.KioskEngine.SetAbandonedTenderHandler(abandonedTenderHandler(d))
	}
}

// abandonedTenderHandler reverses the captured legs of a tender the cashier
// dropped (clear, park, open another order) and warns about unknown legs.
// context.Background(): the reversal must survive the cashier navigating away.
func abandonedTenderHandler(d *common.Deps) pos.AbandonedTenderHandler {
	return func(reason pos.AbandonReason, t pos.PendingTender) pos.AbandonResult {
		ctx := context.Background()
		what := "tender abandoned (" + string(reason) + ")"
		failed := reversePaymentLegs(ctx, d, t.Captured, what)
		warnUnknownLegs(t.Unknown, what)
		log.Printf("%s: %d captured leg(s) reversed, %d failed, %d of unknown outcome (ut-docs#6602)", what, len(t.Captured)-failed, failed, len(t.Unknown))
		return pos.AbandonResult{Reversed: len(t.Captured) - failed, Failed: failed, Unknown: len(t.Unknown)}
	}
}

// warnUnknownLegs raises one Problems item per leg of unknown outcome. Never
// a refund: it could pay out money that was never taken.
func warnUnknownLegs(legs []pos.UnknownLeg, what string) {
	for _, l := range legs {
		logging.L().Warnf("%s with a payment of UNKNOWN outcome — method %q, amount %d %s, authorize_request_id %s: not reversed; check the payment provider and refund it there if it was charged (ut-docs#6602)", what, l.MethodID, l.Amount.Minor(), l.Currency, l.RequestID)
	}
}

// unionCaptured: a, plus every leg of b whose RequestID is not in a.
func unionCaptured(a, b []pos.CapturedLeg) []pos.CapturedLeg {
	have := map[string]bool{}
	for _, l := range a {
		have[l.RequestID] = true
	}
	out := append([]pos.CapturedLeg(nil), a...)
	for _, l := range b {
		if !have[l.RequestID] {
			out = append(out, l)
		}
	}
	return out
}

// minusCaptured: legs of a whose RequestID is not in b.
func minusCaptured(a, b []pos.CapturedLeg) []pos.CapturedLeg {
	in := map[string]bool{}
	for _, l := range b {
		in[l.RequestID] = true
	}
	var out []pos.CapturedLeg
	for _, l := range a {
		if !in[l.RequestID] {
			out = append(out, l)
		}
	}
	return out
}

// unknownNotAsked: legs whose RequestID is not in asked.
func unknownNotAsked(legs []pos.UnknownLeg, asked map[string]bool) []pos.UnknownLeg {
	var out []pos.UnknownLeg
	for _, l := range legs {
		if !asked[l.RequestID] {
			out = append(out, l)
		}
	}
	return out
}

// abandonToastKey takes the engine's one-shot notice and returns the toast
// key (level is always "error"). ok=false when there was no notice.
func abandonToastKey(engine *pos.Service) (key string, ok bool) {
	if engine == nil {
		return "", false
	}
	r, ok := engine.TakeAbandonNotice()
	if !ok {
		return "", false
	}
	if r.Failed > 0 || r.Reversed == 0 {
		return "pos.toast.tender_abandoned_check_provider", true
	}
	return "pos.toast.tender_abandoned_returned_unknown", true
}
