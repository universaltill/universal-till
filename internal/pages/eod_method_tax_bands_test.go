package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/print"
)

// Method x VAT-rate cross-tab tests (ut-docs#1004). Fixture conventions
// follow eod_tax_bands_test.go's etbXxx helpers (same package).
//
// NOTE for future readers: the ut-docs#1004 card body illustrates the
// concept with figures from a real reference day-close document
// (21.38/305.42/326.80 etc.). Those numbers are NOT reproduced here on
// purpose: the payments table has no line-level payment attribution, so
// the cross-tab is derived by apportionment, and forcing the card's
// illustrative table byte-for-byte would prove nothing about the
// algorithm. These fixtures are constructed to pin the apportionment
// rule and the reconciliation identities instead.

// etbPayment inserts a payments row in etbSale/etbLine's hand-fixture
// style. amount follows production's convention: the FULL tendered
// amount including any tip (see EODTip's doc comment in pos_repo.go);
// change/tips are separate columns the revenue-share query subtracts.
func etbPayment(t *testing.T, d *db.DB, saleID, id, methodID string, amount, changeGiven, tipAmount int64) {
	t.Helper()
	etbExec(t, d, `INSERT INTO payments (id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at)
VALUES (?, ?, ?, ?, 'GBP', ?, ?, ?)`, id, saleID, methodID, amount, changeGiven, tipAmount, "2026-01-01T12:00:00Z")
}

