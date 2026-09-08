# Code review: basket recompute fused into a single pass (ut-docs#1358)

**Date:** 2026-09-08
**Card:** ut-docs#1358 (`complexity:medium`)
**Diff:** `internal/pos/service.go`, `internal/pos/performance_test.go`,
`internal/pos/compute_totals_pool_test.go`

## What shipped

Split from the 2026-08-30 principal-engineer performance audit
(`docs/code-reviews/2026-08-30-performance-audit.md`, section A finding
1): `computeTotals` (`internal/pos/service.go`) walked the whole basket
line slice twice per recompute — once to derive each line's net/subtotal,
once to derive each line's tax rate/VAT line — and allocated two fresh
scratch slices (`vatLines`, `chargeTaxLines`) on every single call. This
runs on every basket mutation, so a cashier building an N-line basket one
tap at a time pays N recomputes, each walking the *entire* basket built
so far.

The AC offered two options: a genuinely incremental (per-mutation delta)
recompute, or "at minimum, reuse pre-sized buffers instead of
reallocating three slices per call." A true incremental redesign was
judged too risky for this card's tier — `VATBandsForSale` prorates a
whole-basket discount across per-rate bands by gross share, which is not
line-local, so making that incremental correctly would touch the same
tax-correctness surface as a much larger, `hard`-tier change. This change
takes the safer option:

1. **Fused the two loops into one.** The tax/VAT-line derivation only
   reads `l.LineTotal` (set earlier in the same iteration) and `snap.cfg`
   — never the whole-basket `discount`, which is computed from `sub`
   *after* the original second loop ran. Nothing in either original loop
   depended on the other's output, so they merge into a single pass
   safely.
2. **Pooled the two scratch slices** via `sync.Pool` instead of
   allocating them fresh every call. Safe under `recomputeTotals`'
   optimistic unlocked path (ut-docs#1317, where several goroutines can
   be inside `computeTotals` concurrently with `s.mu` released) because
   each call `Get`s its own exclusive buffer and only `Put`s it back
   after `VATBandsForSale`/`ServiceChargeTax` are done reading from it —
   neither retains the slice past the call.

## Independent review (Opus subagent, per `complexity:medium` routing)

**Verdict: NEEDS FIXES (test-only) → all fixed.** The reviewer confirmed
the production change itself is correct, race-free, and panic-safe, but
found the benchmark evidence shipped alongside it was broken. Full
findings and everything the reviewer verified independently are recorded
below; nothing here is asserted on trust.

### F1 (fixed) — `BenchmarkBuildBasketFromScratch`'s fixture collapsed every basket to 1 line, not N

The original fixture set only `SKU` on each resolver line, leaving
`ItemID`/`VariantID` at their zero value. `mergeResolved`'s merge test is
`SKU match OR (ItemID match AND VariantID match)` — with every line's
`ItemID`/`VariantID` equal to `""`, the second clause was trivially true
for *every* pair regardless of SKU, so every `ScanQty` call merged into
line 0. The benchmark silently measured a 1-line basket's cost N times
over, and the old vs. new numbers were statistically indistinguishable
(362/714 allocs/op identical either way) — exactly what you'd expect from
a linear, not quadratic, workload, and the opposite of what the
benchmark's own doc comment claimed to be watching for.

Fix: gave each fixture line a distinct `ItemID`, and added a one-time
setup assertion (`len(svc.Lines()) == n`, outside the timed loop) so the
fixture can never again silently degenerate without the benchmark itself
failing loudly.

### F2 (fixed) — commit-message benchmark deltas weren't reproducible, and the isolated benchmark's no-asker caveat wasn't documented

The original commit quoted numbers from a single unreproduced run.
Re-measured on this box after the F1 fix, old vs. new, `-benchtime=200x`:

