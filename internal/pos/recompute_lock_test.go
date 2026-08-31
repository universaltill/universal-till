package pos

// ut-docs#1317: recomputeTotals calls the plugin-backed TaxRateAsker /
// ChargePolicyAsker hooks, which on a cache miss make a real wasm call that
// can take ~100ms. The pre-fix code did that while holding Service.mu, so
// every OTHER concurrent request against the same *Service (a second browser
// tab, a status poll) stalled behind the plugin. These tests pin the fixed
// contract: the slow ask happens OUTSIDE the service lock, and an optimistic
// recompute that raced with a concurrent mutation must never commit its
// stale snapshot.

import (
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

// blockingTaxAsker stands in for a wasm tax plugin on a cache miss: every
// AskTaxRateBP call signals `entered` (so the test knows the ask is in
// flight) and then blocks until `release` is closed. After release it
// answers instantly, like the real askers' memoized cache hits.
type blockingTaxAsker struct {
	entered chan struct{} // buffered; receives one token per ask that starts
	release chan struct{} // closed to let every ask (current and future) proceed
}

func (a *blockingTaxAsker) AskTaxRateBP(l BasketLine, orderType string) (int, bool, bool) {
	select {
	case a.entered <- struct{}{}:
	default:
	}
	<-a.release
	return 700, true, false
}

// TestRecomputeDoesNotHoldLockDuringAsk is the ut-docs#1317 regression test:
// while recomputeTotals is waiting on a slow plugin ask, an unrelated call
// on the same *Service (here Lines(), which takes s.mu but never recomputes)
// must complete immediately instead of stalling behind the plugin. Against
// the pre-fix code Lines() blocks until the asker returns, and this test
// fails on the 2s watchdog.
func TestRecomputeDoesNotHoldLockDuringAsk(t *testing.T) {
	asker := &blockingTaxAsker{
		entered: make(chan struct{}, 16),
		release: make(chan struct{}),
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{
		"A": {SKU: "A", Name: "Item A", Qty: 1, PriceCents: money.FromMinor(100)},
	})
	// Installing the asker recomputes, but with no lines yet the per-line
	// ask loop never runs, so this cannot block.
	s.SetTaxRateAsker(asker)

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		if _, err := s.Scan("A"); err != nil {
			t.Errorf("Scan: %v", err)
		}
	}()

	// Wait until the slow ask is genuinely in flight.
	select {
	case <-asker.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("asker was never called")
	}

	// The unrelated calls: each takes s.mu but never triggers a recompute.
	// They must not be serialized behind the in-flight plugin ask.
	otherDone := make(chan struct{})
	go func() {
		defer close(otherDone)
		_ = s.Lines()
		_ = s.OrderType()
		_ = s.HasItems()
	}()
	select {
	case <-otherDone:
		// s.mu was free during the ask — the fixed behavior.
	case <-time.After(2 * time.Second):
		t.Error("Lines()/OrderType()/HasItems() stalled behind an in-flight plugin ask: s.mu is held during AskTaxRateBP")
	}

	close(asker.release)
	<-scanDone
	<-otherDone

	// The recompute must still land correct totals once the ask resolves:
	// 100 minor units at the asker's 700bp, tax-exclusive.
	b := s.Basket()
	if len(b.Lines) != 1 || b.Subtotal != money.FromMinor(100) {
		t.Fatalf("basket after ask resolved: lines=%d subtotal=%d", len(b.Lines), b.Subtotal.Minor())
	}
	if b.Tax != money.FromMinor(7) {
		t.Fatalf("expected asker's 700bp tax (7) applied, got %d", b.Tax.Minor())
	}
}

