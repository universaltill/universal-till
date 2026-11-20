package pages

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paybroker"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

// ut-docs#6455 K1b (design-batch1 §2.4, ux-batch2 A1-A6): what the till does
// with a card-present (Tap to Pay) payment core could not confirm.
//
//   - POST /pay/tap/check: "Check again" / "Cancel this payment". Any signed-in
//     operator (a sale-only cashier may only ASK). The attempt key is NEVER
//     read from the request: core takes it from the locked tender on the
//     register, so nobody can probe another key. Throttled per attempt.
//   - GET /ui/pay/tap/resolve (the panel) and POST /manager/pay/tap/resolve: a manager settles it. Manager role
//     AND a fresh PIN (checkStepUp); core asks the broker first and the answer
//     decides what may be chosen; every resolve is audited.
//
// Neither route moves money: they only read the broker and settle core's own
// record (ADR-0198 §3.10).

// tapCheckInterval is the least time between two checks of one attempt that
// reach the broker.
var tapCheckInterval = 2 * time.Second

// tapResolveAction is the permission a resolve needs: settling a card payment
// is as sensitive as a refund.
const tapResolveAction = "refund"

// Resolve reasons a manager gives when the broker's answer is not what decides.
var tapResolveReasons = map[string]bool{
	"customer_left": true, "card_shown_charged": true, "card_not_charged": true, "mismatch_refund_later": true,
}

