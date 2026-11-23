package pages

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#6795 (ADR-0198 §6.8 note): a tap attempt a restart, "New sale" or
// park left behind is a payment needing attention (data.PaymentAttempt.
// NeedsAttention). Two things follow, both here:
//
//   - tapAttentionGate refuses to START another tap payment on that provider
//     until a manager settles it: a new attempt key lets a paired phone reader
//     release the old attempt as "not charged", and the customer could be
//     charged twice.
//   - GET /api/pay/attempts/attention and POST .../resolve let a manager list
//     and settle such an attempt by key without the register's lock (the
//     existing /manager/pay/tap/resolve works on the locked leg only).
//
// The row of the cashier register's live locked leg is owned by the sale
// screen's own pending panel: it is neither listed nor resolvable here, and
// never gates that register's own retry. An unattended engine's locked leg
// (self-order kiosk, table-QR basket) has no staff panel — its customer was
// sent to the counter — so it gates the cashier and is listed and resolvable
// here (review R2); tapAuthorize refuses to treat a charged_no_sale key as a
// payment, so a retry on that engine can never turn it into a sale (R1).

// attentionListMax bounds the manager list.
const attentionListMax = 100

// liveLockedKeys is the attempt keys of the engine's locked unknown legs, or
// nil when the engine holds no lock.
func liveLockedKeys(engine *pos.Service) map[string]bool {
	if engine == nil || engine.TenderLock() == "" {
		return nil
	}
	keys := map[string]bool{}
	for _, l := range engine.PendingTender().Unknown {
		keys[l.RequestID] = true
	}
	return keys
}

// staffLockedKeys is liveLockedKeys of the cashier register's engine, the
// only one with a staff pending panel that owns its locked leg.
func staffLockedKeys(d *common.Deps) map[string]bool { return liveLockedKeys(d.Engine) }

// tapAttentionGate refuses to START a tap payment on a provider while a
// payment needing attention exists for it (ut-docs#6795). Runs before any
// reader resolution, fiscal start, attempt row, plugin dispatch,
// session_create or reader.collect. Non-tap legs are never looked at, so cash
// and other methods are never refused by it. A DB error on the attempt
// table refuses the tap leg; one on the plugin list refuses the tender, as the
// authorize loop's own read of it would (fail closed).
func tapAttentionGate(ctx context.Context, d *common.Deps, engine *pos.Service, payments []pos.PaymentInput) error {
	own := liveLockedKeys(engine)
	repo := data.NewPaymentAttemptRepo(d.Db)
	checked := map[string]bool{}
	for _, p := range payments {
		provider, isTap, err := tapEntryErr(ctx, d, p.MethodID)
		if err != nil {
			// Fail closed (review R4): not knowing whether a leg is a tap
			// refuses the tender, as the authorize loop's own read would.
			logging.L().Errorf("tap gate: %v", err)
			return &paymentDeclinedError{Method: p.MethodID}
		}
		if !isTap || provider == "" || checked[provider] {
			continue
		}
		checked[provider] = true
		rows, err := repo.ListNeedingAttention(ctx, time.Now(), provider, 0) // 0: the repo's own scan cap
		if err != nil {
			logging.L().Errorf("tap gate: %v", err)
			return &tapStartFailedError{Method: p.MethodID, NeedsAttention: true}
		}
		for _, a := range rows {
			if !own[a.Key] {
				return &tapStartFailedError{Method: p.MethodID, NeedsAttention: true}
			}
		}
	}
	return nil
}

