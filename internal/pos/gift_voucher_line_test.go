package pos

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#4214: gift voucher basket lines. A gift_voucher item is never
// added as goods; AddGiftVoucherLine adds a qty-1, never-merged, never-cached
// line at the entered value that carries the voucher's number, holder and
// use, gets no basket discount, and is taxed only when it is a
// single-purpose ("specific item") voucher.

func giftVoucherTestResolver() mapResolver {
	return mapResolver{
		"GV":    {SKU: "GV", Name: "Gift voucher", Qty: 1, ItemID: "itm-gv", TaxRateBP: 700, TaxCodeID: "tax_red", ItemType: "gift_voucher"},
		"BREAD": {SKU: "BREAD", Name: "Bread", Qty: 1, PriceCents: 1000, ItemID: "itm-bread", TaxRateBP: 2000, TaxCodeID: "tax_std"},
	}
}

func addGV(t *testing.T, s *Service, value int64, v GiftVoucherLine) *Basket {
	t.Helper()
	base, ok := s.ResolveBase("GV")
	if !ok {
		t.Fatal("resolve GV")
	}
	b, err := s.AddGiftVoucherLine(base, money.FromMinor(value), v)
	if err != nil {
		t.Fatalf("AddGiftVoucherLine: %v", err)
	}
	return b
}

func TestAddGiftVoucherLine_QtyOneNoMergeNotCached(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, giftVoucherTestResolver())
	addGV(t, s, 2500, GiftVoucherLine{Code: "111", HolderLabel: "Sam"})
	b := addGV(t, s, 2500, GiftVoucherLine{Code: "222"})
	if len(b.Lines) != 2 {
		t.Fatalf("two voucher adds must stay two lines, got %d", len(b.Lines))
	}
	for _, l := range b.Lines {
		if l.Qty != 1 || l.PriceCents != money.FromMinor(2500) || !l.NoMerge || l.GiftVoucher == nil {
			t.Fatalf("line = %+v", l)
		}
	}
	if b.Lines[0].GiftVoucher.Code != "111" || b.Lines[0].GiftVoucher.HolderLabel != "Sam" || b.Lines[0].GiftVoucher.Purpose != data.VoucherPurposeMulti {
		t.Fatalf("voucher fields = %+v", b.Lines[0].GiftVoucher)
	}
	if s.HasScanCache("GV") {
		t.Fatal("a voucher add must never populate the scan cache")
	}
	if _, err := s.AddGiftVoucherLine(b.Lines[0], money.FromMinor(1000), GiftVoucherLine{Code: "111"}); !errors.Is(err, ErrGiftVoucherCodeInBasket) {
		t.Fatalf("same number twice in one basket: err = %v, want ErrGiftVoucherCodeInBasket", err)
	}
	if !s.HasGiftVoucherCode("222") || s.HasGiftVoucherCode("333") {
		t.Fatal("HasGiftVoucherCode")
	}
}

// A gift_voucher item is never sold as goods: a plain scan and the
// modifier path both refuse it (the scan handler routes it to the popup).
func TestGiftVoucherItem_NeverAddedAsGoods(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, giftVoucherTestResolver())
	b, found, _ := s.ScanQtyWithResult("GV", 1)
	if found || len(b.Lines) != 0 {
		t.Fatalf("plain scan of a gift voucher item: found=%v lines=%d", found, len(b.Lines))
	}
	if s.HasScanCache("GV") {
		t.Fatal("refused scan must not be cached")
	}
	base, _ := s.ResolveBase("GV")
	b = s.AddLineWithModifiers(base, 1, nil)
	if len(b.Lines) != 0 {
		t.Fatalf("modifier path must refuse a gift voucher item, got %d lines", len(b.Lines))
	}
}