// tapThrottle remembers when each attempt was last checked. Bounded: entries
// older than ten minutes go first, and past 256 the table is cleared (the
// worst case is one extra, harmless, broker read).
var tapThrottle = struct {
	mu   sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

func tapCheckAllowed(key string) bool {
	tapThrottle.mu.Lock()
	defer tapThrottle.mu.Unlock()
	now := time.Now()
	if t, ok := tapThrottle.last[key]; ok && now.Sub(t) < tapCheckInterval {
		return false
	}
	if len(tapThrottle.last) >= 128 {
		for k, t := range tapThrottle.last {
			if now.Sub(t) > 10*time.Minute {
				delete(tapThrottle.last, k)
			}
		}
		if len(tapThrottle.last) >= 256 {
			tapThrottle.last = map[string]time.Time{}
		}
	}
	tapThrottle.last[key] = now
	return true
}

// Verdicts of asking the broker about an attempt.
type tapVerdict int

const (
	tapPending  tapVerdict = iota // not known yet (processing, broker unreachable, ...)
	tapPaid                       // succeeded for exactly the sale amount
	tapNotPaid                    // the broker says no charge exists / it is cancelled
	tapMismatch                   // succeeded, but not for this amount/currency
)

// tapFacts is what core learned about the locked tap leg.
type tapFacts struct {
	Leg      pos.UnknownLeg
	Attempt  data.PaymentAttempt
	HaveRow  bool
	Verdict  tapVerdict
	Status   pos.TapStatus
	BrokerOK bool   // the broker answered
	ErrCode  string // paybroker.ErrorCode when it did not
	Reason   string // pending reason when Verdict == tapPending
}

// tapLockedLeg is the register's locked tap leg: the key comes from here.
func tapLockedLeg(d *common.Deps) (pos.UnknownLeg, bool) {
	if d.Engine == nil || d.Engine.TenderLock() == "" {
		return pos.UnknownLeg{}, false
	}
	l, _, ok := d.Engine.PendingTender().TapUnknownLeg()
	return l, ok
}

// tapAssess asks the broker about the leg's attempt and changes nothing.
func tapAssess(ctx context.Context, d *common.Deps, leg pos.UnknownLeg) tapFacts {
	f := tapFacts{Leg: leg}
	a, found, err := data.NewPaymentAttemptRepo(d.Db).Get(ctx, leg.RequestID)
	if err != nil {
		f.Reason, f.ErrCode = pos.PendingLookupFailed, "attempt_unreadable"
		return f
	}
	if !found {
		// No attempt row: no PaymentIntent was ever started through core, so
		// no money can have moved on it.
		f.Verdict = tapNotPaid
		return f
	}
	f.Attempt, f.HaveRow = a, true
	switch a.State {
	case data.AttemptApproved:
		f.Verdict, f.BrokerOK = tapPaid, true
		f.Status = pos.TapStatus{Status: "succeeded", AmountReceivedMinor: a.AmountMinor, Currency: a.Currency, PaymentIntentID: a.PIID, CardBrand: a.CardBrand, Last4: a.Last4}
		return f
	case data.AttemptCancelled, data.AttemptDeclined, data.AttemptResolvedNotCharged:
		f.Verdict = tapNotPaid
		return f
	}
	st, err := pos.TapBrokerStatus(ctx, a)
	if err != nil {
		f.ErrCode = paybroker.ErrorCode(err)
		if f.ErrCode == "payment_not_found" {
			f.Verdict, f.BrokerOK = tapNotPaid, true
			return f
		}
		f.Reason = pos.PendingLookupFailed
		return f
	}
	f.Status, f.BrokerOK = st, true
	switch st.Status {
	case "succeeded":
		if money.FromMinor(st.AmountReceivedMinor) == money.FromMinor(a.AmountMinor) && strings.EqualFold(st.Currency, a.Currency) {
			f.Verdict = tapPaid
		} else {
			f.Verdict, f.Reason = tapMismatch, pos.PendingAmountMismatch
		}
	case "requires_payment_method", "canceled":
		f.Verdict = tapNotPaid
	case "processing", "unknown":
		f.Reason = pos.PendingProcessing
	case "requires_capture":
		f.Reason = pos.PendingRequiresCapture
	default:
		f.Reason = pos.PendingNotConfirmed
	}
	return f
}

// tapCancelThenAssess closes the PaymentIntent of a leg the broker said was
// not paid, and only lets "not paid" stand when the cancel took or the broker
// says the intent is gone. A cancel that fails (e.g. already_succeeded: the
// customer tapped between status and cancel) re-reads the broker and returns
// those facts: paid stays paid, anything else is pending (ADR-0198 3.6.4).
func tapCancelThenAssess(ctx context.Context, d *common.Deps, f tapFacts) tapFacts {
	if f.Verdict != tapNotPaid || !f.HaveRow || !f.BrokerOK || f.Attempt.State == data.AttemptCancelled {
		return f
	}
	err := pos.TapBrokerCancel(ctx, f.Attempt)
	if err == nil {
		return f
	}
	logging.L().Warnf("tap: session_cancel: %s", paybroker.ErrorCode(err))
	f2 := tapAssess(ctx, d, f.Leg)
	if f2.Verdict == tapNotPaid && (f2.Status.Status == "canceled" || f2.ErrCode == "payment_not_found") {
		return f2
	}
	if f2.Verdict == tapNotPaid {
		// Not paid yet, but the intent is still open to a late tap.
		f2.Verdict, f2.Reason = tapPending, pos.PendingNotConfirmed
	}
	return f2
}

// brokerWord is the neutral status the resolve screen shows.
func (f tapFacts) brokerWord() string {
	switch {
	case !f.BrokerOK:
		return "unknown"
	case f.Verdict == tapPaid, f.Verdict == tapMismatch:
		return "succeeded"
	case f.Verdict == tapNotPaid:
		return "not_paid"
	}
	return "unknown"
}

func tapSignedIn(w http.ResponseWriter, r *http.Request) bool {
	if auth.Disabled(os.Getenv("UT_AUTH")) {
		return true
	}
	if _, ok := auth.FromContext(r.Context()); ok {
		return true
	}
	http.Error(w, "sign in required", http.StatusUnauthorized)
	return false
}

// renderTapBasket re-renders the basket, with panel (when set) as the tap
// screen. The locked tender derives its own pending/paid panel (pos.Basket).
func renderTapBasket(w http.ResponseWriter, r *http.Request, d *common.Deps, panel *pos.TapPanel, status int) {
	locale := httpx.ResolveLocale(w, r)
	b := d.Engine.Basket()
	if panel != nil {
		b.TapPanel = panel
	}
	view, err := ui.NewBasketView(httpx.FuncsFor(locale))
	if err != nil {
		http.Error(w, httpx.T(locale, "common.error.server"), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = view.Render(w, b)
}

// tapUnlock settles a leg the broker says was not paid: its attempt row is
// closed, earlier captured legs are returned, and the next try mints a new
// attempt key. Reports whether it unlocked.
func tapUnlock(ctx context.Context, d *common.Deps, leg pos.UnknownLeg, state, reason string) {
	settleAttempt(ctx, data.NewPaymentAttemptRepo(d.Db), leg.RequestID, state, reason)
	stash := d.Engine.PendingTender()
	attemptID := d.Engine.TenderAttemptID()
	if len(stash.Captured) > 0 {
		reversePaymentLegs(ctx, d, d.Engine.NotHandedOff(stash.Captured), "tap payment resolved as not charged")
	}
	d.Engine.RotateTenderAttemptIDIf(attemptID)
}

// tapNote remembers why the leg is pending, on the row and on the register.
func tapNote(ctx context.Context, d *common.Deps, f tapFacts) {
	if f.Reason == "" {
		return
	}
	d.Engine.SetTapReason(f.Leg.RequestID, f.Reason)
	if f.HaveRow {
		settleAttempt(ctx, data.NewPaymentAttemptRepo(d.Db), f.Leg.RequestID, data.AttemptUnknown, f.Reason)
	}
}

func registerTapPay(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /pay/tap/check", func(w http.ResponseWriter, r *http.Request) {
		if !tapSignedIn(w, r) {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		leg, ok := tapLockedLeg(d)
		if !ok {
			http.Error(w, httpx.T(locale, "tap.nothing_pending"), http.StatusConflict)
			return
		}
		// The form is ignored on purpose: only the register's own locked
		// tender names the attempt. The one flag it may carry is "cancel".
		_ = r.ParseForm()
		cancel := r.Form.Get("cancel") == "1"
		if !tapCheckAllowed(leg.RequestID) {
			renderTapBasket(w, r, d, &pos.TapPanel{State: "pending", Method: leg.MethodID, Amount: leg.Amount, Reason: leg.TapReason, Still: true}, http.StatusOK)
			return
		}
		f := tapAssess(r.Context(), d, leg)
		repo := data.NewPaymentAttemptRepo(d.Db)
		if f.Verdict == tapPending && cancel && f.HaveRow && f.BrokerOK && f.Reason == pos.PendingNotConfirmed {
			// "Cancel this payment": a payment the broker holds in no
			// confirmable state is cancelled there before the till lets go.
			if err := pos.TapBrokerCancel(r.Context(), f.Attempt); err == nil {
				f = tapAssess(r.Context(), d, leg)
			}
		}
		f = tapCancelThenAssess(r.Context(), d, f)
		switch f.Verdict {
		case tapPaid:
			conf, err := pos.ConfirmTap(r.Context(), repo, leg.RequestID)
			var pe *pos.PaymentPendingError
			switch {
			case err == nil:
				d.Engine.MarkTapPaid(leg.RequestID, conf.CardBrand, conf.Last4)
			case errors.As(err, &pe) && pe.Reason == pos.PendingAlreadySettled:
				d.Engine.MarkTapPaid(leg.RequestID, f.Attempt.CardBrand, f.Attempt.Last4)
			default:
				f.Verdict, f.Reason = tapPending, pendingReasonOf(err)
				tapNote(r.Context(), d, f)
				renderTapBasket(w, r, d, nil, http.StatusOK)
				return
			}
			renderTapBasket(w, r, d, nil, http.StatusOK)
		case tapNotPaid:
			tapUnlock(r.Context(), d, leg, data.AttemptCancelled, "not_paid")
			renderTapBasket(w, r, d, &pos.TapPanel{State: "not_paid", Method: leg.MethodID, Amount: leg.Amount}, http.StatusOK)
		default:
			tapNote(r.Context(), d, f)
			p := &pos.TapPanel{State: "pending", Method: leg.MethodID, Amount: leg.Amount, Reason: f.Reason, Still: true}
			renderTapBasket(w, r, d, p, http.StatusOK)
		}
	})

	mux.HandleFunc("GET /ui/pay/tap/resolve", func(w http.ResponseWriter, r *http.Request) {
		if !tapSignedIn(w, r) {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		leg, ok := tapLockedLeg(d)
		if !ok {
			http.Error(w, httpx.T(locale, "tap.nothing_pending"), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !canPerform(d, r, tapResolveAction) {
			// The cashier is told to ask a manager; nothing else is revealed.
			httpx.RenderPartial("ui/partials/tap_resolve.html", map[string]any{"Ask": true, "Amount": leg.Amount})(w, r)
			return
		}
		f := tapAssess(r.Context(), d, leg)
		view := map[string]any{
			"Amount": leg.Amount, "Broker": f.brokerWord(), "Mismatch": f.Verdict == tapMismatch,
			"Received": money.FromMinor(f.Status.AmountReceivedMinor), "Paid": f.Verdict == tapPaid,
			"NotPaid": f.Verdict == tapNotPaid, "Unknown": f.Verdict == tapPending,
			"Started": f.Attempt.CreatedAt.Local().Format("15:04"), "Diff": money.FromMinor(f.Status.AmountReceivedMinor).Sub(leg.Amount),
		}
		httpx.RenderPartial("ui/partials/tap_resolve.html", view)(w, r)
	})

	mux.HandleFunc("POST /manager/pay/tap/resolve", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		if err := r.ParseForm(); err != nil {
			http.Error(w, httpx.T(locale, "common.error.server"), http.StatusBadRequest)
			return
		}
		// Manager role AND a fresh PIN, before anything is read.
		elev := checkStepUp(d, r, tapResolveAction, strings.TrimSpace(r.Form.Get("override_pin")))
		if elev.Outcome != elevated {
			http.Error(w, httpx.T(locale, "common.error.manager_or_admin_required"), http.StatusForbidden)
			return
		}
		leg, ok := tapLockedLeg(d)
		if !ok {
			http.Error(w, httpx.T(locale, "tap.nothing_pending"), http.StatusConflict)
			return
		}
		decision := r.Form.Get("decision")
		reason := r.Form.Get("reason")
		if decision != "approve" && decision != "not_charged" && decision != "override" {
			http.Error(w, httpx.T(locale, "tap.resolve.bad_choice"), http.StatusBadRequest)
			return
		}
		f := tapAssess(r.Context(), d, leg)
		repo := data.NewPaymentAttemptRepo(d.Db)
		refuse := func(status int, key string) {
			http.Error(w, httpx.T(locale, key), status)
		}
		needReason := func() bool {
			if !tapResolveReasons[reason] {
				refuse(http.StatusUnprocessableEntity, "tap.resolve.reason_required")
				return true
			}
			return false
		}
		audit := func(mode, outcome string) {
			p := map[string]any{
				"decision": decision, "outcome": outcome, "mode": mode, "broker_status": f.brokerWord(),
				"amount_minor": leg.Amount.Minor(), "currency": leg.Currency, "attempt_state": f.Attempt.State,
				"manager_id": elev.ApproverID, "reason_text": httpx.T("en", "tap.resolve.audit_reason"),
			}
			if reason != "" {
				p["reason"] = reason
			}
			if f.ErrCode != "" {
				p["broker_error"] = f.ErrCode
			}
			if f.Status.AmountReceivedMinor != 0 {
				p["received_minor"] = f.Status.AmountReceivedMinor
			}
			settingsAudit(r, data.NewPOSRepo(d.Db), elev, "payment_attempt", leg.RequestID, "tap_resolve", p)
		}
		if decision == "not_charged" {
			// Close the PaymentIntent before letting go; a cancel that finds
			// it paid turns the verdict to paid and the choice is refused.
			f = tapCancelThenAssess(r.Context(), d, f)
		}
		override := !f.BrokerOK || f.Verdict == tapPending
		switch {
		case f.Verdict == tapPaid && decision == "approve":
			conf, err := pos.ConfirmTap(r.Context(), repo, leg.RequestID)
			var pe *pos.PaymentPendingError
			if err != nil && !(errors.As(err, &pe) && pe.Reason == pos.PendingAlreadySettled) {
				refuse(http.StatusConflict, "tap.pending.still")
				return
			}
			d.Engine.MarkTapPaid(leg.RequestID, conf.CardBrand, conf.Last4)
			audit("broker", "approved")
			tellNativeResolver(r.Context(), r, f, devicepay.OutcomeCharged)
		case f.Verdict == tapPaid, f.Verdict == tapMismatch && decision != "override":
			// A wrong "not charged" double-charges: refused while the broker
			// says paid. A mismatch can only be accepted by override.
			refuse(http.StatusConflict, "tap.resolve.broker_says_paid")
			return
		case f.Verdict == tapMismatch:
			if needReason() {
				return
			}
			settleOverrideApproved(r.Context(), repo, f)
			brand, last4 := pos.SafeCardFacts(f.Status.CardBrand, f.Status.Last4)
			d.Engine.MarkTapPaid(leg.RequestID, brand, last4)
			audit("manager_override", "approved_mismatch")
			tellNativeResolver(r.Context(), r, f, devicepay.OutcomeCharged)
		case f.Verdict == tapNotPaid && decision == "not_charged":
			tapUnlock(r.Context(), d, leg, data.AttemptResolvedNotCharged, "manager_resolved")
			audit("broker", "not_charged")
			tellNativeResolver(r.Context(), r, f, devicepay.OutcomeNotCharged)
			renderTapBasket(w, r, d, &pos.TapPanel{State: "not_paid", Method: leg.MethodID, Amount: leg.Amount}, http.StatusOK)
			return
		case f.Verdict == tapNotPaid:
			refuse(http.StatusConflict, "tap.resolve.nothing_to_approve")
			return
		case override && decision == "not_charged":
			if needReason() {
				return
			}
			tapUnlock(r.Context(), d, leg, data.AttemptResolvedNotCharged, "manager_override")
			audit("manager_override", "not_charged")
			tellNativeResolver(r.Context(), r, f, devicepay.OutcomeNotCharged)
			renderTapBasket(w, r, d, &pos.TapPanel{State: "not_paid", Method: leg.MethodID, Amount: leg.Amount}, http.StatusOK)
			return
		case override:
			if needReason() {
				return
			}
			settleOverrideApproved(r.Context(), repo, f)
			d.Engine.MarkTapPaid(leg.RequestID, f.Attempt.CardBrand, f.Attempt.Last4)
			audit("manager_override", "approved_without_broker")
			tellNativeResolver(r.Context(), r, f, devicepay.OutcomeCharged)
		default:
			refuse(http.StatusConflict, "tap.resolve.bad_choice")
			return
		}
		renderTapBasket(w, r, d, nil, http.StatusOK)
	})
}

// settleOverrideApproved records a manager's acceptance of a payment the
// broker could not confirm for this amount (or at all): the row is approved
// with whatever the broker did report, never anything from the request.
func settleOverrideApproved(ctx context.Context, repo *data.PaymentAttemptRepo, f tapFacts) {
	if !f.HaveRow {
		return
	}
	brand, last4 := pos.SafeCardFacts(f.Status.CardBrand, f.Status.Last4)
	err := repo.Mark(ctx, f.Leg.RequestID, data.AttemptApproved, data.AttemptUpdate{
		PIID: f.Status.PaymentIntentID, CardBrand: brand, Last4: last4, Reason: "manager_override",
	})
	if err != nil && !errors.Is(err, data.ErrAttemptSettled) {
		logging.L().Warnf("tap resolve: mark approved: %v", err)
	}
}

// pendingReasonOf is the reason of a *pos.PaymentPendingError, else lookup_failed.
func pendingReasonOf(err error) string {
	var pe *pos.PaymentPendingError
	if errors.As(err, &pe) {
		return pe.Reason
	}
	return pos.PendingLookupFailed
}

// tellNativeResolver lets the phone shell forget the attempt core just
// settled, so another sale may start there. Best effort: a till with no
// native shell (desktop) has no resolver, and core's own record is already
// settled, so a refusal here changes nothing the manager chose. The actor is
// the signed-in user (devicepay.Resolve refuses anyone who is not a manager,
// and no user at all); the key and provider come from the attempt row, never
// the request.
func tellNativeResolver(ctx context.Context, r *http.Request, f tapFacts, outcome string) {
	if !f.HaveRow {
		return
	}
	var who devicepay.Actor
	if u, ok := auth.FromContext(r.Context()); ok {
		who = u
	}
	res := devicepay.Resolve(ctx, who, f.Attempt.Provider, f.Leg.RequestID, outcome)
	if res.Reason != "" && res.Reason != devicepay.ReasonNotForeground {
		logging.L().Warnf("tap resolve: native resolver answered %q", res.Reason)
	}
}
