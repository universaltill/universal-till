package pages

import (
	"context"
	"errors"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#6653 (ADR-0136 Decision 9): the pages side of the payment-leg
// journal. The write-before-ask insert is fail closed; every later write is
// best effort, because by then the in-memory path is the live one and money
// may already have moved: a journal failure never changes a tender's outcome
// after the provider was asked.

// paymentJournalError: the till could not record the payment leg, so the leg
// was never asked and no money moved. Carries no plugin text; each tender
// surface maps it to its own localized copy and keeps the basket.
type paymentJournalError struct{ Method string }

func (e *paymentJournalError) Error() string {
	return "payment leg journal unavailable: " + e.Method
}

type legJournalHookKey struct{}

// legJournalHook runs inside the authorize dispatch, once a subscriber exists
// and before the plugin is called.
type legJournalHook func(ctx context.Context) error

// withLegJournal makes the authorize dispatch call hook first. Cash and
// hookless legs have no subscriber, so they never reach it.
func withLegJournal(ctx context.Context, hook legJournalHook) context.Context {
	return context.WithValue(ctx, legJournalHookKey{}, hook)
}

// askLegJournal runs the hook carried by ctx for an authorize dispatch. A
// failure is returned as a *paymentJournalError.
func askLegJournal(ctx context.Context, method string) error {
	hook, ok := ctx.Value(legJournalHookKey{}).(legJournalHook)
	if !ok || hook == nil {
		return nil
	}
	if err := hook(ctx); err != nil {
		logging.L().Errorf("payment leg journal: leg for method %q was NOT asked: %v (ut-docs#6653)", method, err)
		return &paymentJournalError{Method: method}
	}
	return nil
}

// legJournalTracker journals the legs of one tender attempt.
type legJournalTracker struct {
	repo  *data.PaymentLegJournalRepo
	began map[string]bool
}

func newLegJournalTracker(d *common.Deps) *legJournalTracker {
	return &legJournalTracker{repo: data.NewPaymentLegJournalRepo(d.Db), began: map[string]bool{}}
}

func legBase(requestID, method string, amount money.Money, currency, readerRef string) data.LegEntry {
	return data.LegEntry{RequestID: requestID, MethodID: method, AmountMinor: amount.Minor(), Currency: currency, ReaderRef: readerRef}
}

// hook is the fail-closed write-before-ask for one leg.
func (t *legJournalTracker) hook(e data.LegEntry) legJournalHook {
	return func(ctx context.Context) error {
		began, err := t.repo.AskLeg(ctx, e)
		if err != nil {
			return err
		}
		if began {
			t.began[e.RequestID] = true
		}
		return nil
	}
}

// note records the answer for a leg that was asked this attempt. Best effort.
func (t *legJournalTracker) note(ctx context.Context, e data.LegEntry, state string) {
	if !t.began[e.RequestID] {
		return
	}
	e.State = state
	if err := t.repo.Append(ctx, e); err != nil {
		logging.L().Warnf("payment leg journal: could not record %s for authorize_request_id %s: %v (ut-docs#6653)", state, e.RequestID, err)
	}
}

// captured records an approved leg. A leg answered without having been asked
// this attempt (a tap core confirmed after the till locked) is asked first.
// Best effort: the money has moved, the sale must go on.
func (t *legJournalTracker) captured(ctx context.Context, e data.LegEntry) {
	if !t.began[e.RequestID] {
		if err := t.hook(e)(ctx); err != nil {
			logging.L().Warnf("payment leg journal: could not record asked before captured for authorize_request_id %s: %v (ut-docs#6653)", e.RequestID, err)
			return
		}
	}
	t.note(ctx, e, data.LegCaptured)
}

// journalReversal appends one reversal state for a captured leg. Best effort:
// the send goes ahead whatever the journal says.
func journalReversal(ctx context.Context, d *common.Deps, leg pos.CapturedLeg, state string) {
	if d == nil || d.Db == nil {
		return
	}
	err := data.NewPaymentLegJournalRepo(d.Db).Append(ctx, data.LegEntry{
		RequestID: leg.RequestID, State: state, MethodID: leg.MethodID, AmountMinor: leg.Amount.Minor(), Currency: leg.Currency,
		TipMinor: leg.TipAmount.Minor(), ReaderRef: leg.ReaderRef, TransactionRef: leg.TransactionRef,
	})
	if err != nil && !errors.Is(err, data.ErrLegJournalTransition) {
		logging.L().Warnf("payment leg journal: could not record %s for authorize_request_id %s: %v (ut-docs#6653)", state, leg.RequestID, err)
	}
}
