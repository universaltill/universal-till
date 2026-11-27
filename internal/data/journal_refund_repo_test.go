package data

import (
	"context"
	"fmt"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#7064 slice 1: the journal's refund link + status, derived from
// sale_links, completed return sales and the refund legs of the leg journal.

func seedRefundLink(t *testing.T, dbx *posTestDB, returnID, originalID string) {
	t.Helper()
	if _, err := dbx.d.DB.ExecContext(context.Background(),
		`INSERT INTO sale_links(id, sale_id, original_sale_id, reason) VALUES(?, ?, ?, 'refund')`,
		"lnk-"+returnID, returnID, originalID); err != nil {
		t.Fatalf("seed link: %v", err)
	}
}

func seedPendingLeg(t *testing.T, dbx *posTestDB, saleID, req string, states ...string) {
	t.Helper()
	lr := NewPaymentLegJournalRepo(dbx.d.DB)
	for _, s := range states {
		e := LegEntry{RequestID: RefundLegKey(saleID, "stripe", req), State: s, MethodID: "stripe", AmountMinor: 100, Currency: "GBP"}
		if err := lr.Append(context.Background(), e); err != nil {
			t.Fatalf("append %s: %v", s, err)
		}
	}
}

func TestJournalRefunds_FullPartialAndTwoPartials(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ts := "2026-01-02T10:00:00Z"
	seedLifecycleSale(t, dbx, "A", "R-A", "sale", "completed", ts, 100, 0)
	seedLifecycleSale(t, dbx, "RA", "R-RA", "return", "completed", ts, 100, 0)
	seedRefundLink(t, dbx, "RA", "A")
	seedLifecycleSale(t, dbx, "B", "R-B", "sale", "completed", ts, 1000, 0)
	seedLifecycleSale(t, dbx, "RB1", "R-RB1", "return", "completed", ts, 400, 0)
	seedRefundLink(t, dbx, "RB1", "B")
	seedLifecycleSale(t, dbx, "C", "R-C", "sale", "completed", ts, 500, 0)
	seedLifecycleSale(t, dbx, "D", "R-D", "sale", "completed", ts, 1000, 0)
	seedLifecycleSale(t, dbx, "RD1", "R-RD1", "return", "completed", ts, 400, 0)
	seedLifecycleSale(t, dbx, "RD2", "R-RD2", "return", "completed", ts, 600, 0)
	seedRefundLink(t, dbx, "RD1", "D")
	seedRefundLink(t, dbx, "RD2", "D")

	entries := []SaleJournalEntry{
		{ID: "A", Total: 100, SaleType: "sale"}, {ID: "RA", Total: 100, SaleType: "return"},
		{ID: "B", Total: 1000, SaleType: "sale"}, {ID: "C", Total: 500, SaleType: "sale"},
		{ID: "D", Total: 1000, SaleType: "sale"},
	}
	got, err := dbx.repo.JournalRefunds(context.Background(), entries)
	if err != nil {
		t.Fatal(err)
	}
	if got["A"] == nil || got["A"].Status != JournalRefundRefunded {
		t.Fatalf("A = %+v, want refunded", got["A"])
	}
	if r := got["RA"]; r == nil || r.OriginalReceipt != "R-A" {
		t.Fatalf("RA = %+v, want OriginalReceipt R-A", r)
	}
	if b := got["B"]; b == nil || b.Status != JournalRefundPartly || b.Refunded != money.FromMinor(400) || b.Total != money.FromMinor(1000) {
		t.Fatalf("B = %+v, want partly 400 of 1000", b)
	}
	if got["C"] != nil {
		t.Fatalf("C has no return, want no entry, got %+v", got["C"])
	}
	if d := got["D"]; d == nil || d.Status != JournalRefundRefunded || d.Refunded != money.FromMinor(1000) {
		t.Fatalf("D = %+v, want refunded (two partials summed)", d)
	}
}

func TestJournalRefunds_VoidedReturnIgnored(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ts := "2026-01-02T10:00:00Z"
	seedLifecycleSale(t, dbx, "A", "R-A", "sale", "completed", ts, 100, 0)
	seedLifecycleSale(t, dbx, "RA", "R-RA", "return", "voided", ts, 100, 0)
	seedRefundLink(t, dbx, "RA", "A")
	got, err := dbx.repo.JournalRefunds(context.Background(), []SaleJournalEntry{{ID: "A", Total: 100, SaleType: "sale"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["A"] != nil {
		t.Fatalf("voided return must not count, got %+v", got["A"])
	}
}

func TestJournalRefunds_MixedTenderSummedAcrossMethods(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ts := "2026-01-02T10:00:00Z"
	seedLifecycleSale(t, dbx, "M", "R-M", "sale", "completed", ts, 1000, 0)
	seedLifecycleSale(t, dbx, "RM1", "R-RM1", "return", "completed", ts, 300, 0)
	seedLifecycleSale(t, dbx, "RM2", "R-RM2", "return", "completed", ts, 200, 0)
	if _, err := dbx.d.DB.Exec(`UPDATE sales SET tender_type='cash' WHERE id='RM1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.Exec(`UPDATE sales SET tender_type='card' WHERE id='RM2'`); err != nil {
		t.Fatal(err)
	}
	seedRefundLink(t, dbx, "RM1", "M")
	seedRefundLink(t, dbx, "RM2", "M")
	got, err := dbx.repo.JournalRefunds(context.Background(), []SaleJournalEntry{{ID: "M", Total: 1000, SaleType: "sale"}})
	if err != nil {
		t.Fatal(err)
	}
	if m := got["M"]; m == nil || m.Status != JournalRefundPartly || m.Refunded != money.FromMinor(500) {
		t.Fatalf("M = %+v, want partly 500", m)
	}
}

func TestJournalRefunds_PendingLegs(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ts := "2026-01-02T10:00:00Z"
	for _, id := range []string{"P1", "P2", "P3", "P4", "P5", "P6"} {
		seedLifecycleSale(t, dbx, id, "R-"+id, "sale", "completed", ts, 100, 0)
	}
	seedPendingLeg(t, dbx, "P1", "q1", LegAsked, LegUnknown)
	seedPendingLeg(t, dbx, "P2", "q2", LegAsked, LegUnknown, LegWarned)
	seedPendingLeg(t, dbx, "P3", "q3", LegAsked, LegUnknown, LegWarned, LegResolved)
	// P4: pending leg AND a completed full return -> pending wins.
	seedPendingLeg(t, dbx, "P4", "q4", LegAsked, LegUnknown)
	seedLifecycleSale(t, dbx, "RP4", "R-RP4", "return", "completed", ts, 100, 0)
	seedRefundLink(t, dbx, "RP4", "P4")
	// P5: another sale's refund leg and a non-refund leg: ignored.
	seedPendingLeg(t, dbx, "OTHER", "q5", LegAsked, LegUnknown)
	lr := NewPaymentLegJournalRepo(dbx.d.DB)
	if err := lr.Append(context.Background(), LegEntry{RequestID: "tap:P5:x", State: LegAsked, MethodID: "stripe", AmountMinor: 1, Currency: "GBP"}); err != nil {
		t.Fatal(err)
	}
	// P6: resolved leg with partial return -> follows returns.
	seedPendingLeg(t, dbx, "P6", "q6", LegAsked, LegUnknown, LegWarned, LegResolved)
	seedLifecycleSale(t, dbx, "RP6", "R-RP6", "return", "completed", ts, 40, 0)
	seedRefundLink(t, dbx, "RP6", "P6")

	var entries []SaleJournalEntry
	for _, id := range []string{"P1", "P2", "P3", "P4", "P5", "P6"} {
		entries = append(entries, SaleJournalEntry{ID: id, Total: 100, SaleType: "sale"})
	}
	got, err := dbx.repo.JournalRefunds(context.Background(), entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"P1", "P2", "P4"} {
		if got[id] == nil || got[id].Status != JournalRefundPending {
			t.Fatalf("%s = %+v, want pending", id, got[id])
		}
	}
	if got["P4"].Refunded != money.FromMinor(100) {
		t.Fatalf("P4 refunded = %v, want 100 kept for display", got["P4"].Refunded)
	}
	for _, id := range []string{"P3", "P5"} {
		if got[id] != nil {
			t.Fatalf("%s = %+v, want no entry", id, got[id])
		}
	}
	if p := got["P6"]; p == nil || p.Status != JournalRefundPartly {
		t.Fatalf("P6 = %+v, want partly", p)
	}
}

func TestJournalRefunds_ManyIDsAcrossChunks(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ts := "2026-01-02T10:00:00Z"
	var entries []SaleJournalEntry
	for i := 0; i < 600; i++ {
		id := fmt.Sprintf("S%04d", i)
		seedLifecycleSale(t, dbx, id, "R-"+id, "sale", "completed", ts, 100, 0)
		entries = append(entries, SaleJournalEntry{ID: id, Total: 100, SaleType: "sale"})
	}
	for _, i := range []int{3, 550} {
		rid := fmt.Sprintf("RET%d", i)
		seedLifecycleSale(t, dbx, rid, "R-"+rid, "return", "completed", ts, 100, 0)
		seedRefundLink(t, dbx, rid, fmt.Sprintf("S%04d", i))
	}
	got, err := dbx.repo.JournalRefunds(context.Background(), entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["S0003"] == nil || got["S0550"] == nil {
		t.Fatalf("got %d entries %v, want S0003 and S0550 only", len(got), got)
	}
}

func TestJournalRefunds_EmptyAndUnlinkedReturn(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	got, err := dbx.repo.JournalRefunds(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	seedLifecycleSale(t, dbx, "RX", "R-RX", "return", "completed", "2026-01-02T10:00:00Z", 100, 0)
	got, err = dbx.repo.JournalRefunds(context.Background(), []SaleJournalEntry{{ID: "RX", Total: 100, SaleType: "return"}})
	if err != nil || got["RX"] != nil {
		t.Fatalf("unlinked return must have no entry: %v %v", got, err)
	}
}

func TestJournalRefundFor_DetailPage(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ts := "2026-01-02T10:00:00Z"
	seedLifecycleSale(t, dbx, "A", "R-A", "sale", "completed", ts, 300, 0)
	seedLifecycleSale(t, dbx, "RA", "R-RA", "return", "completed", ts, 300, 0)
	seedRefundLink(t, dbx, "RA", "A")
	r, err := dbx.repo.JournalRefundFor(context.Background(), "A", 300, "sale")
	if err != nil || r == nil || r.Status != JournalRefundRefunded {
		t.Fatalf("JournalRefundFor = %+v %v", r, err)
	}
}

func TestDeriveRefundStatus(t *testing.T) {
	m := money.FromMinor
	for _, c := range []struct {
		refunded, total int64
		pending         bool
		want            string
	}{
		{0, 100, false, ""}, {40, 100, false, JournalRefundPartly}, {100, 100, false, JournalRefundRefunded},
		{150, 100, false, JournalRefundRefunded}, {0, 100, true, JournalRefundPending},
		{100, 100, true, JournalRefundPending}, {0, 0, false, ""},
	} {
		if got := deriveRefundStatus(m(c.refunded), m(c.total), c.pending); got != c.want {
			t.Errorf("derive(%d,%d,%v) = %q, want %q", c.refunded, c.total, c.pending, got, c.want)
		}
	}
}

// An older unknown row followed by a newer resolved row for the same leg is
// settled: only the newest row per leg decides (ut-docs#7064).
func TestPendingRefundSales_NewestRowPerLegDecides(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	lr := NewPaymentLegJournalRepo(dbx.d.DB)
	// Re-asked after unknown, then captured: the unknown row is history.
	seedPendingLeg(t, dbx, "OLD", "q1", LegAsked, LegUnknown, LegAsked, LegCaptured)
	seedPendingLeg(t, dbx, "NEW", "q2", LegAsked, LegUnknown)
	seedPendingLeg(t, dbx, "WARN", "q3", LegAsked, LegUnknown, LegWarned)
	got, err := lr.PendingRefundSales(context.Background(), []string{"OLD", "NEW", "WARN"})
	if err != nil {
		t.Fatal(err)
	}
	if got["OLD"] || !got["NEW"] || !got["WARN"] || len(got) != 2 {
		t.Fatalf("got %v, want NEW and WARN only", got)
	}
}

func TestListSalesJournal_ScansIDAndSaleType(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	seedLifecycleSale(t, dbx, "RA", "R-RA", "return", "completed", "2026-01-02T10:00:00Z", 250, 0)
	seedLifecycleSale(t, dbx, "A", "R-A", "sale", "completed", "2026-01-02T09:00:00Z", 250, 0)
	es, _, err := dbx.repo.ListSalesJournal(context.Background(), SalesJournalFilter{AllTills: true, Limit: 10})
	if err != nil || len(es) != 2 {
		t.Fatalf("%v %v", es, err)
	}
	if es[0].ID != "RA" || es[0].SaleType != "return" || es[1].ID != "A" || es[1].SaleType != "sale" {
		t.Fatalf("entries = %+v", es)
	}
	if es[0].SignedTotal() != money.FromMinor(-250) || es[1].SignedTotal() != money.FromMinor(250) {
		t.Fatalf("signed totals %v %v", es[0].SignedTotal(), es[1].SignedTotal())
	}
}
