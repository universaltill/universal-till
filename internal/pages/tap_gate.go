package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paybroker"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#6455 (ADR-0198 §3.1, §3.10): the payment gate's side of a
// card-present (Tap to Pay) tender. For an entry that declares tap:true core
//   - binds the authorize dispatch (amount, currency, provider and the
//     attempt key come from the gate, never from the plugin),
//   - treats the plugin's "approved" as advisory and asks the broker itself
//     (pos.ConfirmTap) before money is taken to have moved,
//   - maps anything unconfirmed to the existing outcome-unknown tender lock,
//     shown as the pending screen (never "declined").

// declineCodeTapStartFailed is the decline_code a tap plugin prints when the
// payment never started (no PaymentIntent, no tap): nothing was charged.
const declineCodeTapStartFailed = "start_failed"

// tapStartFailedError: the tap payment could not be started; nothing was
// charged, the basket is not locked.
type tapStartFailedError struct {
	Method string
	// ReaderBusy: the paired phone reader said it was busy (ux A6 wording).
	ReaderBusy bool
	// NeedsAttention: an earlier tap payment on this provider needs a
	// manager (ut-docs#6795); nothing was started.
	NeedsAttention bool
}

func (e *tapStartFailedError) Error() string { return "tap payment could not start: " + e.Method }

var tapProviderRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// tapEntry reports whether method is a payment entry declaring tap:true and
// the broker provider it names in its config ("provider"; "" when it names
// none or a malformed one, which tapAuthorize refuses). A lookup failure is
// "not a tap entry": the ordinary gate then fails closed itself.
func tapEntry(ctx context.Context, d *common.Deps, method string) (string, bool) {
	entries, err := data.NewPluginRepo(d.Db).ListPaymentEntries(ctx)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.EntryKey != method || !plugins.IsTapEntryConfig(e.ConfigJSON) {
			continue
		}
		var c struct {
			Provider string `json:"provider"`
		}
		provider := ""
		if json.Unmarshal([]byte(e.ConfigJSON), &c) == nil && tapProviderRE.MatchString(c.Provider) {
			provider = c.Provider
		}
		return provider, true
	}
	return "", false
}

// tapAuthorize is blockingPaymentEventDispatch for a tender leg. For a tap
// entry it binds the dispatch and confirms the answer with the broker; the
// returned *pos.TapConfirmation is non-nil when core confirmed money moved.
// A payment already confirmed (Check again, manager approve) is not sent to
// the plugin again.
func tapAuthorize(ctx context.Context, d *common.Deps, method, requestID string, amount money.Money, currency string, payload map[string]any) (json.RawMessage, bool, *pos.TapConfirmation, error) {
	provider, isTap := tapEntry(ctx, d, method)
	if !isTap {
		resp, dispatched, err := blockingPaymentEventDispatch(ctx, d, method, "authorize", requestID, payload)
		return resp, dispatched, nil, err
	}
	if provider == "" {
		// A tap entry that names no broker provider cannot be confirmed, so
		// it is never asked: fail closed before any plugin runs.
		return nil, false, nil, fmt.Errorf("payment gate: tap entry %q declares no valid provider", method)
	}
	repo := data.NewPaymentAttemptRepo(d.Db)
	if a, found, err := repo.Get(ctx, requestID); err == nil && found && a.State == data.AttemptApproved && a.AmountMinor == amount.Minor() && strings.EqualFold(a.Currency, currency) {
		return nil, true, &pos.TapConfirmation{PIID: a.PIID, CardBrand: a.CardBrand, Last4: a.Last4}, nil
	}
	b := paybroker.Binding{
		Kind: paybroker.BindAuthorize, AmountMinor: amount.Minor(), Provider: provider, AttemptKey: requestID, Currency: currency,
	}
	// A tender resolved to a paired phone reader is collected on that phone
	// (ut-docs#6538): the reader ref comes from core's own reader resolution
	// (the gate payload), never from the plugin or the page.
	if kind, _ := payload["reader_kind"].(string); kind == data.CardReaderKindPhone {
		if ref, _ := payload["reader_id"].(string); ref != "" {
			b.ReaderRef = ref
		}
	}
	bound := paybroker.WithBinding(ctx, b)
	resp, dispatched, err := blockingPaymentEventDispatch(bound, d, method, "authorize", requestID, payload)
	if err != nil {
		return tapVerifyFailure(ctx, d, repo, requestID, resp, dispatched, err, method)
	}
	if !dispatched {
		// A tap entry nobody was asked about took no payment: completing the
		// sale on it would sell goods for nothing.
		return nil, false, nil, fmt.Errorf("payment gate: tap entry %q has no authorize subscriber", method)
	}
	conf, cerr := pos.ConfirmTap(ctx, repo, requestID)
	if cerr != nil {
		var pe *pos.PaymentPendingError
		if d.ReaderPay != nil && errors.As(cerr, &pe) && b.ReaderRef != "" && d.ReaderPay.LostContact(requestID) {
			// The phone's link dropped mid-tap: say so (the customer may have
			// been charged). The broker's own pending verdict stays on the row.
			settleAttempt(ctx, repo, requestID, data.AttemptUnknown, pos.PendingReaderLost)
			cerr = &pos.PaymentPendingError{Reason: pos.PendingReaderLost}
		}
		return resp, dispatched, nil, cerr
	}
	return resp, dispatched, &conf, nil
}

