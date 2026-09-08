# Code review: Start's digest loop shares one clock with its test's seed data (ut-docs#1769)

**Date:** 2026-09-08
**Card:** ut-docs#1769 (`p2`, `complexity:easy`, bug, test-only fix)
**Branch:** `fix/1769-flaky-alerts-digest-clock`
**Diff:** `internal/alerts/alerts.go`, `internal/alerts/alerts_test.go`
**Reviewer:** independent review in an isolated worktree, different model from
the implementing agent.

## What shipped

`TestStart_RunsDigestLoopBody` failed reproducibly on a clean `origin/main`
when run at 00:15 local time in a non-UTC timezone, three times in a row, with
CI green because CI runs in UTC.

Root cause: the test seeded synthetic "N days ago" sales against one
`time.Now().UTC()` read, while `Start`'s loop internally called
`unusualSales(ctx, db, time.Now())` — a **second, independent** clock read
milliseconds later. `unusualSales` fans out to `POSRepo.DayTotal`
(`internal/data/pos_repo.go:1673`), which buckets by **local calendar day**
(`date(created_at, 'localtime') = date(?, 'localtime', '-N days')`). That
local-day bucketing is deliberate production behaviour — shop reporting uses
the shop's own calendar day — and is not itself the bug. But whenever the two
independent reads straddled a calendar-day boundary, "today" disagreed by one
day, shifting every seeded bucket, so fewer than 3 of the 4 baseline weeks
matched, `unusualSales` never fired, and `unusualHits` stayed 0.

This is the same class of bug already fixed once at the `unusualSales`-unit
level for ut-docs#969 (`TestUnusualSales_EveryWeekdayIsDeterministic`),
reappearing one layer up in `Start`'s own wiring, which had no seam to share a
reference instant through.

The fix adds that seam:

- `internal/alerts/alerts.go` — `unusualSalesNowOverride` (`atomic.Value`
  holding a `func() time.Time`) plus a `unusualSalesNow()` accessor returning
  the override if a test installed one, else `time.Now()`. `Start`'s loop now
  calls `unusualSales(ctx, db, unusualSalesNow())`. This follows the file's
  own pre-existing `firstDelayNS`/`tickIntervalNS` convention and its stated
  rationale (the loop goroutine reads these independently of a test's write).
  **One line of production code changed; no production behaviour change** —
  with no override installed the accessor is `time.Now()`.
- `internal/alerts/alerts_test.go` — the loop test's body became
  `runDigestLoopScenario(t, ref)`, which installs `ref` through the override
  **and** seeds every synthetic `created_at` from that same `ref`, including
  the low-stock digest's own `'sl'` sale (previously a raw
  `datetime('now','-1 days')` SQL literal). Two tests drive it:
  `TestStart_RunsDigestLoopBody` and a new table-driven
  `TestStart_RunsDigestLoopBody_AnyLocalHour`.

