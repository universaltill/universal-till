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
	Reversed, ReverseFailed, Warned, TapLeftToManager, TapChargedNoSale int
}

// ReconcilePaymentLegs is the boot reconciliation of the payment-leg journal
// (ut-docs#6653, ADR-0136 Decision 9.5). It touches only legs whose newest row
// was written before startedAt (the start of this process): a live tender
// exists only in memory, so an older row is not held by one.
//
//   - captured with no sale, not a tap/phone leg: the Decision 2 reversal,
//     with the full reference and exact captured amount from the row.
//   - captured tap/phone leg (ADR-0198): never refunded or sent to the
//     provider here. It is appended warned/tap_charged_no_sale so a manager
//     sees it on Settings -> Payments (ADR-0136 Decision 9.2, ut-docs#7028).
//     If the till cannot tell whether it is a tap (neither read shows a tap
//     and either read failed) it stays captured, logged, and the next boot
//     decides.
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
		logging.L().Infof("payment legs: boot reconciliation: %d returned, %d could not be returned, %d need a human at the provider, %d charged tap payment(s) with no sale listed for a manager, %d tap payment(s) left captured (ut-docs#6653, ut-docs#7028)", res.Reversed, res.ReverseFailed, res.Warned, res.TapChargedNoSale, res.TapLeftToManager)
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
	// A payment_attempts row under the request id is proof of a tap; otherwise
	// the method's plugin entry decides. No tap shown and a read failed: leave
	// it captured (never reverse on doubt).
	_, attFound, attErr := data.NewPaymentAttemptRepo(d.Db).Get(ctx, row.RequestID)
	_, isTap, plugErr := tapEntryErr(ctx, d, row.MethodID)
	switch {
	case attFound || isTap:
		warnChargedTap(ctx, d, row, res)
		return
	case attErr != nil || plugErr != nil:
		res.TapLeftToManager++
		logging.L().Warnf("card payment captured and never became a sale — method %q, amount %d %s, authorize_request_id %s: the till could not tell whether it is a tap payment, so it is left as it is and not refunded; the next start decides again (ADR-0136 Decision 9, ut-docs#7028)", row.MethodID, row.AmountMinor, row.Currency, row.RequestID)
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

// warnChargedTap appends warned/tap_charged_no_sale to a captured tap leg: the
// customer was charged and there is no sale. Nothing is refunded or sent to
// the provider, and the attempt row is not touched (ut-docs#7028).
func warnChargedTap(ctx context.Context, d *common.Deps, row data.LegEntry, res *LegReconcileResult) {
	next := row
	next.State, next.Reason, next.At = data.LegWarned, data.LegReasonTapChargedNoSale, time.Time{}
	if err := data.NewPaymentLegJournalRepo(d.Db).Append(ctx, next); err != nil {
		logging.L().Warnf("payment legs: could not record the charged tap payment for authorize_request_id %s: %v; it stays captured and the next start retries (ut-docs#7028)", row.RequestID, err)
		return
	}
	res.TapChargedNoSale++
	logging.L().Warnf("tap payment charged and never became a sale — method %q, amount %d %s, authorize_request_id %s, transaction_ref %s: the customer WAS charged but there is no sale; listed under Payments needing attention; refund it at the payment provider (ADR-0136 Decision 9, ut-docs#7028)", row.MethodID, row.AmountMinor, row.Currency, row.RequestID, maskedLegRef(row.TransactionRef))
}