// emtbEndOfDay runs the REAL production trio — POSRepo.EndOfDay +
// attachEODTaxBands + attachEODMethodTaxBands — exactly as generateEOD
// does after ut-docs#1004.
func emtbEndOfDay(t *testing.T, d *db.DB, day string) data.EODReport {
	t.Helper()
	repo := data.NewPOSRepo(d.DB)
	rep, err := repo.EndOfDay(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	if err := attachEODTaxBands(context.Background(), repo, &rep); err != nil {
		t.Fatalf("attachEODTaxBands: %v", err)
	}
	if err := attachEODMethodTaxBands(context.Background(), repo, &rep); err != nil {
		t.Fatalf("attachEODMethodTaxBands: %v", err)
	}
	return rep
}

// assertEODMethodTaxBandIdentities pins the cross-tab's own
// internal-consistency contract (ut-docs#1004):
//
//	every cell: Gross == Net + Tax        (Gross is DERIVED, never split)
//	per rate:   sum(cells) == rep.TaxBands[rate], field for field
//
// The per-rate identity holds with ZERO drift by construction (the
// floor+last-payment-remainder split always sums back to the sale's own
// band amount), so it is asserted exactly, not approximately. It assumes
// every banded sale has payments — true for anything pos.CompleteSale
// wrote; the zero-payment skip has its own dedicated test below.
func assertEODMethodTaxBandIdentities(t *testing.T, rep data.EODReport) {
	t.Helper()
	type sums struct{ net, tax, gross int64 }
	byRate := map[int]*sums{}
	for _, c := range rep.MethodTaxBands {
		if c.Gross != c.Net+c.Tax {
			t.Fatalf("cell %s/%d: Gross %d != Net %d + Tax %d", c.Method, c.RateBP, c.Gross, c.Net, c.Tax)
		}
		s, ok := byRate[c.RateBP]
		if !ok {
			s = &sums{}
			byRate[c.RateBP] = s
		}
		s.net += c.Net
		s.tax += c.Tax
		s.gross += c.Gross
	}
	if len(byRate) != len(rep.TaxBands) {
		t.Fatalf("cross-tab covers %d rates, TaxBands has %d: %+v vs %+v", len(byRate), len(rep.TaxBands), rep.MethodTaxBands, rep.TaxBands)
	}
	for _, b := range rep.TaxBands {
		s := byRate[b.RateBP]
		if s == nil {
			t.Fatalf("rate %d in TaxBands but absent from MethodTaxBands", b.RateBP)
		}
		if s.net != b.Net || s.tax != b.Tax || s.gross != b.Gross {
			t.Fatalf("rate %d cell sums %d/%d/%d != TaxBand %d/%d/%d",
				b.RateBP, s.net, s.tax, s.gross, b.Net, b.Tax, b.Gross)
		}
	}
}

// methodSums totals the cross-tab per method.
func methodSums(rep data.EODReport, method string) (net, tax, gross int64) {
	for _, c := range rep.MethodTaxBands {
		if c.Method == method {
			net += c.Net
			tax += c.Tax
			gross += c.Gross
		}
	}
	return
}

// Golden single-tender-per-sale day: the ut-docs#1003 golden fixture (3
// bands, a return cancelling part of the 7% band) with ONE full-amount
// payment per sale, spread over two methods so the cross-tab has real
// breadth — cash carries the 0% and 7% bands, card the 7% and 19%, and
// the return refunds in cash so it must subtract from cash/7%, not
// card/7%.
func TestEndOfDay_MethodTaxBands_GoldenSingleTender(t *testing.T) {
	d := etbOpenDB(t, "eod-mtb-golden.db")

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	at := etbAt(today)

	etbItem(t, d, "vat-itm", 100)

	// Same rows as TestEndOfDay_TaxBands_PerRateNetTaxGross, plus payments.
	etbSale(t, d, "vat-s1", at, "completed", "sale", 6744, 104580)
	etbLine(t, d, "vat-s1", 1, "vat-itm", "Groceries 7%", 1, 700, 6744, 96336, 103080)
	etbLine(t, d, "vat-s1", 2, "vat-itm", "Zero-rated", 1, 0, 0, 1500, 1500)
	etbPayment(t, d, "vat-s1", "vat-s1-p1", "cash", 104580, 0, 0)

	etbSale(t, d, "vat-s2", at, "completed", "sale", 3078, 21170)
	etbLine(t, d, "vat-s2", 1, "vat-itm", "Standard 19%", 1, 1900, 2868, 15092, 17960)
	etbLine(t, d, "vat-s2", 2, "vat-itm", "Groceries 7%", 1, 700, 210, 3000, 3210)
	etbPayment(t, d, "vat-s2", "vat-s2-p1", "card", 21170, 0, 0)

	// Return refunded in CASH: must reduce cash/7%, leaving card/7% whole.
	etbSale(t, d, "vat-r1", at, "completed", "return", 210, 3210)
	etbLine(t, d, "vat-r1", 1, "vat-itm", "Groceries 7%", 1, 700, 210, 3000, 3210)
	etbPayment(t, d, "vat-r1", "vat-r1-p1", "cash", 3210, 0, 0)

	// Voided sale WITH a payment: excluded entirely (status filter).
	etbSale(t, d, "vat-v1", at, "voided", "sale", 999, 999)
	etbLine(t, d, "vat-v1", 1, "vat-itm", "Voided", 1, 1900, 999, 999, 999)
	etbPayment(t, d, "vat-v1", "vat-v1-p1", "card", 999, 0, 0)

	rep := emtbEndOfDay(t, d, etbDay(t, d, today))

	// Deterministic order: Method ascending, then RateBP ascending.
	want := []data.MethodTaxBand{
		{Method: "card", RateBP: 700, Net: 3000, Tax: 210, Gross: 3210},
		{Method: "card", RateBP: 1900, Net: 15092, Tax: 2868, Gross: 17960},
		{Method: "cash", RateBP: 0, Net: 1500, Tax: 0, Gross: 1500},
		{Method: "cash", RateBP: 700, Net: 93336, Tax: 6534, Gross: 99870}, // 96336-3000 / 6744-210
	}
	if len(rep.MethodTaxBands) != len(want) {
		t.Fatalf("expected %d cells, got %+v", len(want), rep.MethodTaxBands)
	}
	for i, w := range want {
		if rep.MethodTaxBands[i] != w {
			t.Fatalf("cell[%d] = %+v, want %+v", i, rep.MethodTaxBands[i], w)
		}
	}

	// Per-rate sums must reproduce the #1003 golden TaxBand values exactly
	// (0bp 1500/0/1500, 700bp 96336/6744/103080, 1900bp 15092/2868/17960)
	// — asserted via the identities helper against rep.TaxBands, which the
	// #1003 golden test already pins to those literals.
	assertEODMethodTaxBandIdentities(t, rep)

	// Per-method Gross sums reconcile to the payment-method breakdown. No
	// tips in this fixture, so each method's cell sum equals its NET
	// takings, In − Out (Out is nonzero only for cash, which took the
	// refund).
	for _, m := range rep.Methods {
		_, _, gross := methodSums(rep, m.Method)
		if gross != m.In-m.Out {
			t.Fatalf("method %s: cross-tab gross %d != In %d - Out %d", m.Method, gross, m.In, m.Out)
		}
	}
}

// A genuine constructed split-tender sale (the card explicitly requires
// one — the real reference day has zero split-tender receipts): two rate
// bands, two payments, hand-computed expected cells.
//
// Sale: 7% line 3000/210/3210 + 19% line 5705/1085/6790, total 10000.
// Payments: CASH 6000 + CARD 4000. Query order is alphabetical by
// method_id within the sale, so card (4000) splits first and cash (6000),
// being LAST, takes each band's remainder. totalTendered = 10000.
//
//	7%  Net 3000:  card floor(3000*4000/10000) = 1200; cash 3000-1200 = 1800
//	7%  Tax  210:  card floor(210*4000/10000)  =   84; cash  210-84   =  126
//	19% Net 5705:  card floor(5705*4000/10000) = 2282; cash 5705-2282 = 3423
//	19% Tax 1085:  card floor(1085*4000/10000) =  434; cash 1085-434  =  651
//
// The 40/60 split divides exactly here (figures chosen so it does), which
// is what makes the per-method assertion below exact; the flooring path
// with a genuinely discarded fraction is exercised end-to-end by the
// return test below and in isolation by TestApportionAmount.
func TestEndOfDay_MethodTaxBands_SplitTender(t *testing.T) {
	d := etbOpenDB(t, "eod-mtb-split.db")

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	at := etbAt(today)

	etbItem(t, d, "vat-itm", 100)
	etbSale(t, d, "split-s1", at, "completed", "sale", 1295, 10000)
	etbLine(t, d, "split-s1", 1, "vat-itm", "Groceries 7%", 1, 700, 210, 3000, 3210)
	etbLine(t, d, "split-s1", 2, "vat-itm", "Standard 19%", 1, 1900, 1085, 5705, 6790)
	etbPayment(t, d, "split-s1", "split-s1-p1", "cash", 6000, 0, 0)
	etbPayment(t, d, "split-s1", "split-s1-p2", "card", 4000, 0, 0)

	rep := emtbEndOfDay(t, d, etbDay(t, d, today))

	want := []data.MethodTaxBand{
		{Method: "card", RateBP: 700, Net: 1200, Tax: 84, Gross: 1284},
		{Method: "card", RateBP: 1900, Net: 2282, Tax: 434, Gross: 2716},
		{Method: "cash", RateBP: 700, Net: 1800, Tax: 126, Gross: 1926},
		{Method: "cash", RateBP: 1900, Net: 3423, Tax: 651, Gross: 4074},
	}
	if len(rep.MethodTaxBands) != len(want) {
		t.Fatalf("expected %d cells, got %+v", len(want), rep.MethodTaxBands)
	}
	for i, w := range want {
		if rep.MethodTaxBands[i] != w {
			t.Fatalf("cell[%d] = %+v, want %+v", i, rep.MethodTaxBands[i], w)
		}
	}

	// Zero drift per rate: cell sums reproduce the sale's own
	// pos.VATBandsForSale output (== rep.TaxBands for this one-sale day).
	assertEODMethodTaxBandIdentities(t, rep)

	// Per-method Gross reproduces this sale's tendered amount per method.
	for method, tendered := range map[string]int64{"cash": 6000, "card": 4000} {
		_, _, gross := methodSums(rep, method)
		if gross != tendered {
			t.Fatalf("method %s: cross-tab gross %d != tendered %d", method, gross, tendered)
		}
	}
}

// Return/refund sign: a sale paid in cash, then a partial return refunded
// SPLIT-tender (card 1140 + cash 1000). The return's OWN payment split
// must drive its sign-flipped contribution — card ends the day with a
// negative 7% cell even though the original sale never saw a card.
//
// Return bands 7%: Net 2000, Tax 140; refund order card(1140), cash(1000),
// totalTendered 2140:
//
//	Net: card floor(2000*1140/2140) = floor(1065.42) = 1065; cash 935
//	Tax: card floor(140*1140/2140)  = floor(74.57)   =   74; cash  66
//
// Signed −1 and merged with the sale's cash/7% cell (3000/210):
// cash/7% = 3000−935 / 210−66 = 2065/144, card/7% = −1065/−74.
func TestEndOfDay_MethodTaxBands_ReturnUsesOwnPaymentSplit(t *testing.T) {
	d := etbOpenDB(t, "eod-mtb-return.db")

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	at := etbAt(today)

	etbItem(t, d, "vat-itm", 100)
	etbSale(t, d, "ret-s1", at, "completed", "sale", 210, 3210)
	etbLine(t, d, "ret-s1", 1, "vat-itm", "Groceries 7%", 1, 700, 210, 3000, 3210)
	etbPayment(t, d, "ret-s1", "ret-s1-p1", "cash", 3210, 0, 0)

	etbSale(t, d, "ret-r1", at, "completed", "return", 140, 2140)
	etbLine(t, d, "ret-r1", 1, "vat-itm", "Groceries 7%", 1, 700, 140, 2000, 2140)
	etbPayment(t, d, "ret-r1", "ret-r1-p1", "card", 1140, 0, 0)
	etbPayment(t, d, "ret-r1", "ret-r1-p2", "cash", 1000, 0, 0)

	rep := emtbEndOfDay(t, d, etbDay(t, d, today))

	want := []data.MethodTaxBand{
		{Method: "card", RateBP: 700, Net: -1065, Tax: -74, Gross: -1139},
		{Method: "cash", RateBP: 700, Net: 2065, Tax: 144, Gross: 2209},
	}
	if len(rep.MethodTaxBands) != len(want) {
		t.Fatalf("expected %d cells, got %+v", len(want), rep.MethodTaxBands)
	}
	for i, w := range want {
		if rep.MethodTaxBands[i] != w {
			t.Fatalf("cell[%d] = %+v, want %+v", i, rep.MethodTaxBands[i], w)
		}
	}
	assertEODMethodTaxBandIdentities(t, rep)
}

// Tips caveat (the documented reconciliation rule, pinned so it can never
// regress into "looks equal by coincidence"): a tipped card payment's tip
// carries no VAT rate, so it is EXCLUDED from the cross-tab. The card
// column's Gross therefore does NOT equal EODMethod.In (which includes
// the tip per InsertPayment's convention) — it equals In minus that
// method's EODTip.Amount.
func TestEndOfDay_MethodTaxBands_TipsExcluded(t *testing.T) {
	d := etbOpenDB(t, "eod-mtb-tips.db")

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	at := etbAt(today)

	etbItem(t, d, "vat-itm", 100)
	etbSale(t, d, "tip-s1", at, "completed", "sale", 1900, 11900)
	etbLine(t, d, "tip-s1", 1, "vat-itm", "Standard 19%", 1, 1900, 1900, 10000, 11900)
	// amount includes the £12.00 tip on top of the £119.00 sale.
	etbPayment(t, d, "tip-s1", "tip-s1-p1", "card", 13100, 0, 1200)

	rep := emtbEndOfDay(t, d, etbDay(t, d, today))

	want := []data.MethodTaxBand{
		{Method: "card", RateBP: 1900, Net: 10000, Tax: 1900, Gross: 11900},
	}
	if len(rep.MethodTaxBands) != 1 || rep.MethodTaxBands[0] != want[0] {
		t.Fatalf("cells = %+v, want %+v", rep.MethodTaxBands, want)
	}
	assertEODMethodTaxBandIdentities(t, rep)

	var in, tip int64
	for _, m := range rep.Methods {
		if m.Method == "card" {
			in = m.In
		}
	}
	for _, tp := range rep.Tips {
		if tp.Method == "card" {
			tip = tp.Amount
		}
	}
	if in != 13100 || tip != 1200 {
		t.Fatalf("fixture drifted: card In=%d Tips=%d, want 13100/1200", in, tip)
	}
	_, _, gross := methodSums(rep, "card")
	if gross == in {
		t.Fatalf("card cross-tab gross %d equals raw In — the tip leaked into the cross-tab", gross)
	}
	if gross != in-tip {
		t.Fatalf("card cross-tab gross %d != In %d - tips %d", gross, in, tip)
	}
}

// Defensive zero-tendered skip: a completed sale with NO payments row is
// unreachable through pos.CompleteSale (it always records >=1 payment),
// but a hand-broken row must be skipped — NOT divide the apportionment by
// zero — while TaxBands (payment-agnostic) still reports it.
func TestEndOfDay_MethodTaxBands_ZeroPaymentSaleSkipped(t *testing.T) {
	d := etbOpenDB(t, "eod-mtb-nopay.db")

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	at := etbAt(today)

	etbItem(t, d, "vat-itm", 100)
	etbSale(t, d, "nopay-s1", at, "completed", "sale", 1900, 11900)
	etbLine(t, d, "nopay-s1", 1, "vat-itm", "Standard 19%", 1, 1900, 1900, 10000, 11900)

	rep := emtbEndOfDay(t, d, etbDay(t, d, today))

	if len(rep.MethodTaxBands) != 0 {
		t.Fatalf("payment-less sale produced cells: %+v", rep.MethodTaxBands)
	}
	if len(rep.TaxBands) != 1 {
		t.Fatalf("TaxBands should still report the sale: %+v", rep.TaxBands)
	}
}

// The apportionment helper in isolation, with a genuinely inexact split:
// every share but the last floors, the last takes the exact remainder,
// and the parts always sum back to the whole (drift-free by construction).
// TestAttachEODBands_MatchesSeparateAttachCalls proves attachEODBands (the
// function generateEOD and the range handler actually call, ut-docs#1004
// review finding) produces the SAME result as calling attachEODTaxBands +
// attachEODMethodTaxBands separately on a static fixture — i.e. the
// single-read refactor changed nothing about the math, only removed the
// window between two reads that a live (not static-test) database could
// see a sale complete inside.
func TestAttachEODBands_MatchesSeparateAttachCalls(t *testing.T) {
	d := etbOpenDB(t, "eod-mtb-attach-bands.db")
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	at := etbAt(today)

	etbItem(t, d, "vat-itm", 100)
	etbSale(t, d, "ab-s1", at, "completed", "sale", 6744, 104580)
	etbLine(t, d, "ab-s1", 1, "vat-itm", "Groceries 7%", 1, 700, 6744, 96336, 103080)
	etbLine(t, d, "ab-s1", 2, "vat-itm", "Zero-rated", 1, 0, 0, 1500, 1500)
	etbPayment(t, d, "ab-s1", "ab-s1-p1", "cash", 60000, 0, 0)
	etbPayment(t, d, "ab-s1", "ab-s1-p2", "card", 44580, 0, 0)

	day := etbDay(t, d, today)
	repo := data.NewPOSRepo(d.DB)

	separate, err := repo.EndOfDay(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	if err := attachEODTaxBands(context.Background(), repo, &separate); err != nil {
		t.Fatalf("attachEODTaxBands: %v", err)
	}
	if err := attachEODMethodTaxBands(context.Background(), repo, &separate); err != nil {
		t.Fatalf("attachEODMethodTaxBands: %v", err)
	}

	combined, err := repo.EndOfDay(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	if err := attachEODBands(context.Background(), repo, &combined); err != nil {
		t.Fatalf("attachEODBands: %v", err)
	}

	if len(combined.TaxBands) == 0 || len(combined.MethodTaxBands) == 0 {
		t.Fatalf("attachEODBands left a breakdown empty: TaxBands=%+v MethodTaxBands=%+v",
			combined.TaxBands, combined.MethodTaxBands)
	}
	if len(combined.TaxBands) != len(separate.TaxBands) {
		t.Fatalf("attachEODBands TaxBands = %+v, want %+v", combined.TaxBands, separate.TaxBands)
	}
	for i := range separate.TaxBands {
		if combined.TaxBands[i] != separate.TaxBands[i] {
			t.Fatalf("TaxBands[%d] = %+v, want %+v", i, combined.TaxBands[i], separate.TaxBands[i])
		}
	}
	if len(combined.MethodTaxBands) != len(separate.MethodTaxBands) {
		t.Fatalf("attachEODBands MethodTaxBands = %+v, want %+v", combined.MethodTaxBands, separate.MethodTaxBands)
	}
	for i := range separate.MethodTaxBands {
		if combined.MethodTaxBands[i] != separate.MethodTaxBands[i] {
			t.Fatalf("MethodTaxBands[%d] = %+v, want %+v", i, combined.MethodTaxBands[i], separate.MethodTaxBands[i])
		}
	}
	assertEODMethodTaxBandIdentities(t, combined)
}

func TestApportionAmount(t *testing.T) {
	// 100 split over shares 333/333/334 (total 1000):
	// floor(100*333/1000)=33, floor(100*333/1000)=33, last = 100-66 = 34.
	got := apportionAmount(100, []int64{333, 333, 334}, 1000)
	want := []int64{33, 33, 34}
	if len(got) != len(want) {
		t.Fatalf("apportionAmount = %v, want %v", got, want)
	}
	var sum int64
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("apportionAmount = %v, want %v", got, want)
		}
		sum += got[i]
	}
	if sum != 100 {
		t.Fatalf("parts sum %d != whole 100", sum)
	}
	// Single share: gets the whole amount exactly.
	if got := apportionAmount(777, []int64{123}, 123); len(got) != 1 || got[0] != 777 {
		t.Fatalf("single-share apportionAmount = %v, want [777]", got)
	}
}

// Mirror of TestBuildEODDoc_VATRateBandWideAmountsNotClipped for the
// method x VAT-rate section: a £1,000,000.00 gross must print every digit.
// Grouped-by-method layout (ut-docs#1004 review finding): a method's rows
// print as a heading line + the SAME 4-column rateTableRows table "BY VAT
// RATE" uses, so this reuses that layout's own proven ~£1M headroom rather
// than a dedicated 5-column budget.
func TestBuildEODDoc_MethodTaxBandWideAmountsNotClipped(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-08-25", GeneratedAt: "2026-08-25T21:30:00Z",
		MethodTaxBands: []data.MethodTaxBand{
			{Method: "card", RateBP: 1900, Net: 84033613, Tax: 15966387, Gross: 100000000},
		},
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	if !strings.Contains(out, "BY METHOD & VAT RATE") || !strings.Contains(out, "Card:") {
		t.Fatalf("BY METHOD & VAT RATE section or Card heading missing:\n%s", out)
	}
	row := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "£840,336.13") {
			row = l
			break
		}
	}
	if row == "" || !strings.Contains(row, "19%") || !strings.Contains(row, "£159,663.87") || !strings.Contains(row, "£1,000,000.00") {
		t.Errorf("method/VAT row lost a column: %q\n%s", row, out)
	}
}

