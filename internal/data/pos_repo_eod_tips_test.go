package data

import (
	"context"
	"testing"
	"time"
)

// ut-docs#1007: the reference day-close reports tips BY PAYMENT METHOD,
// held out of revenue — e.g.
//
//	Trinkgeld nach Zahlungsart
//	CASH                      0.00
//	4x CARD                   3.20
//	                     ---------
//	                          3.20
//
// and the card terminal's own settlement proves the split: card sales
// 814.30 + tips 3.20 = terminal total 817.50. dateRangeSummary (backing
// both EndOfDay and EndOfDayRange) had no tip concept at all before this
// card. tip_amount/tip_recipient already exist on payments (migrations
// 019 and 061 respectively, ADR-0061) — this only adds the aggregation
// and the EODReport.Tips field, nothing new is recorded.
//
// Uses the b8* seed helpers from pos_repo_batch8_reports_test.go (same
// package) — the review-vetted convention for anything that calls
// EndOfDay/dateRangeSummary, since a bare time.Now().Format(...) day
// argument is flaky across host timezones near midnight (ut-docs#559,
// ut-docs#869). Payments are seeded with raw INSERTs directly (mirroring
// eod_zreport_local_day_869_test.go), not through InsertPayment, since
// this test doesn't need the round-trip GetSaleDetail coverage the
// tip_amount/tip_recipient tests already own — only dateRangeSummary's
// own aggregation is under test here.
func TestEndOfDay_TipsByMethod(t *testing.T) {
	d := b8OpenDB(t, "eod-tips.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "tip-coffee", 500, nil, 1)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())

	// One cash sale, no tip.
	b8Sale(t, d, "eod-tip-cash", b8At(today), "completed", "sale", 0, 500)
	b8Line(t, d, "eod-tip-cash", 1, "tip-coffee", "", "Name tip-coffee", 1, 0, 0, 500, 500)

	// Four card sales, each carrying its own tip_amount, summing to 320
	// (3.20) — the issue's exact golden total, deliberately unequal
	// amounts (not a lazy "divide by 4") to prove the SUM, not a
	// multiplication. Card SALE amounts (separate from tip_amount) sum
	// to 81430 (814.30) — the issue's other golden number, used below
	// for the terminal-total arithmetic check.
	cardSaleAmounts := []int64{20000, 20430, 20500, 20500} // sums to 81430
	cardTips := []int64{50, 80, 100, 90}                   // sums to 320
	for i, amt := range cardSaleAmounts {
		saleID := "eod-tip-card" + string(rune('1'+i))
		b8Sale(t, d, saleID, b8At(today), "completed", "sale", 0, amt)
		b8Line(t, d, saleID, 1, "tip-coffee", "", "Name tip-coffee", 1, 0, 0, amt, amt)
	}

	if _, err := d.DB.ExecContext(ctx, `INSERT INTO payments
(id, sale_id, method_id, amount, currency, change_given, tip_amount, tip_recipient, paid_at) VALUES
('pay-tip-cash','eod-tip-cash','cash',500,'GBP',0,0,'employee',?),
('pay-tip-card1','eod-tip-card1','card',20050,'GBP',0,50,'employee',?),
('pay-tip-card2','eod-tip-card2','card',20510,'GBP',0,80,'employee',?),
('pay-tip-card3','eod-tip-card3','card',20600,'GBP',0,100,'employee',?),
('pay-tip-card4','eod-tip-card4','card',20590,'GBP',0,90,'employee',?)`,
		b8At(today), b8At(today), b8At(today), b8At(today), b8At(today)); err != nil {
		t.Fatalf("seed payments: %v", err)
	}

	day := b8ExpectedDay(t, d, today, 0, 0)
	rep, err := repo.EndOfDay(ctx, day)
	if err != nil {
		t.Fatalf("EndOfDay: %v", err)
	}

	// Gross/Net/TaxNet come from sales.total/tax_total, not payments — a
	// nonzero tip_amount on a payment must not move any of them. Compute
	// what they should be from the seeded sale totals directly, so this
	// assertion doesn't just restate a hardcoded number that happens to
	// already exclude tips.
	wantGross := int64(500)
	for _, amt := range cardSaleAmounts {
		wantGross += amt
	}
	if rep.Gross != wantGross {
		t.Fatalf("tip_amount must not inflate Gross: want %d, got %d", wantGross, rep.Gross)
	}
	if rep.Net != wantGross {
		t.Fatalf("tip_amount must not inflate Net: want %d, got %d", wantGross, rep.Net)
	}
	if rep.TaxNet != 0 {
		t.Fatalf("tip_amount must not inflate TaxNet: want 0, got %d", rep.TaxNet)
	}

	// EODMethod.In is the tendered amount (sale + tip, per InsertPayment's
	// own "amount" convention — see TestPOSRepo_TipAmount_RoundTrips) minus
	// change_given; it must still reflect exactly what was seeded, and
	// CARD's sale-only total (excluding tips) is the number the issue's
	// terminal-total check needs.
	var cashIn, cardIn int64
	for _, m := range rep.Methods {
		switch m.Method {
		case "cash":
			cashIn = m.In
		case "card":
			cardIn = m.In
		}
	}
	if cashIn != 500 {
		t.Fatalf("want cash EODMethod.In 500, got %d", cashIn)
	}
	wantCardIn := int64(0)
	for i, amt := range cardSaleAmounts {
		wantCardIn += amt + cardTips[i] // amount column = sale + tip, per InsertPayment's convention
	}
	if cardIn != wantCardIn {
		t.Fatalf("want card EODMethod.In %d (tendered, sale+tip), got %d", wantCardIn, cardIn)
	}

	cardSalesOnly := int64(0)
	for _, amt := range cardSaleAmounts {
		cardSalesOnly += amt
	}
	if cardSalesOnly != 81430 {
		t.Fatalf("want seeded card sales-only total 81430 (814.30), got %d", cardSalesOnly)
	}

	// The issue's own golden numbers and its explicit arithmetic check:
	// card sales 814.30 + tips 3.20 = terminal total 817.50.
	var tipTotal int64
	var tipCount int
	var sawCash bool
	for _, tip := range rep.Tips {
		if tip.Method == "cash" {
			sawCash = true
		}
		if tip.Method == "card" {
			tipTotal = tip.Amount
			tipCount = tip.Count
		}
	}
	if tipCount != 4 || tipTotal != 320 {
		t.Fatalf("want CARD tips {Count:4 Amount:320}, got {Count:%d Amount:%d}", tipCount, tipTotal)
	}
	if sawCash {
		t.Fatalf("want no CASH entry in Tips (its payment carries tip_amount 0), got %+v", rep.Tips)
	}
	if len(rep.Tips) != 1 {
		t.Fatalf("want exactly 1 Tips entry (CARD only), got %+v", rep.Tips)
	}
	// The issue's explicit terminal-total arithmetic check: the card
	// terminal's own cut proves the split (814.30 sales + 3.20 tips =
	// 817.50 terminal total) — this exact addition must appear as an
	// assertion, not just be true incidentally.
	if cardSalesOnly+tipTotal != 81750 {
		t.Fatalf("terminal total check: want 81430+320=81750, got %d", cardSalesOnly+tipTotal)
	}
}
