# Code review: SalesByDay groups by local business day, not raw UTC calendar date

**Date:** 2026-08-13
**Author (Dev):** scrum-master pipeline, Sonnet (complexity:medium)
**Reviewer:** independent Opus subagent (isolated worktree)
**Card:** universaltill/ut-docs#559

## What shipped

`internal/data/pos_repo.go`'s `SalesByDay` grouped its per-day breakdown by
`date(created_at)` — the raw stored UTC calendar date — while the report
*window* itself (ut-docs#519/#290) already resolves against the shop's local
business-day boundary (`reports.business_day_start` via `businessDateFor`/
`parseBusinessDayStart` in `reports_page.go`). A trading night spanning that
boundary — or just local midnight, for any non-UTC shop, even at the 00:00
default — produced two day-rows instead of one, even though the period
*totals* were already correctly business-day-aligned.

`SalesByDay` now takes the resolved `hh, mm` business-day-start and groups via
`date(created_at, 'localtime', ?, ?)` SQLite modifiers instead of the raw
`date(created_at)`. Four call sites updated:

- `reports_page.go` (×2): reuse the window's own already-resolved boundary
  (`reportWindow` gained `Hour`/`Minute` fields) rather than re-parsing the
  setting.
- `backoffice_page.go`'s weekly dashboard widget: passes `0, 0` — it only
  sums `Total`/`Count` across rows, never displays `Day`, so the grouping
  boundary provably cannot change its result (a sum over any partition of
  the same window is identical) — documented at the call site, not just left
  unfixed.
- `ask_api.go`'s `sales_by_day` AI "ask" tool: resolves
  `reports.business_day_start` itself and threads it through fully (a 2-line
  addition — `d.Settings`, `keyReportsBusinessDayStart`, and
  `parseBusinessDayStart` were already in scope in the same file/package),
  so its per-day breakdown now agrees with `/reports` too.

New regression coverage in `internal/data/pos_repo_batch8_reports_test.go`:
`TestPOSRepo_SalesByDay_BusinessDayBoundary_MergesTradingNight` reproduces the
ticket's exact repro (two sales either side of a 04:00 boundary within one
trading night → one merged row with the correct summed count/total/tax and
the correct business day); `TestPOSRepo_SalesByDay_DefaultBoundary_NoRegressionInUTC`
pins the default `hh=mm=0` UTC-host case as unchanged.

## Verified beyond automated tests

- **TDD, independently re-verified twice** — once by the implementing Dev
  subagent, once again by the orchestrator: temporarily reverted just the
  query inside `SalesByDay` back to the old `date(created_at)` form (keeping
  the new signature so it still compiled), reran the new tests, and confirmed
  `MergesTradingNight` failed with exactly the ticket's symptom
  (`got 2: [{Day:2026-08-13 Count:1 Total:500...} {Day:2026-08-12 Count:1
  Total:1000...}]`) while the no-regression test still passed; restored the
  fix, confirmed both pass, byte-identical diff to the version that shipped.
- **Independent review verified the SQL empirically against the real
  driver** (`modernc.org/sqlite`), not by reading documentation: `"0
  hours"`/`"0 minutes"` are genuine no-ops, negative modifiers shift the date
  correctly, `NULL` handling is unchanged, and bind-parameter order matches
  the query's `?` placeholders.
- **`created_at`'s UTC storage confirmed by reading the actual write path**
  (`internal/pos/sales.go`'s `time.Now().UTC().Format(time.RFC3339)` into
  `POSRepo.InsertSale`), not assumed from the commit message.
- **The `'localtime'`/`time.Local` timezone assumption is pre-existing, not
  newly introduced** — `businessDateFor`/`parseReportWindow` and the existing
  `DayTotal` method already rely on the same SQLite-vs-Go local-clock
  agreement, which holds by construction on this product's single-process
  till.
- **`backoffice_page.go`'s "unaffected, not just unfixed" claim verified by
  reading the actual consumer code** — `daily` is scoped to the `if` and
  consumed only via `weekTotal +=`/`weekCount +=`; `.Day` is never read.
- **DST reasoned through explicitly** (spring-forward including the skipped
  hour, fall-back ambiguity) — the shift applies to the already-localized
  wall-clock value, matching `businessDateFor`'s own approach; concluded not
  an issue, not merely an accepted risk.
- **Regression-test quality checked, not just presence**: confirmed
  `MergesTradingNight` would catch a sign-inverted fix (asserts the specific
  merged business day, not just the row count), and that `NoRegressionInUTC`
  exercises two genuinely different UTC calendar dates.
- **Exactly 4 production call sites confirmed via independent grep** — no
  fifth caller missed.
- **`web/help/en/reports.md` checked by reading it**, not trusting the dev's
  claim that no manual edit was needed — its existing "Business day start"
  section already describes the effect generically and becomes *more* true
  after this fix, not less.

## Independent review — findings

**No blockers.** No money/tax correctness, data-loss, or security issue.

One recommended (non-blocking, but fixed anyway before merge — cheap and
correctness-adjacent): the two new regression tests hardcoded expected
`Day` strings computed via Go's `.UTC().Format(...)`, which only hold for
host UTC offsets in roughly `[-19:30, +2:30)` — the reviewer reproduced
failures under `Asia/Kolkata`, `Asia/Tokyo`, and `Pacific/Kiritimati` by
running the actual test suite under those `TZ` values, and traced a related
pre-existing test (`TestPOSRepo_SalesByDay_AggregatesFiltersOrders`) whose
UTC-based expectation had gone stale the moment the query it exercises
switched to local-time grouping — that one is even clock-dependent (fails
only during certain hours of the day at extreme offsets), the worst flake
class to ship. Fixed here by computing expected day strings via a
`date(?, 'localtime', ?, ?)` control query against the real SQLite driver —
mirroring the `ctrlSlot` idiom `TestPOSRepo_SalesByWeekdayAndHour_
BucketsLocalTime` already uses in the same file — instead of a Go-side
`.UTC()` literal. Re-verified under `UTC`, `Asia/Kolkata`, `Asia/Tokyo`, and
`Pacific/Kiritimati`: all pass on all four post-fix.

Nits (noted, no action needed): `SalesByDay` takes raw `hh, mm int` with no
in-repo clamping (safe today — only `parseBusinessDayStart`, which already
validates, feeds it, and both are bound params regardless).

**Suggested follow-up (new Backlog card, out of scope for this card):**
`SalesByWeekday`/`SalesByHour` (the same Sales-trend tab's busiest-day/hour
chart) already use `'localtime'` but still ignore the business-day boundary
— so with a non-default boundary, a late-night sale can count toward one
business day in the merged day-rows table but a different calendar day in
the busiest-day chart on the same tab. Pre-existing, not introduced by this
change.

## Gate — all green

`go build ./...`, `go vet ./...`, full `go test ./...` (not just
`internal/data`/`internal/pages`), `guard-data-access.sh`, `guard-i18n.sh` —
all clean, run independently by both the Dev subagent and the Reviewer
subagent. The TZ-robustness fix re-verified under four timezones
(`UTC`, `Asia/Kolkata`, `Asia/Tokyo`, `Pacific/Kiritimati`) after being
applied.

## Verdict

**Safe to merge.** Single-repo, no ADR needed (bugfix using the same
`'localtime'`-grouping convention this codebase already established
elsewhere, not a new architectural decision), no money-type impact (`int64`
minor-unit totals are the pre-existing `DailySales` struct shape, untouched),
no i18n impact (backend-only change, no template/string additions), manual
already accurate without edit.
