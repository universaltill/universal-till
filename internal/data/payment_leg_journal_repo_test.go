package data

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"
)

// ut-docs#6653 (ADR-0136 Decision 9): the append-only payment-leg journal.

func newLegJournalDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	mig, err := os.ReadFile("../db/migrations/115_payment_leg_journal.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(mig)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return db
}

func legEntry(id, state string) LegEntry {
	return LegEntry{RequestID: id, State: state, MethodID: "card", AmountMinor: 1250, Currency: "GBP"}
}

func TestLegJournal_LegalChainAppendsOneRowPerMove(t *testing.T) {
	db := newLegJournalDB(t)
	r := NewPaymentLegJournalRepo(db)
	ctx := context.Background()
	for _, s := range []string{LegAsked, LegCaptured, LegReversing, LegReversed} {
		if err := r.Append(ctx, legEntry("a", s)); err != nil {
			t.Fatalf("append %s: %v", s, err)
		}
	}
	got, ok, err := r.Latest(ctx, "a")
	if err != nil || !ok || got.State != LegReversed || got.AmountMinor != 1250 || got.Currency != "GBP" || got.Ask != 1 {
		t.Fatalf("Latest = %+v %v %v, want reversed 1250 GBP ask 1", got, ok, err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM payment_leg_journal WHERE request_id='a'`).Scan(&n)
	if n != 4 {
		t.Fatalf("rows = %d, want 4 (one per move)", n)
	}
}

func TestLegJournal_IllegalMovesAreRefused(t *testing.T) {
	r := NewPaymentLegJournalRepo(newLegJournalDB(t))
	ctx := context.Background()
	if err := r.Append(ctx, legEntry("b", LegCaptured)); !errors.Is(err, ErrLegJournalTransition) {
		t.Fatalf("captured with no asked: %v, want ErrLegJournalTransition", err)
	}
	_ = r.Append(ctx, legEntry("b", LegAsked))
	_ = r.Append(ctx, legEntry("b", LegCaptured))
	for _, s := range []string{LegDeclined, LegUnknown, LegWarned, LegReversed, LegResolved, LegAsked} {
		if err := r.Append(ctx, legEntry("b", s)); !errors.Is(err, ErrLegJournalTransition) {
			t.Fatalf("captured -> %s: %v, want ErrLegJournalTransition", s, err)
		}
	}
	if err := r.Append(ctx, LegEntry{RequestID: "b", State: "bogus", MethodID: "card"}); !errors.Is(err, ErrLegJournalInvalid) {
		t.Fatalf("unknown state: %v", err)
	}
}

func TestLegJournal_RepeatedStateIsNoOp(t *testing.T) {
	db := newLegJournalDB(t)
	r := NewPaymentLegJournalRepo(db)
	ctx := context.Background()
	_ = r.Append(ctx, legEntry("c", LegAsked))
	_ = r.Append(ctx, legEntry("c", LegCaptured))
	if err := r.Append(ctx, legEntry("c", LegCaptured)); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM payment_leg_journal`).Scan(&n)
	if n != 2 {
		t.Fatalf("rows = %d, want 2 (a repeated captured is not a new row)", n)
	}
}

func TestLegJournal_UpdateAbortsAppendOnly(t *testing.T) {
	db := newLegJournalDB(t)
	r := NewPaymentLegJournalRepo(db)
	_ = r.Append(context.Background(), legEntry("d", LegAsked))
	if _, err := db.Exec(`UPDATE payment_leg_journal SET state='settled'`); err == nil {
		t.Fatal("UPDATE succeeded, want the append-only trigger to abort it")
	}
}

func TestLegJournal_AskLegCountsReasksAndKeepsACapturedLeg(t *testing.T) {
	r := NewPaymentLegJournalRepo(newLegJournalDB(t))
	ctx := context.Background()
	began, err := r.AskLeg(ctx, legEntry("e", ""))
	if err != nil || !began {
		t.Fatalf("first ask = %v %v, want began", began, err)
	}
	// an already-asked leg (a failed later write) is not asked a second time in the journal
	if began, err := r.AskLeg(ctx, legEntry("e", "")); err != nil || !began {
		t.Fatalf("ask of asked = %v %v", began, err)
	}
	if got, _, _ := r.Latest(ctx, "e"); got.Ask != 1 {
		t.Fatalf("ask = %d, want 1 (no new row for an asked leg)", got.Ask)
	}
	_ = r.Append(ctx, legEntry("e", LegCaptured))
	began, err = r.AskLeg(ctx, legEntry("e", ""))
	if err != nil || began {
		t.Fatalf("re-ask of captured = %v %v, want not began", began, err)
	}
	if got, _, _ := r.Latest(ctx, "e"); got.State != LegCaptured {
		t.Fatalf("state = %s, want captured kept", got.State)
	}
	// unknown, then declined: each re-ask under the same id bumps ask
	_ = r.Append(ctx, legEntry("f", LegAsked))
	_ = r.Append(ctx, legEntry("f", LegUnknown))
	if began, err := r.AskLeg(ctx, legEntry("f", "")); err != nil || !began {
		t.Fatalf("re-ask of unknown = %v %v", began, err)
	}
	_ = r.Append(ctx, legEntry("f", LegDeclined))
	if _, err := r.AskLeg(ctx, legEntry("f", "")); err != nil {
		t.Fatalf("re-ask of declined: %v", err)
	}
	if got, _, _ := r.Latest(ctx, "f"); got.State != LegAsked || got.Ask != 3 {
		t.Fatalf("Latest = %+v, want asked ask=3", got)
	}
	// a leg that already ended is never asked again under the same id
	_ = r.Append(ctx, legEntry("g", LegAsked))
	_ = r.Append(ctx, legEntry("g", LegCaptured))
	_ = r.Append(ctx, legEntry("g", LegReversing))
	_ = r.Append(ctx, legEntry("g", LegReversed))
	if _, err := r.AskLeg(ctx, legEntry("g", "")); !errors.Is(err, ErrLegJournalTransition) {
		t.Fatalf("ask of reversed = %v, want ErrLegJournalTransition", err)
	}
}

func TestLegJournal_CapturedRowKeepsFullRefAmountWithTipAndReader(t *testing.T) {
	r := NewPaymentLegJournalRepo(newLegJournalDB(t))
	ctx := context.Background()
	_ = r.Append(ctx, legEntry("h", LegAsked))
	c := legEntry("h", LegCaptured)
	c.AmountMinor, c.TipMinor, c.ReaderRef, c.TransactionRef = 1350, 100, "rdr_1", "ch_FULLREF1234"
	if err := r.Append(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, _, _ := r.Latest(ctx, "h")
	if got.AmountMinor != 1350 || got.TipMinor != 100 || got.ReaderRef != "rdr_1" || got.TransactionRef != "ch_FULLREF1234" {
		t.Fatalf("captured row = %+v, want 1350 incl tip 100, reader rdr_1, full ref", got)
	}
}

func TestLegJournal_SettleLegsTxWritesSaleIDAndIsIdempotent(t *testing.T) {
	db := newLegJournalDB(t)
	r := NewPaymentLegJournalRepo(db)
	ctx := context.Background()
	_ = r.Append(ctx, legEntry("i", LegAsked))
	_ = r.Append(ctx, legEntry("i", LegCaptured))
	for range 2 {
		tx, _ := db.BeginTx(ctx, nil)
		if err := r.SettleLegsTx(ctx, tx, "sale-1", []string{"i", "", "never-journaled"}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	got, _, _ := r.Latest(ctx, "i")
	if got.State != LegSettled || got.SaleID != "sale-1" || got.AmountMinor != 1250 {
		t.Fatalf("Latest = %+v, want settled sale-1 1250", got)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM payment_leg_journal WHERE state='settled'`).Scan(&n)
	if n != 1 {
		t.Fatalf("settled rows = %d, want 1", n)
	}
	if _, ok, _ := r.Latest(ctx, "never-journaled"); ok {
		t.Fatal("a leg with no row must not get one")
	}
}

func TestLegJournal_ListBeforeIsNewestStateOnlyAndBeforeCutoff(t *testing.T) {
	r := NewPaymentLegJournalRepo(newLegJournalDB(t))
	ctx := context.Background()
	old := time.Now().Add(-time.Hour)
	for _, e := range []LegEntry{legEntry("j", LegAsked), legEntry("j", LegCaptured), legEntry("k", LegAsked)} {
		e.At = old
		if err := r.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	fresh := legEntry("l", LegAsked)
	fresh.At = time.Now().Add(time.Hour)
	_ = r.Append(ctx, fresh)
	got, err := r.ListBefore(ctx, time.Now(), []string{LegAsked, LegCaptured}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].RequestID != "j" || got[0].State != LegCaptured || got[1].RequestID != "k" {
		t.Fatalf("ListBefore = %+v, want j(captured) then k(asked), not l (after cutoff)", got)
	}
	if got, _ := r.ListBefore(ctx, time.Now(), []string{LegAsked, LegCaptured}, 1); len(got) != 1 {
		t.Fatalf("limit 1 returned %d rows", len(got))
	}
}

func TestLegJournal_RetentionSweepsOnlyOldTerminalChains(t *testing.T) {
	db := newLegJournalDB(t)
	r := NewPaymentLegJournalRepo(db)
	ctx := context.Background()
	old := time.Now().Add(-100 * 24 * time.Hour)
	chains := map[string][]string{
		"declined":  {LegAsked, LegDeclined},
		"captured":  {LegAsked, LegCaptured},
		"warned":    {LegAsked, LegUnknown, LegWarned},
		"revfailed": {LegAsked, LegCaptured, LegReversing, LegReverseFailed},
		"resolved":  {LegAsked, LegCaptured, LegReversing, LegReverseFailed, LegResolved},
	}
	for id, states := range chains {
		for _, s := range states {
			e := legEntry(id, s)
			e.At = old
			if err := r.Append(ctx, e); err != nil {
				t.Fatalf("%s %s: %v", id, s, err)
			}
		}
	}
	if err := r.Append(ctx, legEntry("fresh", LegAsked)); err != nil {
		t.Fatal(err)
	}
	for id, wantKept := range map[string]bool{"declined": false, "resolved": false, "captured": true, "warned": true, "revfailed": true, "fresh": true} {
		if _, ok, _ := r.Latest(ctx, id); ok != wantKept {
			t.Fatalf("chain %s kept = %v, want %v", id, ok, wantKept)
		}
	}
}

func TestLegJournal_ManagerResolvesWarnedAndReverseFailedOnly(t *testing.T) {
	r := NewPaymentLegJournalRepo(newLegJournalDB(t))
	ctx := context.Background()
	seed := func(id string, states ...string) {
		for _, s := range states {
			if err := r.Append(ctx, legEntry(id, s)); err != nil {
				t.Fatalf("%s %s: %v", id, s, err)
			}
		}
	}
	seed("n", LegAsked, LegCaptured, LegReversing, LegReverseFailed)
	seed("o", LegAsked, LegUnknown, LegWarned)
	seed("p", LegAsked, LegCaptured, LegSettled)
	for _, id := range []string{"n", "o"} {
		if err := r.Append(ctx, legEntry(id, LegResolved)); err != nil {
			t.Fatalf("%s -> resolved: %v", id, err)
		}
	}
	if err := r.Append(ctx, legEntry("p", LegResolved)); !errors.Is(err, ErrLegJournalTransition) {
		t.Fatalf("settled -> resolved: %v, want ErrLegJournalTransition", err)
	}
}

// ut-docs#6653 slice 3: the manager's attention list, Retry and Resolve.

func seedLegChain(t *testing.T, r *PaymentLegJournalRepo, id string, states ...string) {
	t.Helper()
	for _, s := range states {
		e := legEntry(id, s)
		e.TipMinor, e.ReaderRef, e.TransactionRef = 50, "rdr_1", "pi_FULLREF9876"
		if err := r.Append(context.Background(), e); err != nil {
			t.Fatalf("%s %s: %v", id, s, err)
		}
	}
}

func TestLegJournal_ListCurrent_OnlyNewestReverseFailedAndWarned(t *testing.T) {
	r := NewPaymentLegJournalRepo(newLegJournalDB(t))
	ctx := context.Background()
	seedLegChain(t, r, "failed1", LegAsked, LegCaptured, LegReversing, LegReverseFailed)
	seedLegChain(t, r, "warned1", LegAsked, LegUnknown, LegWarned)
	seedLegChain(t, r, "reversed", LegAsked, LegCaptured, LegReversing, LegReversed)
	seedLegChain(t, r, "resolved", LegAsked, LegCaptured, LegReversing, LegReverseFailed, LegResolved)
	seedLegChain(t, r, "settled", LegAsked, LegCaptured, LegSettled)
	seedLegChain(t, r, "captured", LegAsked, LegCaptured)
	// failed once, then retried: its newest row is reversing, so not listed
	seedLegChain(t, r, "retrying", LegAsked, LegCaptured, LegReversing, LegReverseFailed, LegReversing)
	seedLegChain(t, r, "failed2", LegAsked, LegCaptured, LegReversing, LegReverseFailed)

	got, err := r.ListCurrent(ctx, []string{LegReverseFailed, LegWarned}, 10)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(got))
	for i, e := range got {
		ids[i] = e.RequestID
	}
	if want := []string{"failed1", "warned1", "failed2"}; len(ids) != 3 || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Fatalf("ListCurrent = %v, want %v (oldest first, newest state only)", ids, want)
	}
	if got, _ := r.ListCurrent(ctx, []string{LegReverseFailed, LegWarned}, 2); len(got) != 2 {
		t.Fatalf("limit 2 returned %d rows", len(got))
	}
	if got, err := r.ListCurrent(ctx, nil, 10); err != nil || len(got) != 0 {
		t.Fatalf("no states = %v %v, want empty", got, err)
	}
}

func TestLegJournal_BeginRetry_CounterFromReversingRows(t *testing.T) {
	db := newLegJournalDB(t)
	r := NewPaymentLegJournalRepo(db)
	ctx := context.Background()
	seedLegChain(t, r, "a", LegAsked, LegCaptured, LegReversing, LegReverseFailed)

	cur, n, err := r.BeginRetry(ctx, "a")
	if err != nil || n != 2 {
		t.Fatalf("first retry = n %d err %v, want 2 (never :1, that is the live send)", n, err)
	}
	if cur.State != LegReverseFailed || cur.TransactionRef != "pi_FULLREF9876" {
		t.Fatalf("returned entry = %+v, want the reverse_failed row it retries", cur)
	}
	got, _, _ := r.Latest(ctx, "a")
	if got.State != LegReversing || got.Reason != "" || got.AmountMinor != 1250 || got.TipMinor != 50 || got.ReaderRef != "rdr_1" || got.TransactionRef != "pi_FULLREF9876" || got.Currency != "GBP" || got.MethodID != "card" {
		t.Fatalf("inserted row = %+v, want a reversing copy with ref, amount, tip and reader", got)
	}
	if err := r.Append(ctx, legEntry("a", LegReverseFailed)); err != nil {
		t.Fatal(err)
	}
	if _, n, err := r.BeginRetry(ctx, "a"); err != nil || n != 3 {
		t.Fatalf("second retry = n %d err %v, want 3", n, err)
	}
}

func TestLegJournal_BeginRetry_RefusesUnlessReverseFailed(t *testing.T) {
	db := newLegJournalDB(t)
	r := NewPaymentLegJournalRepo(db)
	ctx := context.Background()
	chains := map[string][]string{
		"warned":    {LegAsked, LegUnknown, LegWarned},
		"captured":  {LegAsked, LegCaptured},
		"reversing": {LegAsked, LegCaptured, LegReversing},
		"reversed":  {LegAsked, LegCaptured, LegReversing, LegReversed},
		"resolved":  {LegAsked, LegCaptured, LegReversing, LegReverseFailed, LegResolved},
	}
	for id, states := range chains {
		seedLegChain(t, r, id, states...)
	}
	var before int
	_ = db.QueryRow(`SELECT COUNT(*) FROM payment_leg_journal`).Scan(&before)
	for id := range chains {
		if _, _, err := r.BeginRetry(ctx, id); !errors.Is(err, ErrLegJournalTransition) {
			t.Fatalf("BeginRetry(%s) = %v, want ErrLegJournalTransition", id, err)
		}
	}
	var after int
	_ = db.QueryRow(`SELECT COUNT(*) FROM payment_leg_journal`).Scan(&after)
	if after != before {
		t.Fatalf("rows %d -> %d: a refused retry must add none", before, after)
	}
	if _, _, err := r.BeginRetry(ctx, "nope"); !errors.Is(err, ErrLegJournalNotFound) {
		t.Fatalf("unknown id = %v, want ErrLegJournalNotFound", err)
	}
}

func TestLegJournal_ResolveOnlyFromWarnedOrReverseFailed(t *testing.T) {
	db := newLegJournalDB(t)
	r := NewPaymentLegJournalRepo(db)
	ctx := context.Background()
	seedLegChain(t, r, "w", LegAsked, LegUnknown, LegWarned)
	e := legEntry("w", LegResolved)
	e.Reason = LegReasonManagerResolved
	if err := r.Append(ctx, e); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := r.Latest(ctx, "w"); got.State != LegResolved || got.Reason != LegReasonManagerResolved {
		t.Fatalf("Latest = %+v, want resolved/manager_resolved", got)
	}
	if _, err := db.Exec(`UPDATE payment_leg_journal SET reason='x' WHERE request_id='w'`); err == nil {
		t.Fatal("UPDATE succeeded, want the append-only trigger to abort it")
	}
}

// ut-docs#7028 (ADR-0136 Decision 9.2 amendment): captured -> warned exists only
// for a charged tap with no sale.
func TestLegJournal_CapturedToWarnedOnlyWithTapReason_7028(t *testing.T) {
	r := NewPaymentLegJournalRepo(newLegJournalDB(t))
	ctx := context.Background()
	for _, id := range []string{"w1", "w2", "w3"} {
		_ = r.Append(ctx, legEntry(id, LegAsked))
		_ = r.Append(ctx, legEntry(id, LegCaptured))
	}
	for id, reason := range map[string]string{"w1": LegReasonRestart, "w2": ""} {
		e := legEntry(id, LegWarned)
		e.Reason = reason
		if err := r.Append(ctx, e); !errors.Is(err, ErrLegJournalTransition) {
			t.Fatalf("captured -> warned/%q: %v, want ErrLegJournalTransition", reason, err)
		}
	}
	if err := r.Append(ctx, legEntry("w3", LegResolved)); !errors.Is(err, ErrLegJournalTransition) {
		t.Fatalf("captured -> resolved: %v, want refused", err)
	}
	e := legEntry("w3", LegWarned)
	e.Reason = LegReasonTapChargedNoSale
	if err := r.Append(ctx, e); err != nil {
		t.Fatalf("captured -> warned/tap_charged_no_sale: %v", err)
	}
	if err := r.Append(ctx, legEntry("w3", LegResolved)); err != nil {
		t.Fatalf("warned -> resolved: %v", err)
	}
}

// The tap reason is for captured -> warned only (ut-docs#7028).
func TestLegJournal_TapReasonRefusedOffCaptured_7028(t *testing.T) {
	r := NewPaymentLegJournalRepo(newLegJournalDB(t))
	ctx := context.Background()
	_ = r.Append(ctx, legEntry("u1", LegAsked))
	e := legEntry("u1", LegWarned)
	e.Reason = LegReasonTapChargedNoSale
	if err := r.Append(ctx, e); !errors.Is(err, ErrLegJournalTransition) {
		t.Fatalf("asked -> warned/tap_charged_no_sale: %v, want ErrLegJournalTransition", err)
	}
}

// --- ut-docs#7027: AppendChain and the attempt_settled reason ---

func legRows(t *testing.T, db *sql.DB, id string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM payment_leg_journal WHERE request_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAppendChain_AllOrNothing_7027(t *testing.T) {
	ctx := context.Background()
	db := newLegJournalDB(t)
	r := NewPaymentLegJournalRepo(db)
	seedLegChain(t, r, "a", LegAsked, LegUnknown)
	from, _, _ := r.Latest(ctx, "a")
	warned, resolved := from, from
	warned.State, warned.Reason = LegWarned, LegReasonRestart
	resolved.State, resolved.Reason = LegResolved, LegReasonAttemptSettled
	// (a) happy path: two rows
	if err := r.AppendChain(ctx, from, warned, resolved); err != nil {
		t.Fatal(err)
	}
	if n := legRows(t, db, "a"); n != 4 {
		t.Fatalf("rows = %d, want 4", n)
	}
	if cur, _, _ := r.Latest(ctx, "a"); cur.State != LegResolved || cur.Reason != LegReasonAttemptSettled {
		t.Fatalf("latest = %+v", cur)
	}
	// (b) invalid second step: nothing written
	seedLegChain(t, r, "b", LegAsked, LegUnknown)
	from, _, _ = r.Latest(ctx, "b")
	warned, bad := from, from
	warned.State, warned.Reason = LegWarned, LegReasonRestart
	bad.State = LegSettled
	if err := r.AppendChain(ctx, from, warned, bad); !errors.Is(err, ErrLegJournalTransition) {
		t.Fatalf("err = %v, want transition", err)
	}
	if n := legRows(t, db, "b"); n != 2 {
		t.Fatalf("rows = %d, want 2 (rolled back)", n)
	}
	// (c) stale from
	seedLegChain(t, r, "c", LegAsked, LegUnknown)
	from, _, _ = r.Latest(ctx, "c")
	seedLegChain(t, r, "c", LegWarned)
	warned, resolved = from, from
	warned.State, warned.Reason = LegWarned, LegReasonRestart
	resolved.State, resolved.Reason = LegResolved, LegReasonAttemptSettled
	if err := r.AppendChain(ctx, from, warned, resolved); !errors.Is(err, ErrLegJournalTransition) {
		t.Fatalf("stale err = %v, want transition", err)
	}
	if n := legRows(t, db, "c"); n != 3 {
		t.Fatalf("rows = %d, want 3", n)
	}
	// (d) invalid input
	asked := from
	asked.State = LegAsked
	if err := r.AppendChain(ctx, from, asked); !errors.Is(err, ErrLegJournalInvalid) {
		t.Fatalf("asked err = %v", err)
	}
	if err := r.AppendChain(ctx, from); !errors.Is(err, ErrLegJournalInvalid) {
		t.Fatalf("empty err = %v", err)
	}
}

func TestAppend_AttemptSettledReasonOnlyOnWarnedToResolved_7027(t *testing.T) {
	ctx := context.Background()
	r := NewPaymentLegJournalRepo(newLegJournalDB(t))
	seedLegChain(t, r, "a", LegAsked, LegUnknown)
	e := legEntry("a", LegWarned)
	e.Reason = LegReasonAttemptSettled
	if err := r.Append(ctx, e); !errors.Is(err, ErrLegJournalTransition) {
		t.Fatalf("unknown -> warned with attempt_settled: %v, want transition", err)
	}
	seedLegChain(t, r, "a", LegWarned)
	e.State = LegResolved
	if err := r.Append(ctx, e); err != nil {
		t.Fatalf("warned -> resolved with attempt_settled: %v", err)
	}
}

// ut-docs#7041 (ADR-0136 Decision 9.8): refund_no_return rides captured ->
// warned only.
func TestLegJournal_RefundNoReturnOnlyCapturedToWarned(t *testing.T) {
	r := NewPaymentLegJournalRepo(newLegJournalDB(t))
	ctx := context.Background()
	_ = r.Append(ctx, legEntry("r1", LegAsked))
	_ = r.Append(ctx, legEntry("r1", LegCaptured))
	e := legEntry("r1", LegWarned)
	e.Reason = LegReasonRefundNoReturn
	if err := r.Append(ctx, e); err != nil {
		t.Fatalf("captured -> warned/refund_no_return: %v", err)
	}
	// the reason is refused on any other move
	_ = r.Append(ctx, legEntry("r2", LegAsked))
	_ = r.Append(ctx, legEntry("r2", LegUnknown))
	e2 := legEntry("r2", LegWarned)
	e2.Reason = LegReasonRefundNoReturn
	e4 := legEntry("r1", LegResolved)
	e4.Reason = LegReasonRefundNoReturn
	if err := r.Append(ctx, e4); !errors.Is(err, ErrLegJournalTransition) {
		t.Fatalf("warned -> resolved/refund_no_return: %v, want ErrLegJournalTransition", err)
	}
	if err := r.Append(ctx, e2); !errors.Is(err, ErrLegJournalTransition) {
		t.Fatalf("unknown -> warned/refund_no_return: %v, want ErrLegJournalTransition", err)
	}
	_ = r.Append(ctx, legEntry("r3", LegAsked))
	_ = r.Append(ctx, legEntry("r3", LegCaptured))
	if err := r.Append(ctx, legEntry("r3", LegWarned)); !errors.Is(err, ErrLegJournalTransition) {
		t.Fatalf("captured -> warned with no reason: %v, want refused", err)
	}
}

func TestRefundLegKey_RoundTrip(t *testing.T) {
	k := RefundLegKey("s1", "demopay", "a:b")
	if k != "refund:s1:demopay:a:b" {
		t.Fatalf("key = %q", k)
	}
	if id, ok := RefundLegOriginalSale(k); !ok || id != "s1" {
		t.Fatalf("original sale = %q,%v, want s1,true", id, ok)
	}
	for _, bad := range []string{"abc", "refund:", "authorize-1"} {
		if _, ok := RefundLegOriginalSale(bad); ok {
			t.Fatalf("%q reported as a refund key", bad)
		}
	}
}