// Multi-purpose: no tax, no basket discount, outside the service-charge
// base — the total is exactly the goods-only total plus the face value.
func TestGiftVoucherLine_TotalsMultiPurpose(t *testing.T) {
	for _, inclusive := range []bool{true, false} {
		cfg := Config{TaxRateBasisPoints: 2000, TaxInclusive: inclusive, ServiceChargeRateBasisPoints: 1000}
		base := NewServiceWithResolver(cfg, giftVoucherTestResolver())
		_, _ = base.Scan("BREAD")
		base.SetDiscountPercent(1000)
		want := base.Basket()

		s := NewServiceWithResolver(cfg, giftVoucherTestResolver())
		_, _ = s.Scan("BREAD")
		s.SetDiscountPercent(1000)
		addGV(t, s, 2500, GiftVoucherLine{Code: "111"})
		got := s.Basket()
		if got.Discount != want.Discount {
			t.Fatalf("inclusive=%v: discount %v, want %v (voucher must not be discounted)", inclusive, got.Discount, want.Discount)
		}
		if got.ServiceCharge != want.ServiceCharge {
			t.Fatalf("inclusive=%v: service charge %v, want %v", inclusive, got.ServiceCharge, want.ServiceCharge)
		}
		if got.Tax != want.Tax {
			t.Fatalf("inclusive=%v: tax %v, want %v (an any-purchase voucher carries no VAT)", inclusive, got.Tax, want.Tax)
		}
		if got.Total != want.Total.Add(money.FromMinor(2500)) {
			t.Fatalf("inclusive=%v: total %v, want %v + 25.00", inclusive, got.Total, want.Total)
		}
		if got.Lines[1].LineTotal != money.FromMinor(2500) {
			t.Fatalf("voucher line total %v", got.Lines[1].LineTotal)
		}
	}
}

// A voucher-only basket with a fixed basket discount: the discount has no
// goods to apply to, so the full face value is still owed.
func TestGiftVoucherLine_FixedDiscountNeverReducesVoucher(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, giftVoucherTestResolver())
	addGV(t, s, 2500, GiftVoucherLine{Code: "111"})
	s.SetDiscount(money.FromMinor(500))
	if b := s.Basket(); b.Total != money.FromMinor(2500) || !b.Discount.IsZero() {
		t.Fatalf("voucher-only basket with a 5.00 discount: total %v discount %v, want 25.00 / 0", b.Total, b.Discount)
	}
}

// Single-purpose ("specific item"): taxed now at the item's own rate.
func TestGiftVoucherLine_TotalsSinglePurposeTaxedAtItemRate(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: true}, giftVoucherTestResolver())
	addGV(t, s, 1070, GiftVoucherLine{Code: "111", Purpose: data.VoucherPurposeSingle})
	b := s.Basket()
	if b.Total != money.FromMinor(1070) || b.Tax != money.FromMinor(70) {
		t.Fatalf("inclusive single-purpose 10.70 at 7%%: total %v tax %v, want 10.70 / 0.70", b.Total, b.Tax)
	}
	if rate, blocked := s.EffectiveLineTaxRateBP(b.Lines[0]); rate != 700 || blocked {
		t.Fatalf("EffectiveLineTaxRateBP = %d blocked=%v", rate, blocked)
	}
}

func TestGiftVoucherLine_QtyAndDiscountLocked(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, giftVoucherTestResolver())
	b := addGV(t, s, 2500, GiftVoucherLine{Code: "111"})
	key := b.Lines[0].LineKey
	s.UpdateLineByKey(key, 3, money.FromMinor(100))
	l := s.Basket().Lines[0]
	if l.Qty != 1 || !l.LineDiscount.IsZero() {
		t.Fatalf("voucher line qty/discount must stay 1/0, got %v/%v", l.Qty, l.LineDiscount)
	}
	s.UpdateLineByKey(key, 0, 0)
	if n := len(s.Basket().Lines); n != 0 {
		t.Fatalf("qty 0 removes the voucher line, got %d lines", n)
	}
}

func TestGiftVoucherLine_HoldResumeRoundTrip(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, giftVoucherTestResolver())
	addGV(t, s, 2500, GiftVoucherLine{Code: "111", HolderLabel: "Sam", Purpose: data.VoucherPurposeSingle})
	want := s.Basket()
	raw, err := json.Marshal(s.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var back BasketSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	s.Reset()
	s.RestoreHeld(back, HeldOrigin{})
	got := s.Basket()
	if len(got.Lines) != 1 || got.Lines[0].GiftVoucher == nil {
		t.Fatalf("restored lines = %+v", got.Lines)
	}
	if gv := got.Lines[0].GiftVoucher; gv.Code != "111" || gv.HolderLabel != "Sam" || gv.Purpose != data.VoucherPurposeSingle {
		t.Fatalf("restored voucher = %+v", gv)
	}
	if !got.Lines[0].NoMerge || got.Total != want.Total {
		t.Fatalf("restored NoMerge=%v total=%v want %v", got.Lines[0].NoMerge, got.Total, want.Total)
	}
}
