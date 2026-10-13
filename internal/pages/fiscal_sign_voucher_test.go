package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#4232 (contract 1.14.0): buildFiscalSignPayload must carry the
// vouchers a sale issues exactly as computeSaleTotals counts them — a
// multi-purpose voucher in total (0%, never in vat_breakdown), a
// single-purpose one as a taxed band — or the signer sees total 0 against
// payments of the face value and every voucher sale stays unsigned.

var voucherSignAt = time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)

// assertPayloadTotalIsSaleTotal ties the payload's total to the total
// CompleteSale will persist: CheckPaymentCoverage (computeSaleTotals) must
// accept a payment of exactly payload.Total and refuse one a cent short.
func assertPayloadTotalIsSaleTotal(t *testing.T, in pos.SaleInput, payloadTotal int64) {
	t.Helper()
	in.Payments = []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(payloadTotal)}}
	if err := pos.CheckPaymentCoverage(in); err != nil {
		t.Fatalf("a payment of payload total %d must cover the sale: %v", payloadTotal, err)
	}
	in.Payments = []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(payloadTotal - 1)}}
	if err := pos.CheckPaymentCoverage(in); err == nil {
		t.Fatalf("payload total %d is below the sale's real total: a cent short still covered it", payloadTotal)
	}
}

func voucherSumGross(p fiscalSignAskPayload) int64 {
	var g int64
	for _, l := range p.VATBreakdown {
		if p.TaxInclusive {
			g += l.Net
		} else {
			g += l.Net + l.Tax
		}
	}
	return g
}

func TestFiscalSignPayload_MultiPurposeVoucherOnlySale(t *testing.T) {
	for _, purpose := range []string{"", data.VoucherPurposeMulti} {
		in := &pos.SaleInput{
			SaleID:        "sale-voucher-only",
			Currency:      "EUR",
			TaxInclusive:  true,
			VoucherIssues: []pos.VoucherIssueInput{{Amount: money.FromMinor(2500), Purpose: purpose}},
		}
		p := buildFiscalSignPayload(in, voucherSignAt)
		if p.Total != 2500 {
			t.Fatalf("purpose %q: a voucher-only sale must sign total 2500 (the face value the customer pays), got %d", purpose, p.Total)
		}
		if len(p.VATBreakdown) != 0 {
			t.Fatalf("purpose %q: a multi-purpose voucher is not a taxable supply at issue and must stay out of vat_breakdown, got %+v", purpose, p.VATBreakdown)
		}
		want := []fiscalSignAskVoucherIssue{{Purpose: data.VoucherPurposeMulti, Amount: 2500}}
		if len(p.VoucherIssues) != 1 || p.VoucherIssues[0] != want[0] {
			t.Fatalf("purpose %q: voucher_issues = %+v, want %+v (purpose normalised)", purpose, p.VoucherIssues, want)
		}
		assertPayloadTotalIsSaleTotal(t, *in, p.Total)
	}
}

// A sale discount never eats a multi-purpose voucher's face value (it is
// added after the clamp, like computeSaleTotals) — and the documented
// reconciliation Σgross − sale_discount + Σmulti = total holds.
func TestFiscalSignPayload_GoodsPlusMultiPurposeVoucherWithDiscount(t *testing.T) {
	for _, inclusive := range []bool{true, false} {
		in := &pos.SaleInput{
			SaleID:       "sale-goods-voucher",
			Currency:     "EUR",
			TaxInclusive: inclusive,
			SaleDiscount: money.FromMinor(200),
			Lines: []pos.SaleLineInput{
				{ItemID: "item-coffee", LocationID: "loc-1", Name: "Coffee", Qty: 1, UnitPrice: money.FromMinor(1190), TaxRateBasisPoints: 1900},
			},
			VoucherIssues: []pos.VoucherIssueInput{{Amount: money.FromMinor(5000), Purpose: data.VoucherPurposeMulti}},
		}
		p := buildFiscalSignPayload(in, voucherSignAt)
		if got := voucherSumGross(p) - p.SaleDiscount + 5000; got != p.Total {
			t.Fatalf("inclusive=%v: Σgross − sale_discount + Σmulti = %d, total = %d", inclusive, got, p.Total)
		}
		assertPayloadTotalIsSaleTotal(t, *in, p.Total)
	}
}

