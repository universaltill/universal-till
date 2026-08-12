# Code review: calendar-aligned report periods (ut-docs#519)

**Date:** 2026-08-12
**Card:** universaltill/ut-docs#519 — "Reports: calendar-aligned day/week/
month/year periods (replaces rolling 365-day cap)" (BA-rewritten split of
the original #519; the 10-year-retention half moved to #551)
**Complexity:** medium (build: Sonnet, inline/subagent; review: Opus subagent)

## What shipped

Reports (`/reports`, `/ui/reports/tab/{name}`) now support calendar-aligned
**day / week / month / year** periods, selectable from a new picker,
additive to the existing rolling `?days=1..365` window (unchanged when no
`?period` is given). A new per-till setting, **`reports.business_day_start`**
(`HH:MM`, default midnight), shifts which calendar day/week/month/year a
sale counts toward — for a bar or late kitchen trading past midnight.

- **Repository layer** (`internal/data/pos_repo.go`): 14 methods
  (`SalesByDay`, `RefundsByWindow`, `TopItems`, `SlowItems`, `DeadStock`,
  `MarginByItem`, `TaxSummary`, `PaymentBreakdown`, `SalesByDepartment`,
  `SalesByTill`, `SalesByWeekday`, `SalesByHour`, `PeriodComparison`,
  `ItemDailySellRates`) changed from a `days int` "trailing N days" window
  to an explicit `[from, to time.Time)` window, bound via `datetime(...)`
  on both the column and the params (fixes a latent RFC3339-vs-`datetime
  ('now')` string-compare bug that previously only `PeriodComparison`
  guarded against — every window query now gets that fix). `EndOfDay`/
  `EndOfDayRange`/`ArchiveReport`/`dateRangeSummary` untouched (already
  worked on explicit date strings).
- **New period resolver** (`internal/pages/reports_page.go`):
  `parseReportWindow`, `businessDateFor`, `parseBusinessDayStart`,
  `reportWindow`. Falls back to the exact prior rolling-`?days=` behavior
  (byte-for-byte, `parseReportDays` untouched) when `?period` is missing/
  invalid.
- **UI**: period picker + anchor date input in `web/ui/pages/reports.html`;
  `reports.business_day_start` field added to the existing EOD/reports
  settings panel (`web/ui/partials/reports_tab_eod.html`), not a new page.
- **i18n**: 8 new keys, all 4 locales (en/fa/ar/tr).
- **Help docs**: `web/help/{en,fa,ar,tr}/reports.md` updated in the same
  branch.

## Process note: an incident mid-pipeline

