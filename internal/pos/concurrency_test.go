package pos

import (
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// TestServiceConcurrentMutations is the ut-docs#449 regression test:
// pos.Service is shared by every request handler (one goroutine per request,
// net/http's normal model), so its exported methods must be safe under real
// concurrent access. This races several goroutines against ONE *Service,
// each hammering a different slice of the mutating/reading API, and relies
// on `go test -race` to flag any unsynchronized access.
//
// It also deliberately exercises every exported method that internally calls
// ANOTHER exported method's logic (Scan→scanQty, UpdateLine→Remove,
// UpdateLineByKey→RemoveLine, Restore→Reset/SetCustomer) so a reentrant
// double-lock would show up here as a hang, not slip through unexercised.
func TestServiceConcurrentMutations(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{
		"A": {SKU: "A", Name: "Item A", PriceCents: money.FromMinor(100)},
		"B": {SKU: "B", Name: "Item B", PriceCents: money.FromMinor(250)},
	})

	const iters = 1000
	var wg sync.WaitGroup
	worker := func(fn func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				fn(i)
			}
		}()
	}

	// Scan path (Scan → scanQty), plus the scan cache.
	worker(func(i int) {
		if _, err := s.Scan("A"); err != nil {
			t.Errorf("Scan: %v", err)
		}
		_ = s.HasScanCache("A")
		_, _ = s.ResolveBase("A")
	})

	// Qty scanning + order-type toggling.
	worker(func(i int) {
		if _, _, err := s.ScanQtyWithResult("B", 2); err != nil {
			t.Errorf("ScanQtyWithResult: %v", err)
		}
		s.SetOrderType(OrderTypeTakeaway)
		s.SetOrderType("")
	})

	// Modifier-line add + targeted remove by key.
	worker(func(i int) {
		base := BasketLine{SKU: "C", Name: "Item C", PriceCents: money.FromMinor(300)}
		s.AddLineWithModifiers(base, 1, nil)
		for _, l := range s.Lines() {
			if l.SKU == "C" {
				s.RemoveLine(l.LineKey)
				break
			}
		}
	})

	// UpdateLine with qty 0 (→ Remove) and UpdateLineByKey with qty 0
	// (→ RemoveLine): the two update→remove reentrant chains.
	worker(func(i int) {
		s.UpdateLine("A", 0, 0)
		if ls := s.Lines(); len(ls) > 0 {
			s.UpdateLineByKey(ls[0].LineKey, 0, 0)
		}
		s.Remove("B")
	})

	// Reads, discounts, customer, hold/restore (Restore → Reset/SetCustomer).
	worker(func(i int) {
		_ = s.Basket()
		s.SetDiscount(money.FromMinor(10))
		s.SetDiscountPercent(100)
		_ = s.SaleDiscount()
		s.SetCustomer("c1", "Jane Doe")
		s.SetCustomerID("c1")
		_ = s.CustomerID()
		_ = s.HasItems()
		_ = s.HasLine("A")
		snap := s.Snapshot()
		s.Restore(snap)
		if i%97 == 0 {
			s.Reset()
		}
	})

	// Config swaps (the LAN-sync drift loop does this live) + tender.
	worker(func(i int) {
		s.SetConfig(Config{TaxRateBasisPoints: 2000, TaxInclusive: i%2 == 0})
		_ = s.Config()
		_ = s.OrderType()
		_ = s.EffectiveLineTaxRateBP(BasketLine{TaxRateBP: 700})
		if _, err := s.Tender(money.FromMinor(100), "card"); err != nil {
			t.Errorf("Tender: %v", err)
		}
	})

	wg.Wait()
}