// tapVerifyFailure decides what a plugin failure (start_failed, a decline, a
// timeout) means for a tap leg. With no attempt row no PaymentIntent exists,
// so only then is start_failed "nothing charged" and a decline final. Once a
// row exists a tap may have happened and the plugin's word is not enough
// (ADR-0198 3.9, 3.10): the broker decides. Paid is confirmed (the sale
// completes), not paid is cancelled and the original failure stands, anything
// else is pending and locks the tender.
func tapVerifyFailure(ctx context.Context, d *common.Deps, repo *data.PaymentAttemptRepo, requestID string, resp json.RawMessage, dispatched bool, err error, method string) (json.RawMessage, bool, *pos.TapConfirmation, error) {
	var exitErr *plugins.HandlerExitError
	startFailed := errors.As(err, &exitErr) && exitErr.DeclineCode() == declineCodeTapStartFailed
	original := err
	if startFailed {
		original = &tapStartFailedError{Method: method, ReaderBusy: d.ReaderPay != nil && d.ReaderPay.WasBusy(requestID)}
	}
	if errors.As(err, &exitErr) && exitErr.DeclineCode() == declineCodeOutcomeUnknown {
		return resp, dispatched, nil, err // already unknown: the gate locks it
	}
	f := tapAssess(ctx, d, pos.UnknownLeg{RequestID: requestID})
	if !f.HaveRow && f.ErrCode == "" {
		// No attempt row: nothing was started through core.
		return resp, dispatched, nil, original
	}
	f = tapCancelThenAssess(ctx, d, f)
	switch f.Verdict {
	case tapNotPaid:
		reason := "declined"
		state := data.AttemptDeclined
		if startFailed {
			reason, state = "start_failed", data.AttemptCancelled
		}
		settleAttempt(ctx, repo, requestID, state, reason)
		return resp, dispatched, nil, original
	case tapPaid:
		conf, cerr := pos.ConfirmTap(ctx, repo, requestID)
		var pe *pos.PaymentPendingError
		if cerr == nil {
			return nil, true, &conf, nil
		}
		if errors.As(cerr, &pe) && pe.Reason == pos.PendingAlreadySettled {
			return nil, true, &pos.TapConfirmation{PIID: f.Attempt.PIID, CardBrand: f.Attempt.CardBrand, Last4: f.Attempt.Last4}, nil
		}
		return resp, dispatched, nil, cerr
	}
	reason := f.Reason
	if reason == "" {
		reason = pos.PendingNotConfirmed
	}
	if f.HaveRow {
		settleAttempt(ctx, repo, requestID, data.AttemptUnknown, reason)
	}
	return resp, dispatched, nil, &pos.PaymentPendingError{Reason: reason}
}

// settleAttempt closes an attempt row core knows nothing was charged for;
// a row that is absent or already settled is left alone.
func settleAttempt(ctx context.Context, repo *data.PaymentAttemptRepo, key, state, reason string) {
	if err := repo.Mark(ctx, key, state, data.AttemptUpdate{Reason: reason}); err != nil && !errors.Is(err, data.ErrAttemptNotFound) && !errors.Is(err, data.ErrAttemptSettled) {
		logging.L().Warnf("tap: settle attempt as %s: %v", state, err)
	}
}

// tapRefundContext binds a refund dispatch of a tap entry to the one
// PaymentIntent and ceiling core derived from the original payment; any other
// entry, or a refund with no single PaymentIntent, is returned unbound (the
// refund host function then refuses: a plugin never names its own PI).
func tapRefundContext(ctx context.Context, d *common.Deps, method, pi string, maxMinor int64, currency, refundKey string) context.Context {
	provider, isTap := tapEntry(ctx, d, method)
	if !isTap || pi == "" || maxMinor <= 0 || refundKey == "" {
		return ctx
	}
	return paybroker.WithBinding(ctx, paybroker.Binding{
		Kind: paybroker.BindRefund, Provider: provider, PI: pi, MaxRefundMinor: maxMinor, Currency: currency, RefundKey: refundKey,
	})
}

// tapPendingReason is the pending reason to remember on the locked leg.
func tapPendingReason(e *pos.PaymentPendingError) string {
	if e == nil {
		return ""
	}
	return e.Reason
}
