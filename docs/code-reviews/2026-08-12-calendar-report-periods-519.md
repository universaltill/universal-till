# Calendar-aligned report periods (ut-docs#519)

**Branch**: `feat/519-calendar-report-periods`
**Complexity**: escalated `medium` → `hard` at Architect time (see issue
comment) — surface area (13 repo methods, handler, 5 templates, 4 locales,
help doc) plus DST/business-day-boundary correctness, not architectural
novelty.
**Dev**: Fable subagent, TDD-first. **Review**: Opus subagent, isolated
worktree, independent of Dev.

## What shipped

- New pure package `internal/reportperiod`: `Window(kind, ref, loc,
  dayStart)` computes `[start, end)` calendar-aligned day/week(Mon-start)/
  month/year windows via `time.Date`/`AddDate` wall-clock construction
  (never a raw `Add(24h)`), so boundaries stay correct across DST
  transitions; `RollingWindow` preserves the exact legacy `?days=`
  semantics as the same instant-pair shape. Table-driven tests cover UTC
  month/year/leap-year boundaries, a non-zero business-day-start offset,
  and real `Europe/Berlin` spring-forward (23h day) / fall-back (25h day)
  transitions.
- New per-shop setting `reports.business_day_start` (local `HH:MM`,
  default `00:00`), validated with the existing `eodTimeRe` pattern,
  editable from the Day-end (EOD) tab alongside the existing EOD-time
  field.
- `/reports` gains a `?period=day|week|month|year` selector, additive to
  the existing `?days=` rolling window — either mode fully replaces the
  other (two independent single-param forms), and every report tab that
  already shared the `days=` convention (`sales-trend`, `items`, `tax`,
  `payments`) now honors `period=` too. `forecast` (fixed 28-day lookback)
  and `eod` (fixed 14-count) are untouched by design — independent,
  fixed-window features, out of scope for this card.
- 13 `POSRepo` methods converted from `days int` to `start, end time.Time`:
  `SalesByDay`, `RefundsByWindow`, `TopItems`, `SlowItems`, `DeadStock`,
  `PeriodComparison`, `TaxSummary`, `MarginByItem`, `SalesByWeekday`,
  `SalesByHour`, `PaymentBreakdown`, `SalesByDepartment`, `SalesByTill`.
  `PeriodComparison`'s year-ago window shifts by a genuine calendar year
  (`AddDate(-1,0,0)`), not `-365*24h`.
- i18n: 8 new keys added to all 4 locales (`en` real copy; `ar`/`fa`/`tr`
  carry the same English text — see "Deferred" below).
- Help doc (`web/help/en/reports.md`) extended with a "Choosing the
  period" section.

## Independent review — findings

No blockers. Three findings, one fixed in review, one left as a follow-up
card, one accepted as-is:

- **Real, follow-up card filed (ut-docs#559)**: `SalesByDay`'s per-day
  breakdown groups by `date(created_at)` — the raw UTC calendar date —
  while the *window* is now business-day/local-aligned. So the day-by-day
  table in the Sales-trend tab can still split one trading night across
  two calendar-date rows even with a non-zero `business_day_start`, even
  though the period *totals* above it are correct. Reviewer fixed the
  user-facing overclaim in the manual + the settings hint text (both now
  say the day-by-day table still splits by calendar date) rather than
  leaving stale docs promising behavior that doesn't exist; the grouping
  fix itself needs `date(created_at, <offset>)` plus a decision on
  UTC-vs-local grouping, and touches the `backoffice_page.go`/`ask_api.go`
  call sites too — correctly scoped as a separate card, not a review-time
  fix.
- **Nitpick, not fixed**: `PeriodComparison`'s year-ago shift collapses to
  a zero-width window on a `?period=day` request for Feb 29 in a
  non-leap year-ago year (`AddDate(-1,0,0)` normalizes 2027-02-29 →
  2027-03-01 for both `start` and `end`). Degrades safely — `YoYHas`
  gates on `lastYear.Count > 0`, so the YoY card just hides rather than
  showing a wrong number. One day every four years; not worth code churn.
- Spot-checked and confirmed correct: the two pre-review visual fixes
  (EOD tab's mislabeled "Save printer" button → generic `common.save`;
  the rolling-days `<select>` defaulting to a misleading "Today" while a
  calendar period is active → explicit disabled placeholder).

Also confirmed by the independent review, not a finding: the two other
`SalesByDay` callers (`backoffice_page.go`'s dashboard widget,
`ask_api.go`'s AI "ask" plugin) now produce a *more* correct window than
before — the new `datetime()` wrapper fixes a real pre-existing boundary
bug (RFC3339 rows with `T` string-compared greater than the old
space-separated `datetime('now', ?)` bound and were sometimes wrongly
included/excluded at the edge), and the window now has an explicit upper
bound so a clock-skewed future-dated row no longer leaks in.

## Verified beyond automated tests

- Real driven run (built binary, `UT_AUTH=off`, throwaway data dir):
  screenshotted `/reports` in rolling mode, `period=month`, `period=week`,
  `period=day` at kiosk viewport (1024×600), `fa` locale (RTL), and the
  EOD tab's new business-day-start editor — English, RTL layout, and
  kiosk sizing all read cleanly; RTL confirmed the (expected, documented)
  English-fallback strings for `fa` render without breaking layout.
  Screenshots not committed (none were load-bearing enough to justify
  binary-in-repo per this pipeline's evidence convention); re-taken after
  applying the two visual fixes to confirm the fix, not just the bug.
  Dark theme was not independently re-screenshotted — it's a server-side
  setting, not `prefers-color-scheme`, and the new controls reuse the
  same `<select>`/`<input>` markup already covered by the existing theme
  CSS, so risk is treated as low, not zero.
- Independent review (Opus, isolated worktree) actually broke and
  restored four pieces of implementation to confirm each guarding test
  is real, not a tautology: the DST wall-clock construction (spring-
  forward/fall-back), the `sqliteUTCEnd` same-second round-up, the
  calendar-year YoY shift, and the `datetime()` column-format wrapper —
  all four failed with the correct, meaningful symptom when reverted and
  passed again once restored. Full before/after transcripts in the
  review agent's report (not reproduced here — see PR discussion).
- `go build ./... && go test ./... -race` clean; all five CI guards
  (`guard-data-access`, `guard-i18n`, `guard-help-topics`,
  `guard-kiosk-engine`, `guard-plugin-menu-read`) pass.

## Deferred / follow-up cards

- **ut-docs#559** — `SalesByDay`'s per-day table doesn't honor the
  business-day boundary (F1 above).
- **ut-docs#560** — `ar`/`fa`/`tr` i18n keys for this card and
  `web/help/{ar,fa,tr}/reports.md` carry English text/are unchanged: the
  self-hosted NAS translator (`192.168.1.231:11434`) is unreachable from
  this cloud pipeline's sandbox, and this pipeline does not substitute
  itself (a paid AI API) to produce shipped translations — same
  constraint and same handling as ut-docs#505. `guard-i18n.sh` (key-set
  parity only, not content) and `guard-help-topics.sh` (topic-set
  completeness, not per-topic content) both stay green regardless.
- 10-year retention / till-cloud-both storage location — already tracked
  separately as ut-docs#551, unaffected by this change.

## Verdict

Safe to merge.
