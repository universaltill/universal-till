package data

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"
)

// ut-docs#7021 (ADR-0044 D1 amendment): the fiscal sign journal.

func newFiscalSignJournalDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	mig, err := os.ReadFile("../db/migrations/117_fiscal_sign_journal.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(mig)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return db
}

func fsjEntry(id string, at time.Time) FiscalSignEntry {
	return FiscalSignEntry{SaleID: id, SaleType: "sale", AmountMinor: 1250, Currency: "EUR", ActorID: "u1", AskedAt: at}
}

func TestFiscalSignJournal_BeginTwiceRefused(t *testing.T) {
	r := NewFiscalSignJournalRepo(newFiscalSignJournalDB(t))
	ctx := context.Background()
	if err := r.Begin(ctx, fsjEntry("s1", time.Now())); err != nil {
		t.Fatalf("first Begin: %v", err)
	}
	if err := r.Begin(ctx, fsjEntry("s1", time.Now())); !errors.Is(err, ErrFiscalSignJournalExists) {
		t.Fatalf("second Begin = %v, want ErrFiscalSignJournalExists", err)
	}
}

func TestFiscalSignJournal_DeleteKeepsOpenRows(t *testing.T) {
	r := NewFiscalSignJournalRepo(newFiscalSignJournalDB(t))
	ctx := context.Background()
	if err := r.Begin(ctx, fsjEntry("s1", time.Now())); err != nil {
		t.Fatal(err)
	}
	e := fsjEntry("s1", time.Now())
	e.Outcome, e.EvidenceJSON, e.Reason = "approved", `{"signature":"sig"}`, "pos.toast.tender_failed"
	if err := r.MarkSignedNoSale(ctx, e); err != nil {
		t.Fatalf("MarkSignedNoSale: %v", err)
	}
	if err := r.Delete(ctx, "s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, ok, err := r.Get(ctx, "s1")
	if err != nil || !ok || got.State != FiscalSignSignedNoSale || got.EvidenceJSON != `{"signature":"sig"}` || got.Outcome != "approved" {
		t.Fatalf("Get after Delete = %+v %v %v, want the signed_no_sale row kept", got, ok, err)
	}
	// An asked row is deletable.
	if err := r.Begin(ctx, fsjEntry("s2", time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, "s2"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := r.Get(ctx, "s2"); ok {
		t.Fatal("asked row should be deleted")
	}
}

func TestFiscalSignJournal_ListAskedBeforeCutoff(t *testing.T) {
	r := NewFiscalSignJournalRepo(newFiscalSignJournalDB(t))
	ctx := context.Background()
	now := time.Now()
	if err := r.Begin(ctx, fsjEntry("old", now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := r.Begin(ctx, fsjEntry("new", now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	got, err := r.ListAskedBefore(ctx, now, 10)
	if err != nil || len(got) != 1 || got[0].SaleID != "old" {
		t.Fatalf("ListAskedBefore = %+v %v, want only old", got, err)
	}
	if err := r.MarkWarned(ctx, "old", FiscalSignReasonRestart); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.ListAskedBefore(ctx, now, 10); len(got) != 0 {
		t.Fatalf("warned row must leave the asked list, got %+v", got)
	}
	open, err := r.ListOpen(ctx, 10)
	if err != nil || len(open) != 1 || open[0].SaleID != "old" || open[0].State != FiscalSignWarned || open[0].Reason != FiscalSignReasonRestart {
		t.Fatalf("ListOpen = %+v %v, want the warned row", open, err)
	}
}
