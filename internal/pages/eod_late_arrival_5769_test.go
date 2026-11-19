package pages

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5769 (ADR-0066 amendment), end to end through the real
// generateEOD + applyJournal: a row made on another till before close N and
// applied after it counts in Z N+1, never in Z N, and Z N is never touched.
// Origins are derived from time.Now() (guard-fixed-dates.sh).

// lateJournalSale is a 2-VAT-rate sale (20% + 5%) paid by card + cash, made
// on the replica at origin.
func lateJournalSale(id, receipt string, origin time.Time) journalSale {
	return journalSale{Sale: data.SaleDetail{
		ID: id, ReceiptNo: receipt, Status: "completed", SaleType: "sale",
		Currency: "GBP", Subtotal: 300, TaxTotal: 27, Total: 300,
		CreatedAt: origin.UTC().Format(time.RFC3339), CashierID: "user1",
		Lines: []data.SaleDetailLine{
			{Name: "Apple", SKU: "ABC", ItemID: "itm1", UnitPrice: 100, Qty: 1, LineTotal: 100, TaxRateBP: 2000, TaxAmount: 17},
			{Name: "Apple", SKU: "ABC", ItemID: "itm1", UnitPrice: 200, Qty: 1, LineTotal: 200, TaxRateBP: 500, TaxAmount: 10},
		},
		Payments: []data.SaleDetailPayment{{Method: "card", Amount: 100}, {Method: "cash", Amount: 200}},
	}}
}

