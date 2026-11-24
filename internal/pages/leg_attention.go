package pages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#6653 slice 3 (ADR-0136 Decision 9.6): payment legs needing attention.
// A leg whose newest journal row is reverse_failed (the till could not send
// the money back) or warned (a restart left it unsettled) is listed for a
// manager on Settings -> Payments. From there a manager can
//
//   - Retry a reverse_failed leg: ONE more reversal, sent from the row's own
//     exact amount, tip, reference and reader. Never for a warned leg: money
//     may already have moved, and only the provider knows.
//   - Resolve a warned or reverse_failed leg once it is refunded or checked at
//     the provider. Nothing is sent; the leg just leaves the list.
//
// Both need the refund permission AND a fresh manager PIN, are audited, and
// never touch a tender outcome or block checkout.

// legAttentionAction is the permission both routes and the list need (a
// literal, so the permission page guard can see it): the
// same one the tap attention list uses.
const legAttentionAction = "refund"

// legAttentionStates are the newest-row states the list shows.
var legAttentionStates = []string{data.LegReverseFailed, data.LegWarned}

const (
	legAttentionTarget  = "#pay-attention"
	legAttentionMsg     = "#pay-attention-msg"
	legAttentionNoteMax = 200
	legAttentionIDMax   = 200
)

// legAttentionDTO is one listed leg in the JSON list. There is no sale id: a
// leg that needs attention never became one.
type legAttentionDTO struct {
	RequestID      string `json:"request_id"`
	Method         string `json:"method"`
	AmountMinor    int64  `json:"amount_minor"`
	Currency       string `json:"currency"`
	State          string `json:"state"`
	Reason         string `json:"reason"`
	TransactionRef string `json:"transaction_ref,omitempty"`
	At             string `json:"at"`
}

// maskedLegRef is the provider reference shown anywhere but the journal: the
// last four characters only, like the Problems log.
func maskedLegRef(ref string) string {
	if ref == "" {
		return ""
	}
	return maskTxnRef(ref)
}

// legRefTail is the last four characters of a reference longer than four, for
// the "ending ..." label; shorter references show nothing.
func legRefTail(ref string) string {
	r := []rune(ref)
	if len(r) <= 4 {
		return ""
	}
	return string(r[len(r)-4:])
}

