package pages

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#7021: the boot pass over the fiscal sign journal.

func fsjBegin(t *testing.T, repo *data.FiscalSignJournalRepo, id string) {
	t.Helper()
	if err := repo.Begin(context.Background(), data.FiscalSignEntry{
		SaleID: id, SaleType: "sale", AmountMinor: 500, Currency: "EUR", AskedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileFiscalSignJournal_AskedWithoutSaleBecomesWarned(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	logging.ResetRecent()
	repo := data.NewFiscalSignJournalRepo(dp.Db)
	fsjBegin(t, repo, "orphan-1")
	settled, warned, open := ReconcileFiscalSignJournal(context.Background(), dp, time.Now().Add(time.Second))
	if settled != 0 || warned != 1 || open != 1 {
		t.Fatalf("settled/warned/open = %d/%d/%d, want 0/1/1", settled, warned, open)
	}
	e, ok, _ := repo.Get(context.Background(), "orphan-1")
	if !ok || e.State != data.FiscalSignWarned || e.Reason != data.FiscalSignReasonRestart {
		t.Fatalf("row = %+v, want warned/restart", e)
	}
	if !logging.HasOpenProblem(fiscalSignedNoSaleProblemPrefix + "orphan-1") {
		t.Fatal("expected an open Problems item")
	}
}

func TestReconcileFiscalSignJournal_AskedWithSaleIsDeleted(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	repo := data.NewFiscalSignJournalRepo(dp.Db)
	if _, err := dp.Db.Exec(`INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES('sale-ok','R-1','completed','sale','EUR',100,0,20,120,datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	fsjBegin(t, repo, "sale-ok")
	settled, warned, _ := ReconcileFiscalSignJournal(context.Background(), dp, time.Now().Add(time.Second))
	if settled != 1 || warned != 0 {
		t.Fatalf("settled/warned = %d/%d, want 1/0", settled, warned)
	}
	if _, ok, _ := repo.Get(context.Background(), "sale-ok"); ok {
		t.Fatal("row for a committed sale should be deleted")
	}
}

func TestReconcileFiscalSignJournal_OpenRowSurvivesReopenAndIsRaised(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	repo := data.NewFiscalSignJournalRepo(dp.Db)
	fsjBegin(t, repo, "orphan-2")
	if err := repo.MarkWarned(context.Background(), "orphan-2", data.FiscalSignReasonRestart); err != nil {
		t.Fatal(err)
	}
	// The ring is in-memory: a restart empties it, the row stays.
	logging.ResetRecent()
	if logging.HasOpenProblem(fiscalSignedNoSaleProblemPrefix + "orphan-2") {
		t.Fatal("ring should be empty after reset")
	}
	_, _, open := ReconcileFiscalSignJournal(context.Background(), dp, time.Now().Add(time.Second))
	if open != 1 || !logging.HasOpenProblem(fiscalSignedNoSaleProblemPrefix+"orphan-2") {
		t.Fatalf("open=%d, want the item raised again", open)
	}
}

func TestReconcileFiscalSignJournal_LeavesRowsAskedAfterStart(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	repo := data.NewFiscalSignJournalRepo(dp.Db)
	fsjBegin(t, repo, "inflight")
	settled, warned, open := ReconcileFiscalSignJournal(context.Background(), dp, time.Now().Add(-time.Hour))
	if settled != 0 || warned != 0 || open != 0 {
		t.Fatalf("settled/warned/open = %d/%d/%d, want 0/0/0", settled, warned, open)
	}
	if e, ok, _ := repo.Get(context.Background(), "inflight"); !ok || e.State != data.FiscalSignAsked {
		t.Fatalf("in-flight row touched: %+v", e)
	}
}
