# Code review: harden `LowStockItem.IsRunningOut` / extract `DaysLeftAt`

**Ticket:** universaltill/ut-docs#440 (follow-up from the independent review of
universaltill/ut-docs#275, `docs/code-reviews/2026-08-08-low-stock-running-out-unify-275.md`)
**Date:** 2026-08-08
**Repo/branch:** `universal-till` / `fix/low-stock-running-out-hardening-440`
**Reviewer:** independent Sonnet subagent (complexity:easy tier — fresh-context
Sonnet is the review model for `easy`), isolated worktree

## What shipped

Four non-blocking gaps deferred by #275's own review, none of them blockers, all
closed here:

1. **Duplicated floor expression.** `internal/pages/inventory_page.go`'s
   displayed "days left" number and `internal/data.LowStockItem.IsRunningOut`'s
   boolean both independently computed `int(CurrentQty/rate)`. Extracted
   `func (l LowStockItem) DaysLeftAt(rate float64) int` (`internal/data/pos_repo.go`,
   next to `IsRunningOut`); both call sites now call it, so the two can't
   silently desync if the formula changes later (e.g. `math.Ceil`, a safety
   margin).
2. **Lead-time floor-divergence boundary now pinned.** Added the qty=21 case
   (`floor(10.5)=10 <= 10` → warns) alongside the pre-existing qty=22 case
   (`floor(11)=11 > 10` → doesn't) — the actual raw/floor divergence point for
   a configured lead time, not just "past the window."
3. **Negative rate and guard ordering now asserted.** Added an explicit
   negative-rate case, and a case that pins the rate-check-before-qty<=0
   ordering (`qty=0, rate=-1` → `false`, proving the rate guard short-circuits
   before the qty<=0 "always warns" branch would otherwise fire).
4. **NaN/out-of-range rate guarded.** `DaysLeftAt` guards its own
   float64→int conversion (`math.IsNaN(days) || days > float64(math.MaxInt)`
   → clamp to `math.MaxInt`, i.e. "never running out at this rate," the same
   direction a raw-float compare would also land on). `IsRunningOut` also
   gained an explicit `math.IsNaN(rate)` check alongside its existing
   `rate <= 0` guard — NaN fails a plain `<= 0` comparison in Go, so the
   original guard didn't catch it.

### Tests (written test-first, TDD)

`internal/data/pos_repo_low_stock_running_out_test.go` — extended
`TestLowStockItem_IsRunningOut` with the new boundary/negative/guard-ordering/
NaN cases, added `TestLowStockItem_DaysLeftAt` (direct coverage of the new
method, including the NaN and overflow-clamp cases), and added
`TestLowStockItem_DaysLeftAtDrivesIsRunningOut` (a structural/property check
that the display path and the boolean path can't disagree for any positive
qty/rate — the specific desync the ticket flagged as previously untested).

`internal/pages/inventory_page.go`'s one call site (`row.DaysLeft = int(l.CurrentQty / rate)`)
now reads `row.DaysLeft = l.DaysLeftAt(rate)` — no behavior change for real
inputs (see below).

## Independent review (round 1)

An independent Sonnet subagent, isolated in its own git worktree, reviewed the
diff without having seen any prior reasoning about it:

- Ran `go build ./...`, `go vet ./...`,
  `go test ./internal/data/... ./internal/pages/... ./internal/alerts/... -v -run 'LowStock|IsRunningOut|DaysLeft'`
  (all pass, including all `IsRunningOut`/`DaysLeftAt`/`DaysLeftAtDrivesIsRunningOut`
  subtests), the unfiltered package tests for the same three packages (pass),
  and `guard-data-access.sh` (clean).
- **Independently re-verified the TDD claim**: reverted just
  `internal/data/pos_repo.go` to its pre-fix parent commit, confirmed both
  `go build ./...` (compile failure — `inventory_page.go` calling
  `l.DaysLeftAt` that no longer exists) and the new test file failed to
  compile, then restored and confirmed clean build + green tests again.
- **Independently probed whether the two new guard-ordering/NaN tests are
  actually load-bearing**, not incidentally passing:
  - Swapped the `qty<=0`/`rate<=0` check order in `IsRunningOut` — the
    guard-ordering test failed as expected (`IsRunningOut(qty=0, rate=-1)`
    flipped from `false` to `true`), confirming it's genuinely pinned to the
    real ordering.
  - Removed the `IsRunningOut`-level NaN guard while leaving `DaysLeftAt`'s
    own NaN handling in place: the plain `NaN rate never warns` case (qty=5,
    i.e. qty>0) **still passed** — fully subsumed by `DaysLeftAt`'s own
    guard, since `DaysLeftAt(NaN)` already clamps to `math.MaxInt`. Only the
    `qty<=0`-combined-with-NaN case failed without the explicit guard
    (`IsRunningOut(qty=0, rate=NaN)` flipped to `true`), because the
    `qty<=0` branch returns before `DaysLeftAt` is ever called. This
    confirms the guard is load-bearing specifically for the qty<=0 case, and
    that it's the second NaN test — not the first — doing the real work.
    Correct behavior either way; no bug, just useful to know which test
    matters.
- **Independently recomputed both new boundary cases** rather than trusting
  the comments: the qty=21 lead-time case (`21/2=10.5`, `floor=10`,
  `10<=10` → warns, genuinely different from qty=22's `11<=10` → doesn't,
  where both raw and floor compares already agreed) and the overflow-clamp
  case (`1.0/1e-300 = 1e+300 > math.MaxInt` → clamp triggers). Also directly
  probed this platform's actual (implementation-defined) `int(NaN)`/
  `int(1e300)` behavior: both convert to `math.MinInt64`, which would satisfy
  `<= EffectiveWarnDays()` and produce a false-positive "running out" without
  the guard — independently confirming the ticket's "returns true where a
  raw compare would say false" claim, not just taking it on faith.
- Confirmed `math.MaxInt` is available (`go.mod` pins `go 1.25.0`).
- Checked the standing rules: no SQL outside `internal/data` (guard green,
  cross-checked with a manual grep for SQL verbs — zero hits), no
  `money.Money`/money math touched, no user-facing string/route/UI changed
  (no locale key or `web/help/` manual topic owed), no disk I/O at all (so
  neither of the two recurring bug classes — missing `os.MkdirAll`, a
  cwd-relative path instead of `paths.Data(...)` — applies), no real
  client/shop name or secret-shaped literal introduced. Confirmed
  `alerts.go`/`reports_page.go` correctly untouched — they already call the
  shared `IsRunningOut` from #275 rather than duplicating the floor
  expression, so gap #1's fix is correctly scoped to `inventory_page.go`
  only.
- Raised, and then set aside as not a finding: whether clamping an
  invalid/absurd rate to "never running out" is the right direction versus
  "fail loud." Concluded it's the same conservative policy the codebase
  already uses for `rate <= 0` (added in #275), the path is unreachable
  through any of the three real call sites today (`ItemDailySellRates`
  only ever produces positive, finite rates from a `qty > 0`-filtered
  query), and the opposite direction would just be a different, equally
  defensible policy — not a bug.

**Verdict: no blocking findings.**

### Non-blocking finding — fixed inline

- `gofmt -l` flagged `internal/data/pos_repo_low_stock_running_out_test.go`:
  two new table rows had trailing-comment columns misaligned by one space.
  Fixed with `gofmt -w`; re-ran the full targeted gate afterward (build, vet,
  the three touched packages' tests, `guard-data-access.sh`, and `gofmt -l`
  on all three changed files) — all clean.

## Verified beyond automated tests

- Full `go test ./... -race` run once, after implementation was finished:
  every package green except `internal/issuereport`'s
  `TestSaveCleansUpDirectoryOnWriteFailure`, which is the same pre-existing,
  already-tracked, unrelated failure #275's own review documented (sandbox
  runs tests as uid 0, so the "read-only bundle directory" the test expects
  to fail against is still writable by root) — universaltill/ut-docs#258 /
  #415, not a regression here, reproduces identically on `main`.
- `gofmt -l` clean on all three changed files after the fix above.
- No visible surface touched (pure `internal/data` logic plus a
  byte-identical call-site swap in `internal/pages`) — no screenshot/visual
  check applicable; noted explicitly rather than silently skipped.

## Safe to merge

Yes. Build, vet, the full non-flaky test suite, `guard-data-access.sh`, and
an independent adversarial re-verification (revert-then-restore TDD check,
guard-order and NaN-guard mutation probing, independent boundary
recomputation, platform-level `int(NaN)`/`int(1e300)` probing) all pass or
confirm the diff's own claims. The one real defect found (`gofmt`
misalignment) was fixed and re-verified. No ADR needed — same-package,
non-architectural, behavior-preserving-for-real-inputs hardening, same class
as the ticket it follows up on.

## CI finding, post-review — `guard-docs-shots.sh`

The first CI push failed a check neither Tester nor Reviewer ran locally:
`guard-docs-shots.sh` hashes the *whole* `internal/pages/**.go` surface (non-
test files), not a semantic diff, so the `inventory_page.go` call-site swap
tripped it even though it changes no rendered output. Fixed by actually
running `make docs-shots` (the real Playwright harness against a live till,
not a hand-edited manifest) and committing the refreshed screenshots +
`manifest.json`.

Six of the sixty regenerated screenshots (alerts/designer in all 4 locales,
plus `tr/sell`) came out with real pixel diffs unrelated to this PR's code:
`alerts` bakes a live "Recent problems" log timestamp into the page, and the
receipt `designer` preview bakes today's date into the receipt mock — both
already tracked as universaltill/ut-docs#360 (pin the designer preview's
time) and universaltill/ut-docs#370 (root-cause the generation-environment
sensitivity this guard has). Visually inspected each before committing —
correct rendering, no missing fonts/truncation/layout breakage, the only
difference is the expected live timestamp/seed content. Re-ran
`guard-docs-shots.sh` after committing: clean.