// TestBuildEODDoc_MethodTaxBandOrdinaryAmountNotClipped is the ut-docs#1004
// independent review's actual finding, reproduced as a regression test: the
// FIRST-DRAFT dedicated 5-column layout clipped digits at perfectly
// ordinary amounts (a single £11,900 cell with a 7-letter method name),
// not just at ~£1M extremes — because a 5th (Method) column shared the
// same print.Width=42 budget as the 4 money/rate columns, and had no
// further fallback once even ITS tight-collapsed widths didn't fit. The
// £1,000,000 test above alone would NOT have caught this (it only proved
// the layout survives at the one scale it was tuned for) — this is
// exactly the class of gap a cherry-picked "wide amount" fixture can hide.
func TestBuildEODDoc_MethodTaxBandOrdinaryAmountNotClipped(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-08-25", GeneratedAt: "2026-08-25T21:30:00Z",
		MethodTaxBands: []data.MethodTaxBand{
			{Method: "voucher", RateBP: 1900, Net: 1000000, Tax: 190000, Gross: 1190000},
		},
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	row := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "£10,000.00") {
			row = l
			break
		}
	}
	if row == "" || !strings.Contains(row, "19%") || !strings.Contains(row, "£1,900.00") || !strings.Contains(row, "£11,900.00") {
		t.Errorf("ordinary-amount method/VAT row lost a column: %q\n%s", row, out)
	}
	if !strings.Contains(out, "Voucher:") {
		t.Errorf("Voucher heading missing:\n%s", out)
	}
}

// TestBuildEODDoc_MethodTaxBandMultipleMethodsGrouped proves two methods'
// rows print as separate headed groups, not interleaved or merged.
func TestBuildEODDoc_MethodTaxBandMultipleMethodsGrouped(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-08-25", GeneratedAt: "2026-08-25T21:30:00Z",
		MethodTaxBands: []data.MethodTaxBand{
			{Method: "card", RateBP: 700, Net: 30542, Tax: 2138, Gross: 32680},
			{Method: "card", RateBP: 1900, Net: 7084, Tax: 1346, Gross: 8430},
			{Method: "cash", RateBP: 0, Net: 1500, Tax: 0, Gross: 1500},
		},
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	cardIdx := strings.Index(out, "Card:")
	cashIdx := strings.Index(out, "Cash:")
	if cardIdx == -1 || cashIdx == -1 || cardIdx > cashIdx {
		t.Fatalf("expected a Card: heading before a Cash: heading:\n%s", out)
	}
	for _, want := range []string{"£305.42", "£70.84", "£15.00"} {
		if !strings.Contains(out, want) {
			t.Errorf("cell %q missing from grouped output:\n%s", want, out)
		}
	}
}
