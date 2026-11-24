package pages

import (
	"context"
	"errors"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// legReconcileMax bounds one boot pass.
const legReconcileMax = 500

// LegReconcileResult counts what one ReconcilePaymentLegs pass did.
type LegReconcileResult struct {
	Reversed, ReverseFailed, Warned, TapLeftToManager int
}

// ReconcilePaymentLegs is the boot reconciliation of the payment-leg journal
// (ut-docs#6653, ADR-0136 Decision 9.5). It touches only legs whose newest row
// was written before startedAt (the start of this process): a live tender
// exists only in memory, so an older row is not held by one.
//
//   - captured with no sale, not a tap/phone leg: the Decision 2 reversal,
//     with the full reference and exact captured amount from the row.
//   - captured tap/phone leg (ADR-0198): never automatic; logged only, the
//     charged-tap-no-sale flow is the manager's.
//   - reversing (crashed mid-send), unknown, asked: warned and logged, and
//     listed for a manager on Settings -> Payments (Payments needing
//     attention; Retry for reverse_failed, Resolve; leg_attention.go). Never
//     a refund and never a re-send here: money may never have moved, or the
//     provider may already have paid it out.
//
// Rows already warned, reverse_failed or terminal are not touched, so a second
// boot does nothing. Errors are logged, never returned to boot or checkout.
func ReconcilePaymentLegs(ctx context.Context, d *common.Deps, startedAt time.Time) LegReconcileResult {
	var res LegReconcileResult
	repo := data.NewPaymentLegJournalRepo(d.Db)
	rows, err := repo.ListBefore(ctx, startedAt, []string{data.LegCaptured, data.LegReversing, data.LegUnknown, data.LegAsked}, legReconcileMax)
	if err != nil {
		logging.L().Errorf("payment legs: boot reconciliation could not read the journal: %v (ut-docs#6653)", err)
		return res
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			break
		}
		switch row.State {
		case data.LegCaptured:
			reconcileCaptured(ctx, d, row, &res)
		default:
			warnLeg(ctx, repo, row, &res)
		}
	}
	if res != (LegReconcileResult{}) {
		logging.L().Infof("payment legs: boot reconciliation: %d returned, %d could not be returned, %d need a human at the provider, %d tap payment(s) left to a manager (ut-docs#6653)", res.Reversed, res.ReverseFailed, res.Warned, res.TapLeftToManager)
	}
	return res
}

func warnLeg(ctx context.Context, repo *data.PaymentLegJournalRepo, row data.LegEntry, res *LegReconcileResult) {
	was := row.State
	row.State, row.Reason = data.LegWarned, data.LegReasonRestart
	if err := repo.Append(ctx, row); err != nil && !errors.Is(err, data.ErrLegJournalTransition) {
		logging.L().Warnf("payment legs: could not record warned for authorize_request_id %s: %v (ut-docs#6653)", row.RequestID, err)
		return
	}
	res.Warned++
	logging.L().Warnf("payment leg left %s by the last run — method %q, amount %d %s, authorize_request_id %s: the till restarted before it was settled; not refunded here (money may never have moved, or may already have been paid out); check the payment provider and refund it there if it was charged (ADR-0136 Decision 9, ut-docs#6653)", was, row.MethodID, row.AmountMinor, row.Currency, row.RequestID)
}

func reconcileCaptured(ctx context.Context, d *common.Deps, row data.LegEntry, res *LegReconcileResult) {
	_, isTap, err := tapEntryErr(ctx, d, row.MethodID)
	if err != nil || isTap {
		// Fail safe: a leg that might be a tap is never refunded automatically.
		res.TapLeftToManager++
		logging.L().Warnf("card payment captured and never became a sale — method %q, amount %d %s, authorize_request_id %s: a tap payment is returned only by a manager (ADR-0198); check it at the provider (ADR-0136 Decision 9, ut-docs#6653)", row.MethodID, row.AmountMinor, row.Currency, row.RequestID)
		return
	}
	leg := legFromRow(row)
	// The send is not cancelled with the boot pass: once it starts it runs to
	// the plugin's own call timeout, like a live reversal (ut-docs#6805).
	if failed := reversePaymentLegs(context.WithoutCancel(ctx), d, []pos.CapturedLeg{leg}, "payment captured by the last run never became a sale"); failed > 0 {
		res.ReverseFailed++
		return
	}
	res.Reversed++
}
