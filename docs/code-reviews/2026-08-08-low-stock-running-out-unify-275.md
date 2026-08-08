# Code review: unify the low-stock "running out" comparison

**Ticket:** universaltill/ut-docs#275
**Date:** 2026-08-08
**Repo/branch:** `universal-till` / `fix/low-stock-running-out-unify-275`
**Reviewer:** independent Opus subagent (complexity:medium tier), isolated worktree

## What shipped

Three call sites each independently decided "is this item running out"
given a sell rate, and could disagree at an exact boundary:

- `internal/pages/inventory_page.go`'s `stockLevelsForDisplay` (the
  `/inventory` page) floored the days-left prediction before comparing:
  `DaysLeft = int(qty/rate)`; `RunsOut = DaysLeft <= warnDays`, with
  `qty <= 0` forcing `RunsOut = true`.
- `internal/alerts/alerts.go`'s `runningOutCount` (the daily low-stock
  digest) and `internal/pages/reports_page.go`'s `/reports` header chip
  both compared the raw float directly: `qty/rate <= float64(warnDays)`.

At `qty/rate = 7.5` against a 7-day warn window, `/inventory` warned
(`floor(7.5)=7 <= 7`) while the digest and reports chip did not
(`7.5 <= 7` is false) — the same item, three different verdicts,
depending only on which surface the owner happened to look at. All three
already shared `data.LowStockItem.EffectiveWarnDays()` (added by the
earlier ut-docs#85 fix), just not the comparison built on top of it.

- Added `data.LowStockItem.IsRunningOut(rate float64) bool`
  (`internal/data/pos_repo.go`, next to `EffectiveWarnDays`) as the single
  shared decision. Floor-then-compare was chosen as the standardized
  behavior: it reproduces `/inventory`'s exact per-item logic (byte-
  identical for the common case, the primary/most-visited surface), and
  it is the more conservative of the two — it never warns *later* than a
  raw-float compare would, only ever narrows the disagreement window.
- `internal/pages/inventory_page.go`, `internal/alerts/alerts.go`,
  `internal/pages/reports_page.go` all now call `IsRunningOut` instead of
  each duplicating the comparison.

### Tests (written test-first, TDD)

- `TestLowStockItem_IsRunningOut`
  (`internal/data/pos_repo_low_stock_running_out_test.go`) — table-driven
  boundary cases: no rate, zero/negative stock, the exact
  `qty/rate = 7.5` divergence case from the ticket, the exact integer
  boundary, and a lead-time-aware pair.

## Independent review (round 1)

An independent Opus subagent, isolated in its own git worktree, reviewed
the diff without having seen any prior reasoning about it:

- Ran `go build ./...`, `go vet ./...`,
  `go test ./internal/data/... ./internal/alerts/... ./internal/pages/...`
  (all pass), `go test ./internal/data/ -run TestLowStockItem_IsRunningOut -v`
  (8/8 subtests pass), the full `go test ./...` (one pre-existing,
  unrelated failure — see below), and `guard-data-access.sh` (clean).
- **Independently re-verified the TDD claim**: deleted the
  `IsRunningOut` method body in its worktree, confirmed the build/test
  failed (compile-time — `l.IsRunningOut undefined` — the diff removes
  the duplicated inline comparisons, so the three call sites no longer
  compile without the method, rather than the test merely failing an
  assertion), restored via `git checkout dbd8a24 -- internal/data/pos_repo.go`,
  confirmed clean build and passing test again.
- **Verified the two headline correctness claims by exhaustive
  brute-force comparison**, not by reading: wrote a standalone harness
  reimplementing all three original predicates plus the new method and
  ran it over 338,604 `(qty, rate, leadTime)` combinations. Result:
  0 mismatches between the new method and `/inventory`'s prior behavior
  (confirms "byte-identical for the common case"); 0 cases where the old
  raw-compare warned but the new method doesn't (confirms switching
  alerts/reports onto floor-then-compare introduces no *new*
  disagreement — only ever closes the existing gap, 1,974 cases in the
  swept range).
- Checked rate provenance: `ItemDailySellRates` only ever inserts
  strictly positive, finite rates, and confirmed no fourth call site
  computes "running out" independently.
- Checked the standing rules: no SQL outside `internal/data` (guard
  green), no money/i18n/offline-first surface touched, no user-facing
  string/route/UI changed so no locale key or `web/help/` manual topic is
  owed, no real client/shop name or secret-shaped literal introduced, and
  confirmed neither of the two recurring bug classes this pipeline
  watches for (missing `os.MkdirAll`, cwd-relative path instead of
  `paths.Data(...)`) applies — this diff does no disk I/O at all.

**Verdict: no blocking findings.**

### Non-blocking findings — 2 fixed inline, 4 deferred to a follow-up card

Fixed in this diff (trivial, zero behavior risk, re-verified with a full
rebuild + the same test packages after applying):

- Two comments (`internal/pages/reports_page.go`,
  `internal/alerts/alerts.go`) still described the old "mirrors"/points-
  at-`EffectiveWarnDays` relationship instead of naming the new shared
  `IsRunningOut` method they actually call now.

Deferred — real but genuinely out of scope for this ticket (which was
scoped to unifying the *comparison*, not eliminating every related
duplication or hardening the method against inputs no current caller
produces); filed as universaltill/ut-docs#440 for a future cycle:

1. `int(qty/rate)` is still computed twice (once inline in
   `inventory_page.go` for the displayed "days left" number, once again
   inside `IsRunningOut` for the boolean) — same expression, same inputs,
   so it cannot drift today, but a future change to `IsRunningOut` (e.g.
   `math.Ceil`, a safety margin) could silently desync the shown number
   from the warning flag with no test catching it.
2. The new test's lead-time row (`qty=16, leadTime=10, rate=2`) sits
   mid-range, not at the actual floor-divergence boundary for a
   configured lead time (that's `qty=21` vs `qty=22`, `rate=2`,
   `leadTime=10`) — the ticket's divergence case is only pinned for the
   *default* 7-day window, not the lead-time-aware path.
3. `IsRunningOut` is untested against a negative `rate`, and the guard
   *ordering* (rate-check before the `qty<=0` check) isn't directly
   asserted.
4. `int(x)` on an out-of-int64-range or NaN `rate` is implementation-
   defined/surprising (confirmed by probe: returns `true` where the old
   raw-compare returned `false`) — unreachable through any of the three
   current call sites (rate is always `positive_qty/28`), but the method
   is exported from `internal/data`, so a future caller passing an
   unsanitized rate would hit it.

## Verified beyond automated tests

- Full `go test ./... -race` run before review: every package green
  except `internal/issuereport`'s `TestSaveCleansUpDirectoryOnWriteFailure`,
  which is **pre-existing and unrelated** — reproduced identically by the
  reviewer against the branch's parent commit (`75a9289`, i.e. `main`)
  in complete isolation from this diff. Root cause: the sandbox runs
  tests as uid 0, so the test's "read-only bundle directory" is still
  writable by root and `Save` succeeds where the test expects failure.
  Already tracked as backlog universaltill/ut-docs#258 / #415 — not a
  regression introduced here, and not blocking this merge.
- `gofmt -l` clean on all changed/added files.

## Safe to merge

Yes. Build, vet, the full non-flaky test suite, `guard-data-access.sh`,
and an independent brute-force correctness check all pass; the one test
failure in the full run is a pre-existing, already-tracked, unrelated
environment artifact. No ADR was needed (same-package, non-architectural,
behavior-preserving-for-the-primary-surface refactor).