A Tester-step `git checkout --` accidentally reverted the uncommitted
`internal/pages/reports_page.go` to its pre-#519 state (never staged, so
unrecoverable via git). A second Dev pass reconstructed it from the rest
of the diff (repo signatures, both test files, the UI template's contract)
as its spec. Flagged here because it's exactly the kind of seam a
subtle behavioral drift hides in — which is what the independent review
below actually found.

## Independent review (Opus subagent, isolated worktree)

Full diff review + live re-run of the whole gate + mutation-tested TDD
re-verification on the two most load-bearing new tests (both genuinely
failed pre-fix, passed post-fix — confirmed independently, not taken on
either Dev pass's word).

**2 blocking findings, both real, both traced to the revert-and-
reconstruct seam above, both fixed in this branch before merge:**

1. **`business_day_start` never rendered back** — the eod tab's render map
   never passed the setting to the template, so the input always showed
   blank. Saving the Day-end panel for *any* reason (even just toggling
   the auto-run checkbox) silently posted an empty value and wiped the
   boundary back to midnight. **Fix**: wire the already-in-scope
   `bizDayStart` into the `eod` case's render map
   (`reports_page.go`). **Regression test added**:
   `TestReportsTabs_EOD_BusinessDayStartRoundTrips` — reverted, confirmed
   FAIL (`business_day_start` renders as `value=""`), restored, confirmed
   PASS.
2. **Picker anchor and tab query-string anchor computed independently** —
   `reportAnchorParam` recomputed "today" from naive `time.Now()`,
   ignoring the business-day shift `parseReportWindow` applies internally.
   The wrong value then got baked into every tab button's `?anchor=`, so a
   shop past its boundary (e.g. 01:47 with a 06:00 boundary) saw correct
   KPIs at the top but every *tab* queried tomorrow's not-yet-started
   (empty) window — directly contradicting the feature's own purpose and
   the shipped manual's "they always agree" claim. **Fix**: `reportWindow`
   now carries the resolved `Anchor` (business-day-shifted, single source
   of truth); `reportAnchorParam` deleted; both the picker's date input
   and the tab buttons' query string read `window.Anchor`. **Regression
   test added**: `TestReportsPage_PickerAnchorMatchesTabQueryStringAnchor`
   — reverted, confirmed FAIL (drift possible), restored, confirmed PASS.

**1 medium finding, fixed:** the manual claimed all 6 report tabs honor
the selected period; `forecast` (`SeasonalForecast(ctx, 28, 10)`) and
`eod`'s archive list (`ListArchivedReports(ctx, 14)`) are both intentionally
fixed-window and don't. Corrected the claim in all 4 locales to name the 4
tabs that do (Sales trend, Items, Tax, Payments & channels) and state
plainly that Forecast and the Day-end archive list don't.

**Confirmed clean** (independently verified, not just re-stated from the
implementer): all 78 call sites of the 15 changed methods migrated (no
stragglers); `[from, to)` exclusivity consistent everywhere; ISO week
math correct including the Sunday edge case; timezone handling has no
`time.Local`/`time.UTC` mismatch (shop-local wall clock throughout,
converted once at the SQL boundary); `?days=` fallback unchanged aside
from the documented string-compare bugfix; no missing `os.MkdirAll` /
cwd-relative-path class of bug (diff writes no files); no scope leakage
from #551 (retention); no GoBD/§147 AO compliance claim anywhere; no real
client/shop name in test data; ar/fa/tr translations read as genuine,
idiomatic translations, not English-left-in-place or garbled; RTL logical
properties preserved.

## Verified beyond automated tests

- Full `go test ./... -race -count=1` run twice more after the blocking
  fixes (once by the reviewer subagent pre-fix, once by this session
  post-fix) — clean both times, whole module.
- All 5 CI guards (`guard-data-access`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-i18n`, `guard-help-topics`) re-run after
  the fixes — pass.
- **Real driven run** (throwaway till, seeded sales across two calendar
  months): screenshotted `/reports` with the month/week period picker
  exercised at desktop width (1280×900), the 10" kiosk width (1024×600),
  and `?lang=fa` (RTL) — labels correctly mirrored, date input aligned,
  no overlapping/misaligned elements, period picker + KPI cards render
  correctly in all three. Also screenshotted the Day-end settings tab with
  the new "Business day starts at" field — correctly placed, labeled, no
  layout collision with the existing Save-printer control. **Not checked**:
  dark theme (attempted via a guessed cookie name that didn't actually
  switch it — noting this honestly rather than claiming false coverage;
  low risk, since the new markup reuses existing `<select>`/`<input>`
  styling with no new CSS).
- Manually confirmed via `curl` against the running till that
  `?period=month&anchor=2026-08-05` and `?days=` requests return the
  expected, differently-scoped data.

## Verdict

**Safe to merge.** Both blocking findings fixed with dedicated regression
tests (mutation-verified genuine, not tautological), the medium doc finding
corrected in all 4 locales, full gate green, real driven UI check done.

## Explicitly deferred (not this card)

- 10-year retention + till/cloud/both merchant setting — ut-docs#551
  (cross-repo, needs an ADR first).
- Dark-theme visual confirmation for the new period picker (noted above as
  not independently verified this cycle — low risk, flagging for whoever
  next touches this surface rather than silently claiming coverage).
