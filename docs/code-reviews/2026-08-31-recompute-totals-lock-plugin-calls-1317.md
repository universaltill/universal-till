# Review: recomputeTotals holds the service-wide mutex across plugin calls (ut-docs#1317)

**Card:** universaltill/ut-docs#1317 (rescoped 2026-08-31 — split from a
5-finding perf audit bundle into this one concurrency-correctness fix; the
other four findings are ut-docs#1358/#1359/#1360/#1361).

**Branch:** `fix/1317-recompute-lock-plugin-calls` · **PR:** universal-till#TBD

## What shipped

`internal/pos/service.go`'s `recomputeTotals()` — called by every basket
mutator (`AddLine`, `RemoveLine`, `SetDiscount`, `Tender`, …) while the
caller holds `s.mu` — used to call straight into `taxAsker.AskTaxRateBP`
(once per line) and `chargeAsker.AskChargePolicy()` while still holding that
lock. Both askers are wasm-plugin-backed and, on a cache miss (a new
item/tax-code/order-type combination, or right after a plugin
install/settings-save/permission-grant bumps the plugin-bus generation), can
take ~100ms. Holding `s.mu` across that call stalled every OTHER concurrent
request against the same till (a second tab, a background sync read, a
status poll) for the duration.

The fix restructures `recomputeTotals` into an optimistic snapshot pattern:

1. Bump a new `recomputeGen uint64` counter and snapshot everything the
   computation needs (`totalsSnapshot`) while still holding `s.mu`.
2. Release `s.mu`, run the plugin-dependent computation (`computeTotals`,
   a pure function of the snapshot) unlocked.
3. Re-acquire `s.mu`; commit (`commitTotalsLocked`) only if `recomputeGen`
   still matches — otherwise another mutation raced in during the unlocked
   window, so re-snapshot and retry (bounded at 4 attempts).
4. If every attempt races (pathological contention), fall back to computing
   fully under the lock — today's pre-fix behavior — so the function always
   terminates with correct data.

`recomputeTotals`'s external contract — always called with `s.mu` held,
always returns with it held — is unchanged, so none of the ~15 existing call
sites needed to change. Every locked mutation of totals-relevant state that
does NOT itself call `recomputeTotals` (`Tender`, `resetLocked`,
`SetCustomerID`, `setCustomerLocked`) now bumps `recomputeGen` directly, so
an in-flight optimistic pass detects the race and refuses to commit stale
data over it.

## Independent review — two rounds

Both rounds ran as isolated-worktree Opus subagents (Dev was Fable, per the
`complexity:hard` model-routing rule) — a different model from the one that
wrote the code, with its own checkout so no revert/restore verification
could touch the shared working tree.

### Round 1 — 2 blocking findings

- **B1 (blocking):** the branch was 2 commits behind `main`, missing PR
  universal-till#674 (merged same day), which patched the SAME file
  (`mergeResolved` gained a `TaxCodeID` carry-through fix for a live VAT
  over-collection) and added a real-chain regression suite this card's own
  AC5 explicitly named. **Fixed:** merged `origin/main`
  (`b2c1fe9`, a clean 2-parent merge — verified in round 2 to be lossless on
  both sides, see below); the named regression suite passes on the merged
  tree.
- **B2 (blocking):** `TestRecomputeAbandonsStaleSnapshotOnReset` was a
  false-pass. It asserted via `s.Basket()`, called well after the race
  resolved — but `Basket()` itself calls `recomputeTotals()` fresh from
  (by-then) correct state, silently washing away any transient stale commit
  that happened in between. The reviewer proved this by replacing the
  staleness guard with `if true` and showing the suite still passed.
  **Fixed:** the test now asserts on `Scan()`'s own returned `*Basket`
  (captured at the exact moment that call's `recomputeTotals` returns,
  before any later call can mask it), and a new test
  `TestRecomputeAbandonsStaleSnapshotOnCustomerSet` isolates `recomputeGen`
  as independently load-bearing (it touches no `s.lines` at all, so the
  line-count half of the commit guard can't accidentally cover for it).

Six non-blocking notes also came out of round 1; the code-relevant ones are
folded into this commit's doc comments (below); the rest are recorded here.

### Round 2 — scoped re-verification of the B1/B2 fixes: safe to merge

- Verified the merge is genuinely lossless both directions (diffed
  `a067cb0→faef9c4` against `7508853→b2c1fe9`, and `a067cb0→7508853` against
  `faef9c4→b2c1fe9` — differ only in blob hashes/line offsets, zero content
  drift), and re-ran the named regression suite against the real wasm
  plugin chain.
- Re-proved B2's fix for real: `if true` in place of the guard fails BOTH
  new tests deterministically (3/3 runs); a finer probe — dropping only the
  gen half of the check while keeping the length half — shows the Reset
  test is *also* (coincidentally) caught by the length check, while the
  CustomerSet test is caught ONLY by `recomputeGen`, confirming the new
  test's own doc comment is accurate about which mechanism it isolates.
- Confirmed removing `commitTotalsLocked`'s truncating length guard (now an
  unconditional index, replaced by a caller-side hard invariant) introduces
  no new panic risk: all 3 call sites either hold the lock continuously
  since the snapshot (fast/fallback paths, equality by construction) or
  gate on an explicit `len` check (optimistic path).