type attentionAttemptDTO struct {
	Key               string `json:"key"`
	Provider          string `json:"provider"`
	AmountMinor       int64  `json:"amount_minor"`
	Currency          string `json:"currency"`
	State             string `json:"state"`
	Reason            string `json:"reason"`
	ProviderPaymentID string `json:"provider_payment_id,omitempty"`
	CardBrand         string `json:"card_brand,omitempty"`
	Last4             string `json:"last4,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

func attentionDTO(a data.PaymentAttempt) attentionAttemptDTO {
	return attentionAttemptDTO{
		Key: a.Key, Provider: a.Provider, AmountMinor: money.FromMinor(a.AmountMinor).Minor(), Currency: a.Currency,
		State: a.State, Reason: a.Reason, ProviderPaymentID: a.PIID, CardBrand: a.CardBrand, Last4: a.Last4,
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: a.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func writeAttentionError(w http.ResponseWriter, status int, locale, code, key string) {
	writeJSON(w, status, map[string]any{"data": nil, "error": map[string]string{"code": code, "message": httpx.T(locale, key)}})
}

func registerTapAttention(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /api/pay/attempts/attention", func(w http.ResponseWriter, r *http.Request) {
		if !tapSignedIn(w, r) {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		if !canPerform(d, r, tapResolveAction) {
			writeAttentionError(w, http.StatusForbidden, locale, "forbidden", "common.error.manager_or_admin_required")
			return
		}
		rows, err := data.NewPaymentAttemptRepo(d.Db).ListNeedingAttention(r.Context(), time.Now(), "", attentionListMax)
		if err != nil {
			logging.L().Errorf("tap attention list: %v", err)
			writeAttentionError(w, http.StatusInternalServerError, locale, "attempts_unreadable", "common.error.server")
			return
		}
		live := staffLockedKeys(d)
		out := make([]attentionAttemptDTO, 0, len(rows))
		for _, a := range rows {
			if !live[a.Key] {
				out = append(out, attentionDTO(a))
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"attempts": out, "count": len(out)}, "error": nil})
	})

	mux.HandleFunc("POST /api/pay/attempts/attention/resolve", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		fail := func(status int, code, key string) { writeAttentionError(w, status, locale, code, key) }
		if err := r.ParseForm(); err != nil {
			fail(http.StatusBadRequest, "bad_request", "common.error.server")
			return
		}
		// Manager role AND a fresh PIN, before anything is read.
		elev := checkStepUp(d, r, tapResolveAction, strings.TrimSpace(r.Form.Get("override_pin")))
		if elev.Outcome != elevated {
			fail(http.StatusForbidden, "step_up_required", "common.error.manager_or_admin_required")
			return
		}
		decision, reason, key := r.Form.Get("decision"), r.Form.Get("reason"), r.Form.Get("key")
		if decision != "not_charged" && decision != "charged_refund_at_provider" {
			fail(http.StatusBadRequest, "bad_choice", "tap.resolve.bad_choice")
			return
		}
		if key == "" || len(key) > 200 {
			fail(http.StatusBadRequest, "bad_request", "tap.resolve.bad_choice")
			return
		}
		// ut-docs#6908 (rule of #6805/#6784): the leg guard comes first, so a
		// second resolve queued behind this one re-reads the settled row. From
		// the first broker ask on, the decision and its compensations (cancel,
		// attempt write, audit, native release) run detached; a dropped
		// connection must not corrupt the facts or lose the decision. Bounded:
		// paybroker.Timeout, nativeResolveTimeout.
		if !tapLegInFlight.acquire(key) {
			fail(http.StatusConflict, "in_progress", "tap.attention.in_progress")
			return
		}
		defer tapLegInFlight.release(key)
		repo := data.NewPaymentAttemptRepo(d.Db)
		a, found, err := repo.Get(r.Context(), key)
		if err != nil {
			logging.L().Errorf("tap attention resolve: %v", err)
			fail(http.StatusInternalServerError, "attempts_unreadable", "common.error.server")
			return
		}
		if !found {
			fail(http.StatusNotFound, "not_found", "tap.attention.gone")
			return
		}
		switch {
		case staffLockedKeys(d)[key]:
			fail(http.StatusConflict, "in_progress", "tap.attention.in_progress")
			return
		case a.State != data.AttemptPending && a.State != data.AttemptUnknown:
			fail(http.StatusConflict, "settled", "tap.attention.gone")
			return
		case !a.NeedsAttention(time.Now()):
			fail(http.StatusConflict, "in_progress", "tap.attention.in_progress")
			return
		}

		// Ask the broker first; its answer decides what may be chosen. A
		// cancel that finds the intent paid turns the verdict to paid.
		ctx := context.WithoutCancel(r.Context())
		f := tapAssess(ctx, d, pos.UnknownLeg{RequestID: a.Key})
		if decision == "not_charged" {
			f = tapCancelThenAssess(ctx, d, f)
		}
		override := !f.BrokerOK || f.Verdict == tapPending
		brand, last4 := pos.SafeCardFacts(f.Status.CardBrand, f.Status.Last4)

		var mode, outcome, newState string
		var upd data.AttemptUpdate
		nativeOutcome := devicepay.OutcomeNotCharged
		switch {
		case !override && (f.Verdict == tapPaid || f.Verdict == tapMismatch) && decision == "not_charged":
			// A wrong "not charged" double-charges: refused while the broker says paid.
			fail(http.StatusConflict, "broker_says_paid", "tap.resolve.broker_says_paid")
			return
		case !override && f.Verdict == tapNotPaid && decision == "charged_refund_at_provider":
			fail(http.StatusConflict, "nothing_to_approve", "tap.resolve.nothing_to_approve")
			return
		case !override && decision == "charged_refund_at_provider":
			mode, outcome, newState = "broker", "charged_no_sale", data.AttemptApproved
			upd = data.AttemptUpdate{PIID: f.Status.PaymentIntentID, CardBrand: brand, Last4: last4, Reason: data.AttemptReasonChargedNoSale}
			nativeOutcome = devicepay.OutcomeCharged
		case !override:
			mode, outcome, newState = "broker", "not_charged", data.AttemptResolvedNotCharged
			upd = data.AttemptUpdate{Reason: "manager_resolved"}
		default:
			if !tapResolveReasons[reason] {
				fail(http.StatusUnprocessableEntity, "reason_required", "tap.resolve.reason_required")
				return
			}
			mode = "manager_override"
			if decision == "charged_refund_at_provider" {
				outcome, newState = "charged_no_sale", data.AttemptApproved
				upd = data.AttemptUpdate{PIID: f.Status.PaymentIntentID, CardBrand: brand, Last4: last4, Reason: data.AttemptReasonChargedNoSale}
				nativeOutcome = devicepay.OutcomeCharged
			} else {
				outcome, newState = "not_charged", data.AttemptResolvedNotCharged
				upd = data.AttemptUpdate{Reason: "manager_override"}
			}
		}
		// Never touches d.Engine: there is no sale to finish or unlock.
		if err := repo.Mark(ctx, key, newState, upd); err != nil {
			if err == data.ErrAttemptSettled {
				fail(http.StatusConflict, "settled", "tap.attention.gone")
				return
			}
			logging.L().Errorf("tap attention resolve: mark: %v", err)
			fail(http.StatusInternalServerError, "attempts_unreadable", "common.error.server")
			return
		}
		p := map[string]any{
			"decision": decision, "outcome": outcome, "mode": mode, "broker_status": f.brokerWord(),
			"amount_minor": a.AmountMinor, "currency": a.Currency, "attempt_state": a.State,
			"manager_id": elev.ApproverID, "reason_text": httpx.T("en", "tap.resolve.audit_reason"),
			"context": "needs_attention",
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
		settingsAudit(r.WithContext(ctx), data.NewPOSRepo(d.Db), elev, "payment_attempt", key, "tap_resolve", p)
		f.Attempt, f.HaveRow = a, true
		tellNativeResolver(ctx, r, f, nativeOutcome)
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"key": key, "state": newState, "outcome": outcome, "broker_status": f.brokerWord(),
		}, "error": nil})
	})
}