func legAttentionDTOs(ctx context.Context, d *common.Deps) ([]legAttentionDTO, error) {
	rows, err := data.NewPaymentLegJournalRepo(d.Db).ListCurrent(ctx, legAttentionStates, attentionListMax)
	if err != nil {
		return nil, err
	}
	out := make([]legAttentionDTO, 0, len(rows))
	for _, e := range rows {
		out = append(out, legAttentionDTO{
			RequestID: e.RequestID, Method: e.MethodID, AmountMinor: money.FromMinor(e.AmountMinor).Minor(), Currency: e.Currency,
			State: e.State, Reason: e.Reason, TransactionRef: maskedLegRef(e.TransactionRef), At: e.At.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

// legRow is one listed leg, ready for payment_attention.html.
type legRow struct {
	N                      int
	RequestID              string
	Amount, Method, Reason string
	RefTail                string
	State                  string
	At                     time.Time
	Retryable              bool
}

// legCardView is the data of payment_attention.html.
type legCardView struct {
	Rows      []legRow
	ShowList  bool // false for a refused session: no rows and no empty line
	Flash     string
	FlashWarn bool
}

func legReasonKey(reason string) string {
	switch reason {
	case data.LegReasonRestart:
		return "pay.attention.reason.till_restarted"
	case data.LegReasonReversalFailed:
		return "pay.attention.reason.reversal_failed"
	case data.LegReasonMainTillOnly:
		return "pay.attention.reason.main_till_only"
	case data.LegReasonNoRefundPlugin:
		return "pay.attention.reason.no_refund_plugin"
	}
	return "" // a legacy or empty reason shows no line
}

// legMethodLabels maps tender method ids to their on-screen names.
func legMethodLabels(ctx context.Context, d *common.Deps, locale string) map[string]string {
	out := map[string]string{}
	methods, err := data.NewPOSRepo(d.Db).ListActivePaymentMethods(ctx)
	if err != nil {
		return out
	}
	for _, m := range methods {
		out[m.ID] = httpx.T(locale, m.Name)
	}
	return out
}

func legAmount(e data.LegEntry, locale string) string {
	return httpx.FormatMoneyInDisplay(money.FromMinor(e.AmountMinor).Minor(), strings.ToUpper(e.Currency), locale)
}

func legRows(ctx context.Context, d *common.Deps, locale string, entries []data.LegEntry) []legRow {
	labels := legMethodLabels(ctx, d, locale)
	rows := make([]legRow, 0, len(entries))
	for i, e := range entries {
		method := labels[e.MethodID]
		if method == "" {
			method = e.MethodID
		}
		reason := ""
		if k := legReasonKey(e.Reason); k != "" {
			reason = httpx.T(locale, k)
		}
		rows = append(rows, legRow{
			N: i + 1, RequestID: e.RequestID, Amount: legAmount(e, locale), Method: method, Reason: reason,
			RefTail: legRefTail(e.TransactionRef), State: e.State, At: e.At, Retryable: e.State == data.LegReverseFailed,
		})
	}
	return rows
}

// legSummary is the PIN dialog's description of the action: amount and method.
func legSummary(ctx context.Context, d *common.Deps, locale, key string, e data.LegEntry) string {
	method := legMethodLabels(ctx, d, locale)[e.MethodID]
	if method == "" {
		method = e.MethodID
	}
	return fmt.Sprintf(httpx.T(locale, key), legAmount(e, locale), method)
}

// writeLegCard answers with the card fragment and, when set, a flash. Every
// answer to an action retargets to #pay-attention (the row forms and the PIN
// dialog aim at the flash region, so the elevation hint never replaces the
// card), whatever the status; app.js swaps a 4xx text/html body.
func writeLegCard(w http.ResponseWriter, r *http.Request, d *common.Deps, status int, flash string, warn, showList bool) {
	locale := httpx.ResolveLocale(w, r)
	view := legCardView{Flash: flash, FlashWarn: warn, ShowList: showList}
	if showList {
		entries, err := data.NewPaymentLegJournalRepo(d.Db).ListCurrent(r.Context(), legAttentionStates, attentionListMax)
		if err != nil {
			logging.L().Errorf("payment legs attention list: %v", err)
			view.Flash, view.FlashWarn = httpx.T(locale, "common.error.server"), true
		} else {
			view.Rows = legRows(r.Context(), d, locale, entries)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("HX-Retarget", legAttentionTarget)
	w.Header().Set("HX-Reswap", "innerHTML")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	httpx.RenderPartial("ui/partials/payment_attention.html", view)(w, r)
}

// validLegNote: optional, one line, at most legAttentionNoteMax runes.
func validLegNote(note string) bool {
	if utf8.RuneCountInString(note) > legAttentionNoteMax {
		return false
	}
	for _, c := range note {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func registerLegAttention(mux *http.ServeMux, d *common.Deps) {
	// GET /ui/settings/payment-attention: the lazy-loaded card on Settings ->
	// Payments. Empty (and silent) for anyone without the refund permission
	// and when nothing needs attention, so most tills never see it.
	mux.HandleFunc("GET /ui/settings/payment-attention", func(w http.ResponseWriter, r *http.Request) {
		if !tapSignedIn(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !canPerform(d, r, legAttentionAction) {
			return
		}
		entries, err := data.NewPaymentLegJournalRepo(d.Db).ListCurrent(r.Context(), legAttentionStates, attentionListMax)
		if err != nil {
			logging.L().Errorf("payment legs attention list: %v", err)
			return
		}
		if len(entries) == 0 {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		httpx.RenderPartial("ui/partials/payment_attention.html", legCardView{ShowList: true, Rows: legRows(r.Context(), d, locale, entries)})(w, r)
	})

	// prelude is the shared start of Retry and Resolve: permission, form, id,
	// the leg (for the PIN dialog's summary), then the step-up PIN. ok=false
	// means the answer was written.
	prelude := func(w http.ResponseWriter, r *http.Request, action, summaryKey string, extra func() ([]elevationHiddenField, bool)) (id string, elev elevationCheck, ok bool) {
		locale := httpx.ResolveLocale(w, r)
		fail := func(status int, key string) {
			writeLegCard(w, r, d, status, httpx.T(locale, key), true, status != http.StatusForbidden)
		}
		if !canPerform(d, r, legAttentionAction) {
			fail(http.StatusForbidden, "pay.attention.forbidden")
			return
		}
		if err := r.ParseForm(); err != nil {
			fail(http.StatusBadRequest, "common.error.server")
			return
		}
		id = r.Form.Get("request_id")
		if id == "" || len(id) > legAttentionIDMax {
			fail(http.StatusBadRequest, "pay.attention.gone")
			return "", elev, false
		}
		cur, found, err := data.NewPaymentLegJournalRepo(d.Db).Latest(r.Context(), id)
		if err != nil {
			logging.L().Errorf("payment legs attention %s: %v", action, err)
			fail(http.StatusInternalServerError, "common.error.server")
			return "", elev, false
		}
		if !found {
			fail(http.StatusNotFound, "pay.attention.gone")
			return "", elev, false
		}
		hidden := []elevationHiddenField{{Name: "request_id", Value: id}}
		if extra != nil {
			more, valid := extra()
			if !valid {
				fail(http.StatusBadRequest, "pay.attention.note_invalid")
				return "", elev, false
			}
			hidden = append(hidden, more...)
		}
		elev = checkStepUp(d, r, legAttentionAction, strings.TrimSpace(r.Form.Get("override_pin")))
		if elev.Outcome != elevated {
			renderElevationPrompt(w, r, "/api/pay/legs/"+action, legAttentionMsg, legSummary(r.Context(), d, locale, summaryKey, cur), hidden, elev)
			return "", elev, false
		}
		return id, elev, true
	}

	mux.HandleFunc("POST /api/pay/legs/retry", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		fail := func(status int, key string) {
			writeLegCard(w, r, d, status, httpx.T(locale, key), true, true)
		}
		id, elev, ok := prelude(w, r, "retry", "pay.attention.retry_summary", nil)
		if !ok {
			return
		}
		// One Retry or Resolve per leg at a time, in the tap resolve's key
		// space (the authorize request id): a double click, or a Resolve racing
		// a Retry, finds the key busy. BeginRetry refusing a reversing row is
		// the durable backstop if this guard is ever lost.
		if !tapLegInFlight.acquire(id) {
			fail(http.StatusConflict, "pay.attention.in_progress")
			return
		}
		defer tapLegInFlight.release(id)
		repo := data.NewPaymentLegJournalRepo(d.Db)
		cur, found, err := repo.Latest(r.Context(), id)
		switch {
		case err != nil:
			logging.L().Errorf("payment legs retry: %v", err)
			fail(http.StatusInternalServerError, "common.error.server")
			return
		case !found:
			fail(http.StatusNotFound, "pay.attention.gone")
			return
		case cur.State != data.LegReverseFailed:
			fail(http.StatusConflict, "pay.attention.not_retryable")
			return
		case tapRefundMainOnly(r.Context(), d, cur.MethodID):
			// An additional till never sends a card tap refund; the main till does.
			fail(http.StatusConflict, "tap.reverse.main_only")
			return
		}
		// From the reversing row on, the send and its records run detached: a
		// dropped connection must neither lose the outcome nor the audit
		// (ut-docs#6805). Bounded by the plugin call timeout.
		ctx := context.WithoutCancel(r.Context())
		row, n, err := repo.BeginRetry(ctx, id)
		switch {
		case errors.Is(err, data.ErrLegJournalTransition):
			fail(http.StatusConflict, "pay.attention.not_retryable")
			return
		case errors.Is(err, data.ErrLegJournalNotFound):
			fail(http.StatusNotFound, "pay.attention.gone")
			return
		case err != nil:
			// Fail closed: nothing is in flight yet, so a journal that cannot
			// record the attempt means nothing is sent.
			logging.L().Errorf("payment legs retry: could not record the attempt for authorize_request_id %s, nothing sent: %v (ut-docs#6653)", id, err)
			fail(http.StatusInternalServerError, "common.error.server")
			return
		}
		leg := legFromRow(row)
		dispatched, sendErr := sendLegReversal(ctx, d, leg, n)
		outcome, reason := data.LegReversed, ""
		switch {
		case sendErr != nil:
			outcome, reason = data.LegReverseFailed, data.LegReasonReversalFailed
			logging.L().Warnf("manager retry of a failed reversal FAILED again — method %q, amount %d %s, authorize_request_id %s, transaction_ref %s, attempt %d: %v — check the payment provider and refund manually if the capture stands (ut-docs#6653)", leg.MethodID, leg.Amount.Minor(), leg.Currency, id, maskTxnRef(leg.TransactionRef), n, sendErr)
		case !dispatched:
			// No plugin heard the refund: that is not a refund.
			outcome, reason = data.LegReverseFailed, data.LegReasonNoRefundPlugin
			logging.L().Warnf("manager retry of a failed reversal reached no payment plugin — method %q, amount %d %s, authorize_request_id %s, transaction_ref %s, attempt %d (ut-docs#6653)", leg.MethodID, leg.Amount.Minor(), leg.Currency, id, maskTxnRef(leg.TransactionRef), n)
		}
		journalReversal(ctx, d, leg, outcome, reason)
		settingsAudit(r.WithContext(ctx), data.NewPOSRepo(d.Db), elev, "payment_leg", id, "payment_leg_retry", map[string]any{
			"prior_state": data.LegReverseFailed, "outcome": outcome, "attempt": n, "method": row.MethodID,
			"amount_minor": row.AmountMinor, "currency": row.Currency, "manager_id": elev.ApproverID,
			"transaction_ref": maskedLegRef(row.TransactionRef), "reason_text": httpx.T("en", "pay.attention.audit_retry"),
		})
		if outcome == data.LegReversed {
			writeLegCard(w, r, d, http.StatusOK, fmt.Sprintf(httpx.T(locale, "pay.attention.retry_done"), legAmount(row, locale)), false, true)
			return
		}
		writeLegCard(w, r, d, http.StatusOK, httpx.T(locale, "pay.attention.retry_failed"), true, true)
	})

	mux.HandleFunc("POST /api/pay/legs/resolve", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		fail := func(status int, key string) {
			writeLegCard(w, r, d, status, httpx.T(locale, key), true, true)
		}
		var note string
		id, elev, ok := prelude(w, r, "resolve", "pay.attention.resolve_summary", func() ([]elevationHiddenField, bool) {
			note = strings.TrimSpace(r.Form.Get("note"))
			return []elevationHiddenField{{Name: "note", Value: note}}, validLegNote(note)
		})
		if !ok {
			return
		}
		if !tapLegInFlight.acquire(id) {
			fail(http.StatusConflict, "pay.attention.in_progress")
			return
		}
		defer tapLegInFlight.release(id)
		repo := data.NewPaymentLegJournalRepo(d.Db)
		cur, found, err := repo.Latest(r.Context(), id)
		switch {
		case err != nil:
			logging.L().Errorf("payment legs resolve: %v", err)
			fail(http.StatusInternalServerError, "common.error.server")
			return
		case !found:
			fail(http.StatusNotFound, "pay.attention.gone")
			return
		case cur.State != data.LegWarned && cur.State != data.LegReverseFailed:
			fail(http.StatusConflict, "pay.attention.not_resolvable")
			return
		}
		ctx := context.WithoutCancel(r.Context())
		// The free-text note goes to the audit only, never to the journal.
		next := cur
		next.State, next.Reason = data.LegResolved, data.LegReasonManagerResolved
		// Zero At so Append stamps now: the old row's time would let the
		// retention sweep delete a just-resolved chain.
		next.At = time.Time{}
		switch err := repo.Append(ctx, next); {
		case errors.Is(err, data.ErrLegJournalTransition):
			fail(http.StatusConflict, "pay.attention.not_resolvable")
			return
		case err != nil:
			logging.L().Errorf("payment legs resolve: %v", err)
			fail(http.StatusInternalServerError, "common.error.server")
			return
		}
		p := map[string]any{
			"prior_state": cur.State, "method": cur.MethodID, "amount_minor": cur.AmountMinor, "currency": cur.Currency,
			"manager_id": elev.ApproverID, "transaction_ref": maskedLegRef(cur.TransactionRef),
			"reason_text": httpx.T("en", "pay.attention.audit_resolve"),
		}
		if note != "" {
			p["note"] = note
		}
		settingsAudit(r.WithContext(ctx), data.NewPOSRepo(d.Db), elev, "payment_leg", id, "payment_leg_resolve", p)
		writeLegCard(w, r, d, http.StatusOK, httpx.T(locale, "pay.attention.resolved"), false, true)
	})
}
