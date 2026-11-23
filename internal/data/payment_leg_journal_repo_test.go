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