// A discount larger than the goods clamps the goods part at zero but still
// leaves the voucher's full face value payable.
func TestFiscalSignPayload_OverDiscountNeverEatsVoucher(t *testing.T) {
	in := &pos.SaleInput{
		SaleID:        "sale-overdiscount-voucher",
		Currency:      "EUR",
		TaxInclusive:  true,
		SaleDiscount:  money.FromMinor(900),
		Lines:         []pos.SaleLineInput{{ItemID: "item-cake", LocationID: "loc-1", Name: "Cake", Qty: 1, UnitPrice: money.FromMinor(500), TaxRateBasisPoints: 700}},
		VoucherIssues: []pos.VoucherIssueInput{{Amount: money.FromMinor(2000)}},
	}
	p := buildFiscalSignPayload(in, voucherSignAt)
	if p.Total != 2000 {
		t.Fatalf("total must be the voucher's full face value 2000 after the goods part clamps at 0, got %d", p.Total)
	}
	assertPayloadTotalIsSaleTotal(t, *in, p.Total)
}

// A single-purpose voucher is a taxable supply at issue (§3 Abs. 14 UStG):
// it must appear in its rate's band, like a line, and in the charge
// apportionment basis computeSaleTotals uses.
func TestFiscalSignPayload_SinglePurposeVoucherIsATaxedBand(t *testing.T) {
	for _, inclusive := range []bool{true, false} {
		in := &pos.SaleInput{
			SaleID:       "sale-single-voucher",
			Currency:     "EUR",
			TaxInclusive: inclusive,
			Lines: []pos.SaleLineInput{
				{ItemID: "item-cake", LocationID: "loc-1", Name: "Cake", Qty: 1, UnitPrice: money.FromMinor(450), TaxRateBasisPoints: 700},
			},
			Charges: []pos.ChargeInput{{Key: "service", Amount: money.FromMinor(130)}},
			VoucherIssues: []pos.VoucherIssueInput{
				{Amount: money.FromMinor(1000), Purpose: data.VoucherPurposeSingle, VATRateBasisPoints: 1900},
			},
		}
		p := buildFiscalSignPayload(in, voucherSignAt)
		var band *fiscalSignAskVATLine
		for i := range p.VATBreakdown {
			if p.VATBreakdown[i].RateBP == 1900 {
				band = &p.VATBreakdown[i]
			}
		}
		if band == nil || band.Net < 1000 {
			t.Fatalf("inclusive=%v: the single-purpose voucher must be in the 19%% band, got %+v", inclusive, p.VATBreakdown)
		}
		if len(p.VoucherIssues) != 1 || p.VoucherIssues[0].Purpose != data.VoucherPurposeSingle || p.VoucherIssues[0].RateBP != 1900 {
			t.Fatalf("inclusive=%v: voucher_issues = %+v", inclusive, p.VoucherIssues)
		}
		if got := voucherSumGross(p); got != p.Total {
			t.Fatalf("inclusive=%v: Σgross = %d, total = %d (a single-purpose voucher is in vat_breakdown, never counted again)", inclusive, got, p.Total)
		}
		assertPayloadTotalIsSaleTotal(t, *in, p.Total)
	}
}

// No voucher → no voucher_issues key on the wire: an existing signer sees
// no shape change.
func TestFiscalSignPayload_NoVoucherOmitsField(t *testing.T) {
	in := &pos.SaleInput{
		SaleID:   "sale-no-voucher",
		Currency: "EUR",
		Lines:    []pos.SaleLineInput{{ItemID: "item-tea", LocationID: "loc-1", Name: "Tea", Qty: 1, UnitPrice: money.FromMinor(300), TaxRateBasisPoints: 700}},
	}
	raw, err := json.Marshal(buildFiscalSignPayload(in, voucherSignAt))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "voucher_issues") {
		t.Fatalf("a sale without vouchers must omit voucher_issues, got %s", raw)
	}
}

// Through the real tender handler: the payload the signer receives for a
// goods + voucher sale carries the same total CompleteSale persisted.
func TestFiscalSignAsk_TenderedVoucherSalePayloadMatchesPersistedTotal(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	var mu sync.Mutex
	var captured []fiscalSignAskPayload
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-voucher-spy", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		var p fiscalSignAskPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return nil, err
		}
		mu.Lock()
		captured = append(captured, p)
		mu.Unlock()
		return json.RawMessage(`{"status":"approved"}`), nil
	})

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	// 2.50 net + 20% exclusive = 3.00 of goods, plus a 15.00 voucher.
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"cash","amount":1800}],"issue_vouchers":[{"amount":1500,"code":"GS-FS1"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender failed: code %d body %s", rec.Code, rec.Body.String())
	}
	var total int64
	if err := dp.Db.QueryRow(`SELECT total FROM sales`).Scan(&total); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("signer saw %d payloads, want 1", len(captured))
	}
	p := captured[0]
	if p.Total != total || total != 1800 {
		t.Fatalf("signed total %d, persisted total %d, want both 1800", p.Total, total)
	}
	if len(p.VoucherIssues) != 1 || p.VoucherIssues[0].Purpose != data.VoucherPurposeMulti || p.VoucherIssues[0].Amount != 1500 {
		t.Fatalf("voucher_issues = %+v", p.VoucherIssues)
	}
}
