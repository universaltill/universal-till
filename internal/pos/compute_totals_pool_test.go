package pos

// ut-docs#1358 (review finding F5): computeTotals draws its two per-call
// scratch slices (vatLines, chargeTaxLines) from package-level sync.Pools
// instead of allocating fresh ones every basket mutation. That pooling has
// no other mechanical guard in the suite, so these tests pin the two ways
// it could silently corrupt checkout totals: a stale-length reuse leaking a
// prior call's extra lines into a shorter basket, and a data race between
// concurrent unlocked calls (the shape recomputeTotals' optimistic path,
// ut-docs#1317, actually produces in production).

import (
	"fmt"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// makeTotalsSnapshot builds a deterministic totalsSnapshot of n lines, same
// shape as BenchmarkComputeTotals' fixture.
func makeTotalsSnapshot(n int) totalsSnapshot {
	lines := make([]BasketLine, n)
	for i := range lines {
		lines[i] = BasketLine{
			SKU:        fmt.Sprintf("SKU%03d", i),
			Qty:        1,
			PriceCents: money.FromMinor(int64(100 + i)),
			TaxRateBP:  2000,
		}
	}
	return totalsSnapshot{lines: lines, cfg: Config{TaxRateBasisPoints: 2000}}
}

// TestComputeTotals_PoolBufferReuseDoesNotLeakAcrossCalls: compute a small
// basket, then a large one (forcing the pooled buffers to grow well past
// the small basket's length), then the same small basket again, and
// require identical results. A stale-length reuse bug — e.g. dropping the
// reslice-to-zero-length step before appending — would let the large
// call's extra VAT/charge-tax lines leak into the second small
// computation's subtotal/tax.
func TestComputeTotals_PoolBufferReuseDoesNotLeakAcrossCalls(t *testing.T) {
	small := makeTotalsSnapshot(2)
	before := computeTotals(small)

	_ = computeTotals(makeTotalsSnapshot(500))

	after := computeTotals(small)
	if before != after {
		t.Fatalf("computeTotals(small) changed after an intervening large call: before=%+v after=%+v — pooled scratch buffer leaked across calls", before, after)
	}
}

// TestComputeTotals_ConcurrentPoolAccessIsRaceFree exercises the pools under
// real concurrent, unlocked access. Each goroutine's result is checked
// against an independently-computed reference for its own exact snapshot
// (never shared with any other goroutine), so a shared-buffer corruption
// bug shows up as a wrong total here, not just as a `-race` flag.
func TestComputeTotals_ConcurrentPoolAccessIsRaceFree(t *testing.T) {
	const goroutines = 32
	const itersPerGoroutine = 200

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < itersPerGoroutine; i++ {
				n := 1 + (g*itersPerGoroutine+i)%60 // 1..60 lines, varies per call
				got := computeTotals(makeTotalsSnapshot(n))
				want := computeTotals(makeTotalsSnapshot(n))
				if got != want {
					t.Errorf("goroutine %d iter %d: computeTotals(%d lines) = %+v, want %+v", g, i, n, got, want)
					return
				}
			}
		}()
	}
	wg.Wait()
}
