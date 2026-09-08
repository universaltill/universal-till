package pos

import (
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// TestPendingVoucher_SetGetClear (ut-docs#1833): SetPendingVoucher stashes a
// scanned Gutschein's id/balance on both the engine and the published
// Basket; ClearPendingVoucher removes it again.
func TestPendingVoucher_SetGetClear(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{})

	if got := s.PendingVoucherID(); got != "" {
		t.Fatalf("PendingVoucherID before any scan = %q, want empty", got)
	}
	if got := s.PendingVoucherBalance(); got != 0 {
		t.Fatalf("PendingVoucherBalance before any scan = %v, want 0", got)
	}

	s.SetPendingVoucher("GS-1234", money.FromMinor(2500))

	if got := s.PendingVoucherID(); got != "GS-1234" {
		t.Fatalf("PendingVoucherID = %q, want GS-1234", got)
	}
	if got := s.PendingVoucherBalance(); got != money.FromMinor(2500) {
		t.Fatalf("PendingVoucherBalance = %v, want 2500", got)
	}
	b := s.Basket()
	if b.VoucherID != "GS-1234" || b.VoucherBalance != money.FromMinor(2500) {
		t.Fatalf("basket voucher fields = %+v", b)
	}

	s.ClearPendingVoucher()
	if got := s.PendingVoucherID(); got != "" {
		t.Fatalf("PendingVoucherID after clear = %q, want empty", got)
	}
	if got := s.PendingVoucherBalance(); got != 0 {
		t.Fatalf("PendingVoucherBalance after clear = %v, want 0", got)
	}
	b = s.Basket()
	if b.VoucherID != "" || b.VoucherBalance != 0 {
		t.Fatalf("basket voucher fields after clear = %+v", b)
	}
}

// TestPendingVoucher_ClearedOnReset: a pending voucher must not survive a
// sale completion / basket reset, same rule as CustomerID/CustomerName
// (ut-docs#1833).
func TestPendingVoucher_ClearedOnReset(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{})
	s.SetPendingVoucher("GS-9999", money.FromMinor(500))

	s.Reset()

	if got := s.PendingVoucherID(); got != "" {
		t.Fatalf("PendingVoucherID after Reset = %q, want empty", got)
	}
	if got := s.PendingVoucherBalance(); got != 0 {
		t.Fatalf("PendingVoucherBalance after Reset = %v, want 0", got)
	}
	b := s.Basket()
	if b.VoucherID != "" || b.VoucherBalance != 0 {
		t.Fatalf("basket voucher fields after Reset = %+v", b)
	}
}

// TestPendingVoucher_SurvivesRecompute: unlike CustomerID (re-derived from
// the totals snapshot on every recompute), the pending voucher is not
// totals-relevant -- adding a line and letting the basket recompute must
// not disturb it.
func TestPendingVoucher_SurvivesRecompute(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{
		"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100},
	})
	s.SetPendingVoucher("GS-1", money.FromMinor(1000))

	if _, err := s.ScanQty("ABC", 1); err != nil {
		t.Fatalf("ScanQty error: %v", err)
	}
	b := s.Basket()
	if b.VoucherID != "GS-1" || b.VoucherBalance != money.FromMinor(1000) {
		t.Fatalf("pending voucher lost across recompute: %+v", b)
	}
}