// TestRecomputeAbandonsStaleSnapshotOnReset pins the staleness rule for the
// optimistic (unlocked-ask) recompute: a mutation that lands during the
// unlocked window — here Reset(), which clears the basket WITHOUT triggering
// its own recompute — must not be overwritten by the in-flight recompute's
// stale snapshot. If the generation check were missing, the finished ask
// would commit the pre-Reset line back into the basket.
//
// Assert on Scan()'s OWN returned *Basket, not a later s.Basket() call:
// s.Basket() calls recomputeTotals() again from (by-then) empty state, which
// would recompute a correct empty basket and mask a stale commit that
// happened and was overwritten in between — a false pass that let a real
// bug in this exact path through this test once. Scan()'s return is a copy
// taken under the same lock, immediately after ITS OWN recomputeTotals call
// returns, so it shows exactly what that call committed.
func TestRecomputeAbandonsStaleSnapshotOnReset(t *testing.T) {
	asker := &blockingTaxAsker{
		entered: make(chan struct{}, 16),
		release: make(chan struct{}),
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{
		"A": {SKU: "A", Name: "Item A", Qty: 1, PriceCents: money.FromMinor(100)},
	})
	s.SetTaxRateAsker(asker)

	var scanResult *Basket
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanResult, _ = s.Scan("A")
	}()
	select {
	case <-asker.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("asker was never called")
	}

	// Race a Reset into the (post-fix) unlocked ask window. Pre-fix this
	// simply queues on s.mu and runs after the scan commits, which also
	// yields an empty basket — the assertion is about the FIXED code never
	// resurrecting the cleared sale via a stale optimistic commit.
	resetDone := make(chan struct{})
	go func() {
		defer close(resetDone)
		s.Reset()
	}()
	select {
	case <-resetDone:
	case <-time.After(2 * time.Second):
		// Pre-fix code: Reset queues behind the ask; unblock and let it run.
	}

	close(asker.release)
	<-scanDone
	<-resetDone

	if scanResult == nil {
		t.Fatal("Scan returned a nil basket")
	}
	if len(scanResult.Lines) != 0 || scanResult.Total != 0 {
		t.Fatalf("stale recompute resurrected a Reset basket via Scan's own return: lines=%d total=%d", len(scanResult.Lines), scanResult.Total.Minor())
	}

	// Belt and braces: current state is consistent too.
	b := s.Basket()
	if len(b.Lines) != 0 || b.Total != 0 {
		t.Fatalf("basket not empty after Reset: lines=%d total=%d", len(b.Lines), b.Total.Minor())
	}
}

// TestRecomputeAbandonsStaleSnapshotOnCustomerSet pins the same staleness
// rule for a mutation that changes NO line count — Reset's own test is
// (harmlessly) also caught by commitTotalsLocked's length-mismatch guard,
// which doesn't fire here, so this is the test that actually isolates
// recomputeGen as the load-bearing mechanism: SetCustomerID writes directly
// into s.basket.CustomerID without touching s.lines at all. If its
// recomputeGen bump were missing, the in-flight recompute's stale snapshot
// (customerID from before the set) would silently overwrite it.
func TestRecomputeAbandonsStaleSnapshotOnCustomerSet(t *testing.T) {
	asker := &blockingTaxAsker{
		entered: make(chan struct{}, 16),
		release: make(chan struct{}),
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{
		"A": {SKU: "A", Name: "Item A", Qty: 1, PriceCents: money.FromMinor(100)},
	})
	s.SetTaxRateAsker(asker)

	var scanResult *Basket
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanResult, _ = s.Scan("A")
	}()
	select {
	case <-asker.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("asker was never called")
	}

	setDone := make(chan struct{})
	go func() {
		defer close(setDone)
		s.SetCustomerID("cust-99")
	}()
	select {
	case <-setDone:
	case <-time.After(2 * time.Second):
		t.Fatal("SetCustomerID stalled behind the in-flight ask")
	}

	close(asker.release)
	<-scanDone

	if scanResult == nil {
		t.Fatal("Scan returned a nil basket")
	}
	if scanResult.CustomerID != "cust-99" {
		t.Fatalf("stale recompute clobbered the customer set during the ask window: got %q, want %q", scanResult.CustomerID, "cust-99")
	}
}

// TestRecomputeConcurrentMutationsWithSlowAsker extends ut-docs#449's race
// coverage to the new optimistic recompute path: many goroutines hammer the
// service while every tax ask goes through the (released, thus fast) asker,
// so the unlock/re-ask/re-lock retry machinery runs constantly under -race.
func TestRecomputeConcurrentMutationsWithSlowAsker(t *testing.T) {
	asker := &blockingTaxAsker{
		entered: make(chan struct{}, 16),
		release: make(chan struct{}),
	}
	close(asker.release) // answer instantly, but still via the asker path
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{
		"A": {SKU: "A", Name: "Item A", Qty: 1, PriceCents: money.FromMinor(100)},
		"B": {SKU: "B", Name: "Item B", Qty: 1, PriceCents: money.FromMinor(250)},
	})
	s.SetTaxRateAsker(asker)

	const iters = 500
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
	worker(func(i int) {
		_, _ = s.Scan("A")
		_ = s.Lines()
	})
	worker(func(i int) {
		_, _ = s.ScanQty("B", 2)
		s.SetOrderType(OrderTypeTakeaway)
		s.SetOrderType("")
	})
	worker(func(i int) {
		_ = s.Basket()
		s.SetDiscount(money.FromMinor(10))
		s.SetCustomer("c1", "Jane Doe")
		if i%97 == 0 {
			s.Reset()
		}
	})
	worker(func(i int) {
		s.Remove("A")
		s.UpdateLine("B", 0, 0)
		snap := s.Snapshot()
		s.Restore(snap)
	})
	wg.Wait()
}