func archivedZ(t *testing.T, dp *common.Deps) string {
	t.Helper()
	var s string
	if err := dp.Db.QueryRowContext(t.Context(),
		`SELECT z_number || '|' || period || '|' || created_at || '|' || content_json FROM report_archive WHERE kind='eod' ORDER BY z_number LIMIT 1`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEOD_LateJournaledRowsCountInNextZ(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	if _, err := dp.Db.Exec(`INSERT INTO tills (id, name, bearer_hash) VALUES ('till-2', 'Replica', 'h2')`); err != nil {
		t.Fatal(err)
	}

	z1, _, err := generateEOD(ctx, dp, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	before := archivedZ(t, dp)
	origin := time.Now().Add(-10 * time.Minute) // before close N

	if applied, _, err := applyJournal(ctx, dp, "till-2", lateJournalSale("late-1", "T2-L1", origin)); err != nil || !applied {
		t.Fatalf("apply sale: applied=%v err=%v", applied, err)
	}
	if _, err := repo.ApplyJournaledNoSaleEvent(ctx, data.NoSaleEvent{ID: "late-ns", CreatedAt: origin.UTC().Format(time.RFC3339), TillID: "till-2", ActorID: "user1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApplyJournaledClearedBasket(ctx, data.ClearedBasket{ID: "late-cb", CreatedAt: origin.UTC().Format(time.RFC3339), TillID: "till-2", LineCount: 1}); err != nil {
		t.Fatal(err)
	}
	if z1.SalesCount != 0 {
		t.Fatalf("Z N had %d sales", z1.SalesCount)
	}
	if got := archivedZ(t, dp); got != before {
		t.Fatalf("closed Z N changed:\n before %s\n after  %s", before, got)
	}

	time.Sleep(1100 * time.Millisecond)
	z2, _, err := generateEOD(ctx, dp, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if z2.SalesCount != 1 || z2.Gross != 300 || z2.NoSaleCount != 1 || z2.ClearedBasketCount != 1 {
		t.Fatalf("Z N+1 sales=%d gross=%d nosale=%d cleared=%d, want 1/300/1/1", z2.SalesCount, z2.Gross, z2.NoSaleCount, z2.ClearedBasketCount)
	}
	// per-Z identities hold with the late row.
	var tax, net int64
	for _, b := range z2.TaxBands {
		tax += b.Tax
		net += b.Net
	}
	if len(z2.TaxBands) != 2 || tax != z2.TaxNet {
		t.Fatalf("sum(band.Tax)=%d TaxNet=%d bands=%+v", tax, z2.TaxNet, z2.TaxBands)
	}
	var in int64
	for _, m := range z2.Methods {
		in += m.In
	}
	if len(z2.Methods) != 2 || in != z2.Gross {
		t.Fatalf("methods %+v must sum to Gross %d", z2.Methods, z2.Gross)
	}
	// the note is on the report (and so in the archived content_json).
	raw, _ := json.Marshal(z2)
	var back struct {
		Late *data.EODLateEntries `json:"late_entries"`
	}
	if err := json.Unmarshal(raw, &back); err != nil || back.Late == nil || len(back.Late.Tills) != 1 {
		t.Fatalf("late_entries missing from the stored report: %s", raw)
	}
	l := back.Late.Tills[0]
	if l.TillID != "till-2" || l.Name != "Replica" || l.SalesCount != 1 || l.Gross != 300 || l.NoSaleCount != 1 || l.ClearedBasketCount != 1 {
		t.Fatalf("late till = %+v", l)
	}
	if want := origin.UTC().Truncate(time.Second).Local().Format(time.RFC3339); back.Late.EarliestOrigin != want {
		t.Fatalf("earliest origin = %q, want %q", back.Late.EarliestOrigin, want)
	}
}

// First-ever close: no late rows even with old origins; a Z whose journaled
// rows are on time has no note (AC3/AC4).
func TestEOD_NoNoteWhenNothingLate(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.Exec(`INSERT INTO tills (id, name, bearer_hash) VALUES ('till-2', 'Replica', 'h2')`); err != nil {
		t.Fatal(err)
	}
	if applied, _, err := applyJournal(ctx, dp, "till-2", lateJournalSale("old-1", "T2-O1", time.Now().Add(-72*time.Hour))); err != nil || !applied {
		t.Fatal(applied, err)
	}
	time.Sleep(1100 * time.Millisecond) // whole-second window compare
	z1, _, err := generateEOD(ctx, dp, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if z1.SalesCount != 1 || z1.LateEntries != nil {
		t.Fatalf("first-ever close: sales=%d late=%+v, want 1 and nil", z1.SalesCount, z1.LateEntries)
	}
	// on time: made and applied inside Z N+1.
	time.Sleep(1100 * time.Millisecond)
	if applied, _, err := applyJournal(ctx, dp, "till-2", lateJournalSale("fresh-1", "T2-F1", time.Now())); err != nil || !applied {
		t.Fatal(applied, err)
	}
	time.Sleep(1100 * time.Millisecond)
	z2, _, err := generateEOD(ctx, dp, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if z2.SalesCount != 1 || z2.LateEntries != nil {
		t.Fatalf("on-time journaled: sales=%d late=%+v, want 1 and nil", z2.SalesCount, z2.LateEntries)
	}
}

// A late journaled sale that issues a voucher: the sale and the voucher flow
// land in the same Z (AC6).
func TestEOD_LateVoucherSaleConsistent(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.Exec(`INSERT INTO tills (id, name, bearer_hash) VALUES ('till-2', 'Replica', 'h2')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := generateEOD(ctx, dp, "system", "", ""); err != nil {
		t.Fatal(err)
	}
	j := journalSale{Sale: data.SaleDetail{
		ID: "late-v", ReceiptNo: "T2-V1", Status: "completed", SaleType: "sale", Currency: "GBP",
		Subtotal: 1000, Total: 1000, VoucherIssueTotal: 1000,
		CreatedAt: time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339), CashierID: "user1",
		VoucherIssues: []data.SaleDetailVoucherIssue{{VoucherID: "GS-LATE", Amount: 1000}},
		Payments:      []data.SaleDetailPayment{{Method: "cash", Amount: 1000}},
	}}
	if applied, _, err := applyJournal(ctx, dp, "till-2", j); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	time.Sleep(1100 * time.Millisecond)
	z2, _, err := generateEOD(ctx, dp, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if z2.SalesCount != 1 || z2.VouchersIssuedCount != 1 || z2.VouchersIssued != 1000 {
		t.Fatalf("Z N+1 sales=%d vouchers=%d/%d, want the sale and its voucher together", z2.SalesCount, z2.VouchersIssuedCount, z2.VouchersIssued)
	}
}