`BenchmarkComputeTotals` (isolated, no `taxAsker`/`chargeAsker` —
exercises `recomputeTotals`' fast/never-unlocked path only):

| lines | before | after |
|---|---|---|
| 10  | 617.3 ns/op, 488 B/op, 5 allocs | 661.0 ns/op, 93 B/op, 3 allocs |
| 50  | 2194 ns/op, 2264 B/op, 5 allocs | 1282 ns/op, 116 B/op, 3 allocs (−42% time, −95% bytes) |
| 100 | 4409 ns/op, 4568 B/op, 5 allocs | 2532 ns/op, 141 B/op, 3 allocs (−43% time, −97% bytes) |

`BenchmarkBuildBasketFromScratch` (real end-to-end, N genuinely distinct
lines after the F1 fix):

| lines | before | after |
|---|---|---|
| 50  | 313549 ns/op, 435093 B/op, 563 allocs | 246503 ns/op, 382552 B/op, 468 allocs (−21% time, −12% bytes, −17% allocs) |
| 100 | 983176 ns/op, 1585782 B/op, 1118 allocs | 802643 ns/op, 1375856 B/op, 926 allocs (−18% time, −13% bytes, −17% allocs) |

Added a doc-comment caveat on `BenchmarkComputeTotals`: production
always installs both askers (`internal/pages/init.go`), so production
always runs the optimistic unlocked path, where a cache-miss plugin ask
(~100ms) dwarfs anything this isolated benchmark measures. The saving is
real on a cache hit; it says nothing about the asker-installed cost.

**Honesty note carried forward, not hidden:** the aggregate
`BenchmarkBuildBasketFromScratch` numbers still grow worse than linearly
either side of this fix (100-line time is ~3.1–3.3× the 50-line time,
both before and after) — this change reduces the *constant factor* of
each recompute call, it does not remove the underlying "N mutations each
walk the whole current basket" shape. That's the AC's "at minimum"
option, deliberately chosen over a full incremental redesign for the
reason given above; a true fix for the aggregate growth is out of scope
here and would need its own `hard`-tier card if it's ever worth doing.

### F3 (noted, not fixed — informational) — the change optimizes the smaller of two allocations

`snapshotForTotalsLocked`'s `append([]BasketLine{}, s.lines...)` copies
the whole line slice on every recompute (required — it's what makes the
unlocked window in `recomputeTotals` safe) and is unchanged by this
diff; at 100 lines it's ~23 KB against the ~4 KB this fix removes. Left
as-is: the copy is correctness-load-bearing and a different, larger
change to touch. Recorded here so nobody mistakes this path as fully
optimized.

### F4 (not applied — accepted as-is) — no cap on returning an oversized buffer to the pool

A one-off pathological basket (e.g. a large stock-take rung through as
one sale) parks its oversized backing array in the pool. `sync.Pool` is
GC-drained (at most a two-cycle victim-cache delay), so this is bounded,
not a leak; the reviewer flagged it as optional stdlib-convention
polish, not a defect. Not applied, to keep this change minimal.

### F5 (fixed — added permanent regression coverage)

The reviewer ran an ad hoc differential test (20,000 randomized
snapshots against a reference copy of the pre-fix `computeTotals`), a
32-goroutine concurrent-access test under `-race`, and manual mutation
tests (dropping the `[:0]` reslice) to prove the pool's stale-length and
concurrency contracts hold today — but none of that was committed, so
the contract had no lasting mechanical guard. Added
`internal/pos/compute_totals_pool_test.go`:

- `TestComputeTotals_PoolBufferReuseDoesNotLeakAcrossCalls` — computes a
  small basket, then a large one (forcing the pooled buffers to grow),
  then the small one again, and requires identical results.
- `TestComputeTotals_ConcurrentPoolAccessIsRaceFree` — 32 goroutines ×
  200 calls at varying basket sizes, each checked against an
  independently-built reference snapshot; run under `-race`.

Both pass. `go build ./... && go vet ./... && golangci-lint run
./internal/pos/... && go test ./internal/pos/... -race -count=1` all
clean after the fixes.

## What was verified beyond automated tests

- **Loop fusion correctness**, verified by the independent reviewer via a
  20,000-case differential property test against the pre-fix
  implementation (varying line counts, negative/over-discount amounts,
  both pricing modes, per-line order-type mixing, and asker-driven tax
  rate/charge-policy overrides) — every case produced identical
  `computedTotals` and identical per-line `LineTotal`.
- **No retention of pooled slices past the call**, confirmed by reading
  `VATBandsForSale` (`internal/pos/vat_breakdown.go`) and
  `ServiceChargeTax`/`ApportionServiceChargeTax`
  (`internal/pos/service_charge_tax.go`) — both only aggregate from their
  input slice into a freshly allocated result.
- **Concurrency safety under the ut-docs#1317 unlocked path**, tested by
  the reviewer under `-race` with 32 goroutines and forced pool growth
  across mixed basket sizes; independently re-verified here via the two
  tests added under F5.
- **Panic safety** — a `TaxRateAsker` that panics mid-loop still leaves
  the pool in a safe state (the deferred `Put` returns a pre-growth
  buffer; the write-back-through-pointer that retains append growth is
  skipped), and the existing `recomputeTotals` lock-reacquire-on-panic
  contract (ut-docs#1317) is untouched by this diff.
- **TDD/mutation claim**, re-verified independently by the reviewer:
  dropping the `chargeTaxLines` append fails
  `TestService_BasketPreviewIncludesServiceChargeTax` and
  `TestService_ChargePolicyAnswerDrivesPreview` with the exact wrong-tax
  message; reverting restores a clean `git status`.
- **Full repo gate**: `gofmt -l .` empty, `go build ./...`,
  `go vet ./...`, full `go test ./...` (all packages), `go test
  ./internal/pos/... -race -count=1`, `golangci-lint run ./...` (0
  issues), `bash scripts/ci/guard-data-access.sh` — all clean. No SQL
  introduced, no new user-facing strings (backend-only Go, no i18n keys
  needed), no ADR conflict, `internal/money.Money` used correctly
  throughout (constructed via `money.FromMinor` at the benchmark/test
  boundary, basis-point rates stay `int`).

## Non-goals confirmed still out of scope

- A truly incremental (O(1) per mutation) basket recompute — deliberately
  deferred; would need to redesign `VATBandsForSale`'s whole-basket
  discount proration to be maintainable incrementally, which is a
  `hard`-tier, likely ADR-worthy change on its own (see ut-docs#1368/#1369,
  the sibling perf-audit cards already scoped as `hard` for the same
  reason on the sync-protocol side).
- `snapshotForTotalsLocked`'s per-call `BasketLine` copy (F3) — left
  untouched; it is the larger of the two allocations removed from a
  future pass, but changing it touches the unlocked-window safety
  contract `recomputeTotals` depends on, not just a scratch-buffer
  lifetime.
