package pos

import (
	"encoding/json"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

type holdResolver map[string]BasketLine

func (m holdResolver) Resolve(code string) (BasketLine, bool) {
	v, ok := m[code]
	return v, ok
}

func newHoldService() *Service {
	return NewServiceWithResolver(Config{TaxInclusive: true, TaxRateBasisPoints: 2000}, holdResolver{
		"A": {SKU: "A", Name: "Apples", PriceCents: money.FromMinor(150), ItemID: "itm-a", TaxRateBP: 2000},
		"B": {SKU: "B", Name: "Bread", PriceCents: money.FromMinor(120), ItemID: "itm-b", TaxRateBP: 2000, IsWeighed: true},
	})
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	s := newHoldService()
	_, _ = s.Scan("A")
	_, _ = s.ScanQty("B", 3)
	s.SetDiscountPercent(500) // 5%
	s.SetCustomer("cust-1", "Jo")
	want := s.Basket()

	snap := s.Snapshot()
	if len(snap.Lines) != 2 {
		t.Fatalf("snapshot lines = %d, want 2", len(snap.Lines))
	}
	if snap.Total != want.Total {
		t.Fatalf("snapshot total = %v, want %v", snap.Total, want.Total)
	}

	// Survive JSON persistence like the held_sales table does.
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var back BasketSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}

	// Serve another customer in between.
	s.Reset()
	_, _ = s.Scan("A")
	s.Reset()

	s.Restore(back)
	got := s.Basket()
	if len(got.Lines) != 2 {
		t.Fatalf("restored lines = %d, want 2", len(got.Lines))
	}
	if got.Total != want.Total || got.Subtotal != want.Subtotal || got.Discount != want.Discount {
		t.Fatalf("restored totals %v/%v/%v, want %v/%v/%v",
			got.Subtotal, got.Discount, got.Total, want.Subtotal, want.Discount, want.Total)
	}
	if got.CustomerName != "Jo" || got.CustomerID != "cust-1" {
		t.Fatalf("restored customer = %q/%q", got.CustomerID, got.CustomerName)
	}
	// Hidden fields must survive (they never hit the basket wire format).
	if !s.HasItems() {
		t.Fatal("expected items after restore")
	}
	lines := s.Lines()
	for _, l := range lines {
		if l.SKU == "B" && (!l.IsWeighed || l.ItemID != "itm-b" || l.TaxRateBP != 2000) {
			t.Fatalf("hidden fields lost on line B: %+v", l)
		}
	}
}

func TestHasItems(t *testing.T) {
	s := newHoldService()
	if s.HasItems() {
		t.Fatal("new service should be empty")
	}
	_, _ = s.Scan("A")
	if !s.HasItems() {
		t.Fatal("expected items after scan")
	}
	s.Reset()
	if s.HasItems() {
		t.Fatal("expected empty after reset")
	}
}