- Re-ran the full gate fresh in an independent worktree: `gofmt`/`build`/
  `vet` clean, `go test ./... -count=1` 58 packages exit 0, `-race` on
  `internal/pos` 174 sub-tests/0 failures/0 races.
- Verified the merge doesn't interact badly with the snapshot/commit
  mechanism: `TaxCodeID` (PR #674's fix) is a plain `BasketLine` field
  carried whole through `totalsSnapshot`/`commitTotalsLocked`'s
  `s.basket.Lines = snap.lines` publish; the write-back loop touches only
  `LineTotal`, so it can't clobber or duplicate it — confirmed empirically
  under `-race -count=3` on both the straight-commit and forced-retry
  branches via a temporary probe (removed after).

## Verified beyond the automated suite

- **TDD claim re-proven independently, twice** (once per review round): the
  new tests fail with the exact claimed symptom pre-fix / with the guard
  disabled, and pass with it restored — shown as real command output both
  times, not taken on report alone.
- Benchmarked the actual hot path (a direct micro-benchmark of
  `recomputeTotals`, since `BenchmarkCompleteSale*` is SQLite-write-dominated
  and can't see this change): sub-microsecond-per-op regression (+13-14% on
  ~550-620ns baseline, +23%/+38B under 4-goroutine contention) traded for
  removing a ~100ms lock-hold on a cache miss. No allocation-count increase.
- Confirmed the fast (no-asker) path is dead in production —
  `internal/pages/init.go` installs both askers unconditionally on every
  engine — so every live basket mutation runs the optimistic path; this is
  now stated explicitly in `recomputeTotals`'s doc comment rather than left
  implicit.
- Confirmed the `ToastMessage`/`ToastLevel` "survives a recompute" doc
  comment's actual mechanism: both fields are written only on
  `basketCopyLocked`'s returned *copies* (`internal/pages/pos_api.go` and
  10+ other sites), never on `s.basket` directly, so `commitTotalsLocked`'s
  field-by-field write never had anything to clobber — comment corrected to
  say so precisely instead of the vaguer original framing.
- No real client/shop name, no literal secrets in the diff.
- Scope confirmed clean both rounds: only `internal/pos/service.go` and the
  new `internal/pos/recompute_lock_test.go` (plus the merge commit's
  unrelated files from `main`); `internal/pages/tax_hook.go`/`charge_hook.go`
  untouched, as the card specified.
- No file I/O in this diff — the two recurring bug classes this pipeline
  watches for (missing `os.MkdirAll`, a cwd-relative path instead of
  `paths.Data(...)`) don't apply.
- Not a UI-facing change — no help-manual topic or i18n key owed.

## Explicitly deferred (non-blocking, carried to follow-up)

- `EffectiveLineTaxRateBP` (a separate exported method, called per-line by
  the tender handler) still asks its tax asker while holding `s.mu` — same
  class of hazard, outside this card's literal scope. Worth a follow-up
  card.
- `Basket()` is a read-only accessor that still calls `recomputeTotals()`
  and therefore bumps `recomputeGen` on every call — safe (falls back to
  the always-correct locked path under contention) but under heavy
  concurrent status-polling could push recomputes onto the locked fallback
  more often than necessary. Mitigated today by the askers' own per-
  generation memoization keeping the common case fast. Worth a card if
  polling frequency ever rises.
- `maxAttempts = 4`'s specific value is asserted but not derived — a minor
  documentation gap, not a correctness concern.
- The exhausted-retry fallback path CAN run a plugin ask under the lock
  (the deliberate termination guarantee) — this is a real, intentional
  exception to the "never holds `s.mu` across a plugin ask" goal, scoped to
  pathological contention only, and is now called out explicitly in the
  doc comment rather than left to read as an unconditional guarantee.
- Exported mutators are no longer atomic end-to-end across the unlocked
  window (e.g. `Scan()` can be observed mid-call with the new line in
  `s.lines` but not yet in `s.basket`) — transient display staleness only,
  self-healing on the next recompute; the tender path was never atomic
  across its own separate lock acquisitions anyway. Documented on
  `Service.mu` itself.

## Verdict

**Safe to merge.** No blocking findings remain after round 2. Full gate
(gofmt/build/vet/`go test ./...`/`-race`/applicable CI guards) green in two
independent worktrees.
