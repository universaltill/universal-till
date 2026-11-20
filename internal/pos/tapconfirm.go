package pos

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
	"github.com/universaltill/universal-till/internal/paybroker"
)

// Core's own confirmation of a card-present (Tap to Pay) payment, ut-docs#6455,
// ADR-0198 §3.10. A payment plugin's "approved" and the phone's verdict are
// advisory: they only say a tap happened. Money is taken to have moved only
// when the broker, asked by core as CallerCore, reports the PaymentIntent
// succeeded for exactly the amount and currency of the attempt row core wrote
// before the tap. Everything else is PENDING (the till locks on the tender and
// a manager settles it), never "declined": a "declined" that was really an
// approval charges the customer twice (#6439).

// Reasons a confirmation is pending (closed set; also the attempt row's
// reason and what the pay screen is keyed on).
const (
	PendingAmountMismatch   = "amount_mismatch"
	PendingContextCancelled = "context_cancelled"
	PendingLookupFailed     = "lookup_failed"
	PendingProcessing       = "processing"
	PendingRequiresCapture  = "requires_capture"
	PendingNotConfirmed     = "not_confirmed"
	PendingAlreadySettled   = "already_settled"
)

// PaymentPendingError says the payment's outcome is not confirmed. The caller
// maps it to a pending (unknown) tender leg, never to a decline.
type PaymentPendingError struct{ Reason string }

func (e *PaymentPendingError) Error() string { return "payment outcome not confirmed: " + e.Reason }

// TapAttempts is the attempt store ConfirmTap reads and settles
// (*data.PaymentAttemptRepo).
type TapAttempts interface {
	Get(ctx context.Context, key string) (data.PaymentAttempt, bool, error)
	Mark(ctx context.Context, key, state string, u data.AttemptUpdate) error
}

// TapConfirmation is what core learned from the broker about an approved
// payment: the PaymentIntent and, as the provider reports them, brand and
// last four digits (ADR-0127: nothing else about the card).
type TapConfirmation struct {
	PIID, CardBrand, Last4 string
}

var last4RE = regexp.MustCompile(`^[0-9]{4}$`)
var brandRE = regexp.MustCompile(`^[a-z0-9_ -]{1,32}$`)

// ConfirmTap asks the broker for the real state of attempt key and approves
// only on succeeded + equal amount + equal currency (+ equal key and
// PaymentIntent when the answer carries them). The attempt is read from the
// store, never from the caller. Approval marks the row approved with the PI
// and card brand/last4 from the broker's answer; every other outcome marks it
// unknown and returns a *PaymentPendingError.
func ConfirmTap(ctx context.Context, store TapAttempts, key string) (TapConfirmation, error) {
	a, ok, err := store.Get(ctx, key)
	if err != nil {
		logging.L().Warnf("tap confirm: read attempt: %v", err)
		return TapConfirmation{}, &PaymentPendingError{Reason: PendingLookupFailed}
	}
	if !ok {
		return TapConfirmation{}, &PaymentPendingError{Reason: PendingNotConfirmed}
	}
	if a.State == data.AttemptApproved {
		// One approval per attempt: a replay is never a second capture.
		return TapConfirmation{}, &PaymentPendingError{Reason: PendingAlreadySettled}
	}
	pending := func(reason string) (TapConfirmation, error) {
		if merr := store.Mark(ctx, key, data.AttemptUnknown, data.AttemptUpdate{Reason: reason}); merr != nil && !errors.Is(merr, data.ErrAttemptSettled) {
			logging.L().Warnf("tap confirm: mark attempt unknown: %v", merr)
		}
		return TapConfirmation{}, &PaymentPendingError{Reason: reason}
	}
	if ctx.Err() != nil {
		return pending(PendingContextCancelled)
	}

	req := map[string]string{"idempotency_key": a.Key}
	if a.PIID != "" {
		req["payment_intent_id"] = a.PIID
	}
	body, err := json.Marshal(req)
	if err != nil {
		return pending(PendingLookupFailed)
	}
	out, err := paybroker.Call(ctx, paybroker.CallerCore, a.Provider, "session_status", body)
	if err != nil {
		if ctx.Err() != nil {
			return pending(PendingContextCancelled)
		}
		logging.L().Warnf("tap confirm: session_status: %v", err)
		return pending(PendingLookupFailed)
	}
	// The cloud's wire names (payment_intent_id, nested card) are mapped to
	// the neutral envelope of ADR-0198 §3.5 by the broker package.
	var st struct {
		PaymentIntentID     string `json:"id"`
		IdempotencyKey      string `json:"idempotency_key"`
		Status              string `json:"status"`
		AmountReceivedMinor int64  `json:"amount_received_minor"`
		Currency            string `json:"currency"`
		CardBrand           string `json:"card_brand"`
		Last4               string `json:"last4"`
	}
	if !json.Valid(out) || json.Unmarshal(paybroker.Neutral(out), &st) != nil {
		return pending(PendingLookupFailed)
	}
	if (st.IdempotencyKey != "" && st.IdempotencyKey != a.Key) || (a.PIID != "" && st.PaymentIntentID != "" && st.PaymentIntentID != a.PIID) {
		return pending(PendingNotConfirmed)
	}
	switch st.Status {
	case "succeeded":
	case "processing", "unknown":
		return pending(PendingProcessing)
	case "requires_capture":
		return pending(PendingRequiresCapture)
	default:
		return pending(PendingNotConfirmed)
	}
	if st.PaymentIntentID == "" && a.PIID == "" {
		return pending(PendingNotConfirmed) // a success that names no PaymentIntent is not evidence
	}
	if money.FromMinor(st.AmountReceivedMinor) != money.FromMinor(a.AmountMinor) || !strings.EqualFold(st.Currency, a.Currency) {
		return pending(PendingAmountMismatch)
	}

	c := TapConfirmation{PIID: st.PaymentIntentID}
	if c.PIID == "" {
		c.PIID = a.PIID
	}
	if b := strings.ToLower(strings.TrimSpace(st.CardBrand)); brandRE.MatchString(b) {
		c.CardBrand = b
	}
	if last4RE.MatchString(st.Last4) {
		c.Last4 = st.Last4
	}
	if err := store.Mark(ctx, key, data.AttemptApproved, data.AttemptUpdate{PIID: c.PIID, CardBrand: c.CardBrand, Last4: c.Last4}); err != nil {
		if errors.Is(err, data.ErrAttemptSettled) {
			return TapConfirmation{}, &PaymentPendingError{Reason: PendingAlreadySettled}
		}
		return TapConfirmation{}, fmt.Errorf("tap confirm: %w", &PaymentPendingError{Reason: PendingLookupFailed})
	}
	return c, nil
}