`TestUnusualSales` and `TestUnusualSales_EveryWeekdayIsDeterministic`
(ut-docs#969's own fix) are untouched — the diff's first hunk starts at line
497, well below both — and both still pass.

## Findings

### 1. BLOCKER (fixed here) — the fixed-date `ref` gave the test an 11-day fuse

`Start`'s loop has **two** halves, and only one of them got a clock seam. The
other, `pushDigest` → `runningOutCount` (`alerts.go:69`), still reads the real
clock and has none:

```go
now := time.Now().Add(time.Second)
rates, err := repo.ItemDailySellRates(ctx, now.Add(-28*24*time.Hour), now)
```

The shipped diff anchored the low-stock digest's `'sl'` sale — the only seeded
sale carrying a `sale_lines` row, and therefore the only one that produces a
sell rate — to `ref.AddDate(0, 0, -1)` where `ref` was the hard-coded
`2026-08-23T12:00:00Z`. Seeded at 2026-08-22, that sale sits inside the real
28-day window only while `time.Now() < 2026-09-19T12:00Z`. From roughly
2026-09-19 onward `ItemDailySellRates` returns an empty map,
`runningOutCount` returns 0, `pushDigest` pushes nothing, and every one of the
four (sub)tests fails at `digestHits == 0` — on `main`, for reasons unrelated
to what the test is about.

Reviewed today (2026-09-08) that left **11 days** of headroom. So the fix for
a timezone-flaky test replaced it with a deterministically-expiring one.

Demonstrated rather than argued: shifting only the `ref` date back 17 days
(2026-08-23 → 2026-08-06), i.e. simulating 17 days of calendar drift with
nothing else changed:

```
=== RUN   TestStart_RunsDigestLoopBody
    alerts_test.go:603: Start's loop never pushed a low-stock digest within 2s of fast-forwarded timers
--- FAIL: TestStart_RunsDigestLoopBody (2.08s)
```

**Fix applied:** `ref` is now built by a new `scenarioRef(hour, min, zone)`
helper as *the given time-of-day, in the given zone, on the calendar day three
days before today*. The offset is constrained from both ends, and the helper's
doc comment records why neither end is free:

- **Not a fixed past date** — it must stay inside `runningOutCount`'s
  real-clock 28-day window. Three days back leaves three weeks of margin,
  permanently.
- **Not today either** — the 3-day offset is what keeps the test red when the
  fix is reverted. With the override removed the loop queries the real clock,
  whose buckets sit 3 days from the seeded ones, and 3 never coincides with
  the detector's 7-day baseline lattice (1, 8, 15, 22, 29), so no baseline
  week matches and the assertion fires.

### 2. MEDIUM (fixed here) — the cleanup installed a *broken* clock rather than restoring the real one

```go
t.Cleanup(func() { unusualSalesNowOverride.Store(func() time.Time { return time.Time{} }) })
```

That does not uninstall the override; it swaps in a clock pinned to the zero
time (year 1). Any later `Start()` in the same test binary would then bucket
against year-1 dates. It also made `unusualSalesNow`'s own `f != nil` guard
dead code — that branch exists precisely to be the reset path. Latent today
(the only other `Start` caller, `TestStart_JoinsOnCancel`, runs earlier and
cancels before the first fire), but it is a trap for the next test added to
this package.

**Fix applied:** `unusualSalesNowOverride.Store((func() time.Time)(nil))`.
`atomic.Value` accepts this — it panics only on a nil *interface*, and the
concrete type stays `func() time.Time`, satisfying its consistent-type rule —
and `unusualSalesNow` then falls through to `time.Now()` as intended.

### 3. MEDIUM (fixed here) — a failing subtest leaked the loop goroutine into the next one

`cancel()` was only reached on the success path. Every `t.Fatal` above it
returned without stopping the loop, while `defer database.Close()` shut the DB
out from under a goroutine still querying it. Observed live while
reproducing finding 1:

```
    alerts_test.go:603: Start's loop never pushed a low-stock digest within 2s of fast-forwarded timers
2026-09-08T01:21:03Z [WARN] alerts: digest push failed (will retry tomorrow): query sell rates: sql: database is closed
2026-09-08T01:21:03Z [WARN] alerts: digest push failed (will retry tomorrow): query sell rates: sql: database is closed
2026-09-08T01:21:03Z [WARN] alerts: digest push failed (will retry tomorrow): query sell rates: sql: database is closed
```

Harmless as a single test; now that the body is a helper invoked four times in
a row, a first failure leaves a goroutine spinning at a 2 ms tick through the
remaining subtests, muddying their output and their timing.

**Fix applied:** `defer cancel()` immediately after the context is created.
Deferred LIFO puts it ahead of `defer database.Close()`, so the loop is told
to stop before its DB goes away. The explicit `cancel()` + `waitWithin` join
on the success path is unchanged. Re-running the reverted-fix red case after
this change, the `database is closed` warnings are gone.

### 4. LOW (fixed here) — the new test's central claim about its own zones was inverted

`TestStart_RunsDigestLoopBody_AnyLocalHour`'s comment asserted its refs were
"chosen so the local calendar day it represents differs from the UTC calendar
day for the same instant". They were chosen so it does **not**. All three
instants sat on the same date in both representations:

```
local=2026-08-23T00:05:00-08:00   utc=2026-08-23T08:05:00Z  localDate=2026-08-23 utcDate=2026-08-23 differ=false
local=2026-08-23T23:55:00+09:00   utc=2026-08-23T14:55:00Z  localDate=2026-08-23 utcDate=2026-08-23 differ=false
local=2026-08-23T12:00:00Z        utc=2026-08-23T12:00:00Z  localDate=2026-08-23 utcDate=2026-08-23 differ=false
```

The hours were exactly the wrong way round: to straddle the boundary you need
23:55 *west* of UTC (already tomorrow in UTC) and 00:05 *east* (still
yesterday). **Fix applied:** hours swapped, subtest names corrected.

### 5. LOW (fixed here) — the zone labels overstated what is covered

`DayTotal` binds `ref.UTC()` and buckets with SQLite's `'localtime'`, which is
the **process's** timezone. A `time.Time`'s `Location` therefore never reaches
the query — only the instant it denotes does. So `time.FixedZone("UTC-8", …)`
does not exercise a non-UTC process timezone; the three cases reduce to three
instants (07:55Z, 15:05Z, 12:00Z). That is still the right variable — those
are exactly the times of day where a second, independent clock read could land
on a different calendar day — but the comment read as if timezone coverage
were being obtained. **Fix applied:** the comment now states precisely what
varies and what does not, and points at the deferred TZ sweep below.

Verified the underlying premise rather than assuming it — a throwaway probe
(added, run, deleted) confirmed the process TZ *does* reach SQLite through
`mattn/go-sqlite3`:

```
TZ=""                     -> datetime('2026-08-23T12:00:00Z','localtime') = 2026-08-23 12:00:00
TZ="Asia/Tokyo"           -> datetime('2026-08-23T12:00:00Z','localtime') = 2026-08-23 21:00:00
TZ="America/Los_Angeles"  -> datetime('2026-08-23T12:00:00Z','localtime') = 2026-08-23 05:00:00
```

### 6. LOW (fixed here) — seeded `created_at` deviated from the file's own documented convention

`TestUnusualSales` carries an explicit note that seeded `created_at` must
mirror what production writes (genuine UTC, per `internal/pos/sales.go`)
because `DayTotal` applies a single `'localtime'` conversion. The new seeds
used `ref.AddDate(…).Format(time.RFC3339)` with `ref` in a `FixedZone`,
emitting `…-08:00` / `…+09:00` suffixes. Functionally fine — SQLite parses the
offset and normalises — but inconsistent with the stated convention and with
production's shape. **Fix applied:** seeds now `.UTC().Format(time.RFC3339)`.

### 7. Not a defect — `atomic.Value` is the right tool, and the store/load is safe

Reasoned through, not just `-race`'d:

- **Install → read.** The test's `Store` is sequenced before `Start`, and
  goroutine creation establishes happens-before with everything sequenced
  before it, so the loop goroutine can never observe a stale/absent override.
  There is no window.
- **Read → reset.** On the success path the explicit `cancel()` +
  `waitWithin(&wg, 2*time.Second)` join completes inside the test body, before
  any `t.Cleanup` runs, so the reset cannot race a live read. Cleanup LIFO
  order is fine either way: the override reset is registered *second*, so it
  runs *first*, and both run only after the join.
- **The failure path is why `atomic.Value` (not a plain var) is correct.** A
  leaked goroutine surviving a `t.Fatal` can genuinely read concurrently with
  the cleanup's `Store`; `atomic.Value` makes that data-race-free rather than
  UB. Finding 3 narrows the window further but does not eliminate it, so the
  choice stands on its own merits.
- Subtests are sequential (no `t.Parallel`), so the single package-global is
  not contended. Same constraint the existing `firstDelayNS`/`tickIntervalNS`
  knobs already impose — **accepted**, consistent with package convention.

### 8. Not a defect — `pushDigest`/`runningOutCount` is not flaky the same way

Checked explicitly, since it is the half of the loop this diff does not touch.
`runningOutCount` reads the real clock, but `ItemDailySellRates` compares
**absolute instants** over a 28-day span (`datetime(s.created_at) >=
datetime(?) AND < datetime(?)`), with no calendar-day quantisation. Two clock
reads milliseconds apart move that window's edges by milliseconds, against
data seeded ~27 days from either edge. There is no day-boundary cliff to fall
off, so it is genuinely different from `DayTotal`'s single-day equality match
— **no separate finding**. Its real-clock dependency matters only as the
mechanism behind finding 1, which is now handled on the test side.

### 9. Checklist items — all clear

- **`os.MkdirAll` on a file-write path:** N/A, no file writes in the diff.
- **cwd-relative path instead of `paths.Data(…)`:** N/A, no paths in the diff.
- **Repository pattern:** no SQL added outside `internal/data`;
  `guard-data-access.sh` passes. The test's raw `d.Exec` seeds are pre-existing
  style in this file and the guard exempts `_test.go`.
- **`money.Money`:** no monetary arithmetic added; the seeded totals are raw
  `int64` at the DB boundary, matching the surrounding tests.
- **i18n:** no user-facing strings added.
- **Real client/shop names or secrets in test data:** none — `Cola`, `Floor`,
  `store-l`, `tok-l` are synthetic, and the token is a local `httptest`
  server's.

## What was verified beyond the existing automated tests

- **TDD re-verified personally, twice.** Reverted *only* the `alerts.go`
  call site to `unusualSales(ctx, db, time.Now())`, leaving the test file
  intact, and confirmed the exact reported failure on every subtest:

  ```
  === RUN   TestStart_RunsDigestLoopBody_AnyLocalHour
      alerts_test.go:629: Start's loop never pushed an unusual-sales notification within 2s of fast-forwarded timers
      alerts_test.go:629: Start's loop never pushed an unusual-sales notification within 2s of fast-forwarded timers
      alerts_test.go:629: Start's loop never pushed an unusual-sales notification within 2s of fast-forwarded timers
  --- FAIL: TestStart_RunsDigestLoopBody_AnyLocalHour (6.22s)
  ```

  Then **repeated the same revert after my own fixes**, since changing how
  `ref` is chosen could have destroyed the red. It did not — and the plain
  `TestStart_RunsDigestLoopBody` now goes red too (it did not have to before,
  since its fixed ref was already far from `time.Now()`):

  ```
  --- FAIL: TestStart_RunsDigestLoopBody (2.07s)
  --- FAIL: TestStart_RunsDigestLoopBody_AnyLocalHour (6.22s)
      --- FAIL: .../just-before-local-midnight-west-of-UTC (2.07s)
      --- FAIL: .../just-after-local-midnight-east-of-UTC (2.07s)
      --- FAIL: .../local-noon-UTC (2.08s)
  ```

  Restored, green again, including `-race`.

- **Expiry reproduced** by date-shifting `ref` (finding 1) rather than taken on
  faith from arithmetic.

- **Real non-UTC process timezones**, the closest available stand-in for the
  reported 00:15-local repro: `go test ./internal/alerts/ -count=1` passes
  under `TZ=America/Los_Angeles`, `TZ=Asia/Tokyo`, `TZ=Pacific/Kiritimati`
  (+14) and `TZ=Etc/GMT+12` (−12), the two offset extremes.

- **Full gate re-run in this worktree, after my fixes:**

  | Command | Result |
  | --- | --- |
  | `gofmt -l .` | clean |
  | `go build ./...` | OK |
  | `go vet ./...` | OK |
  | `go test ./...` | 0 FAIL |
  | `go test ./internal/alerts/... -race` | ok, 32.9s |
  | `golangci-lint run ./...` | 0 issues |
  | all 22 CI-blocking guards in `ci.yml`'s `build` job | all pass |

  Guards run individually: `guard-data-access`, `guard-price-history-sync`,
  `guard-migration-version-collision`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-page-http-error`, `guard-i18n`,
  `guard-compliance-claims`, `guard-help-topics`, `guard-webkit-version`,
  `guard-kiosk-launch-flags`, `guard-android-status-address`,
  `guard-android-i18n`, `guard-android-external-links`, `guard-emoji-font`,
  `guard-htmx-loaded`, `guard-autofill-suppression`, `guard-osk-loaded`,
  `guard-e2e-fixtures-import`, `check-brand-assets`, `guard-makefile-version`,
  `guard-docs-shots`.

## Explicitly deferred

- **A real process-timezone sweep.** Finding 5 establishes that SQLite's
  `'localtime'` follows the process TZ, so the faithful regression test for
  this bug class would run the scenario under several `TZ` values. Changing
  `TZ` mid-process is not reliable in Go (`time.Local` is resolved once, and
  C-side `tzset` behaviour varies), so it needs a subprocess re-exec harness
  or a CI matrix dimension — more than a `p2`/easy card should carry. The
  current test is a deterministic red/green regression for the seam itself,
  and four TZs were checked by hand above. **Backlog-worthy note, not a
  blocker.**
- **A DST-transition caveat in the shared seeding idiom.** `ref.AddDate(0, 0,
  -n)` subtracts exact 24 h multiples, while `date(?, 'localtime', '-n days')`
  subtracts calendar days *after* converting to local. In a DST-observing
  process timezone the two can diverge by an hour, which only shifts a bucket
  when the resulting local time is within an hour of midnight. Pre-existing —
  it applies equally to `TestUnusualSales_EveryWeekdayIsDeterministic`'s
  `00:00:30` refs from ut-docs#969, which are closer to the edge than
  anything here — and unreachable in UTC CI. Not introduced or worsened by
  this diff. **Backlog-worthy note.**
- **No seam for `runningOutCount`'s clock.** Deliberately not added (finding
  8): it would be production surface area for a half of the loop that has no
  day-boundary cliff. The test simply stays inside its window instead.

## Verdict

**Safe to merge**, with the four fixes above applied in this worktree.

The shipped production change is one line behind a test-only seam, correct,
and idiomatic for this file. The shipped *test* change, however, would have
turned a timezone-flaky test into a calendar-expiring one within 11 days
(finding 1) — that was a genuine blocker, not a nit, and is the reason this
review is not a rubber stamp. With `ref` now anchored relative to today, the
override properly uninstalled, the loop goroutine no longer leaking past a
failed assertion, and the comments describing what the test actually covers,
the change does what ut-docs#1769 asked: the digest loop and its seed data
read one clock, and the test proves it deterministically at instants either
side of a local day boundary.
