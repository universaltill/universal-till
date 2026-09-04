# Code review: EOD close-to-close wiring — scheduler / manual run / UI (ut-docs#1141)

**Date:** 2026-09-04
**Card:** ut-docs#1141 (card 2/2 of ut-docs#1081; depends on ut-docs#1140,
already merged as universal-till#574)
**Binding spec:** `ut-docs/adr/0066-eod-zreport-close-to-close-period.md`
**Branch reviewed:** `fix/1141-eod-close-to-close-wiring` @ `05be7ef`
(range `434adef..05be7ef`)
**Complexity:** medium-hard — fiscal (GoBD-relevant) reporting code, write-once
archive rows. Review: independent Reviewer in an isolated worktree, on a diff
it did not write.

## What shipped

Card 1 (ut-docs#1140) landed the data layer: the instant-windowed query family
(`dateRangeSummaryInstant` + siblings), `LatestArchivedAt`, and
`ArchiveReport`'s explicit `closedAt` write plus its atomic same-local-day
double-close predicate. This card wires the behaviour on top of it.

- **`internal/pages/eod_api.go`**
  - `generateEOD` drops its `day string` parameter. It computes
    `from = LatestArchivedAt(ctx, "eod")` (nil → zero `time.Time` → unbounded
    lower bound, ADR-0066 Decision 3) and `to = time.Now()` captured **once**,
    then aggregates via `EndOfDayInstant`.
  - Tax banding calls `SalesForTaxBandsInstant` **directly** and feeds
    `computeEODTaxBandsFromSales` / `computeEODMethodTaxBandsFromSales` from
    that single snapshot. `attachEODBands` is not called on this path at all
    (Decision 6's trap: `date()` parses RFC3339 silently and would degrade the
    report to calendar-day banding with no error).
  - `rep.From`/`rep.To` are populated as **local-offset** RFC3339 for display
    (`from.Local().Format(...)`), `rep.Day` left empty; `From` stays `""` on the
    till's first-ever close.
  - `period` (the archive row's key, and now the audit entry's `entity_id`)
    is `rep.To` — the same local-offset instant string.
  - `ArchiveReport` is called with `closedAt = to`, arming both the clock-skew
    fix and the atomic double-close guard.
  - New `eodPeriodMeta` renders the printed Z-Bon's period header: `END OF DAY
    <day>` for a legacy row, `Zeitraum <From> - <To>`, or `Zeitraum bis <To>`
    for the unbounded first close.
  - `eodSchedulerTick`'s `alreadyDone` moves off `HasArchivedReport(…, day)` to
    `LatestArchivedAt(…).Local()` compared against today's local calendar day.
  - The manual-run handler drops its `day := time.Now().Format(...)` line and
    the request-level day concept entirely.
- **`internal/data/pos_repo.go`** — `ArchivedReportsInRange`'s filter moves off
  `period BETWEEN ? AND ?` onto the row's own `created_at` (Decision 5);
  `PruneReportArchiveOlderThan`'s and `reportRetentionCutoff`'s stale
  "period is always YYYY-MM-DD" doc comments updated; new exported
  `EndOfDayInstant` wrapper so `internal/pages` can reach the unexported
  `dateRangeSummaryInstant`.
- **`internal/pages/reports_page.go` + `web/ui/partials/reports_tab_eod.html`**
  — the Day-end tab's archived-report row gains `From`/`To` (populated only
  when `rep.Day == ""`) and renders `From – To` via the new
  `reports.eod.period_range` key, falling back to the bare `Period` for legacy
  rows.
- **`web/locales/{en,ar,fa,tr}.json`** — new `reports.eod.period_range`.
- **`web/help/en/reports.md`** — prose describing close-to-close.
- Tests: existing EOD/export tests re-anchored off now-meaningless fixed date
  literals, plus 5 new named regression tests (one new file,
  `internal/pages/eod_close_to_close_test.go`).

## Verified against ADR-0066, decision by decision

| Decision | Check | Result |
| --- | --- | --- |
| 2 — instant window, no `attachEODBands` | `generateEOD` calls `SalesForTaxBandsInstant` directly; `attachEODBands`' only remaining caller is the `EndOfDayRange` export handler (`eod_api.go:887`) | correct |
| 2 — all instant siblings reached | `dateRangeSummaryInstant` calls `VouchersIssuedRedeemedForInstantWindow`, `DepartmentsForInstantWindow`, `CashReconciliationForInstantWindow` ungated | correct |
| 3 — unbounded first close | `if !from.IsZero()` guards `rep.From`; no `0001-01-01` string ever reaches display; print path renders `Zeitraum bis …` | correct |
| 4 — `closedAt` on every path | exactly one production `ArchiveReport` call site (`eod_api.go:490`), always passing non-zero `to` | correct |
| 5 — `alreadyDone` local-day | `latest.Local().Format("2006-01-02") == now.Format(...)`; `LatestArchivedAt` itself parses UTC-naive via `time.Parse` | correct |
| 5 — print/`{period}` unchanged | reprint tested against a real RFC3339 period (`url.PathEscape(rep.To)`), and the URL round trip re-verified independently (below) | correct |
| 6 — local offset, never UTC | `.Local().Format(time.RFC3339)` for `From`/`To`/`period`; no downstream `.UTC()` re-conversion on the display path | correct, **with one exception — finding F1** |

Repository pattern: `guard-data-access.sh` passes and reading `eod_api.go`'s
diff by hand confirms no SQL text moved out of `internal/data`. No new file
writes and no new filesystem paths in the diff, so the recurring
missing-`os.MkdirAll` / cwd-relative-path-instead-of-`paths.Data(...)` classes
do not apply here. No real client/shop name and no secret-shaped literal
introduced.

i18n: `reports.eod.period_range` is present in all four locale files with an
identical `"%s – %s"` shape. `eodPeriodMeta`'s fixed German vocabulary
(`Zeitraum`, `bis`) is correctly **not** routed through `T()` — it matches the
same printed-document convention already used in this file for `GUTSCHEINE`,
`STORNOS`, `Erstellt von` and `Anmerkung` (a fiscal-format label an auditor
reads regardless of the operator UI's locale), while the Reports-page HTML
correctly *does* go through `T`.

## Independent re-verification of the TDD claims

Both done as atomic revert → test → restore sequences in the isolated worktree.

1. **`ArchivedReportsInRange`'s SQL reverted to `WHERE period BETWEEN ? AND ?`**
   (everything else untouched). Both named regression tests failed with exactly
   the claimed symptom — the new-format row silently absent, only the legacy row
   returned:
   - `internal/data` `TestArchivedReportsInRange_NewFormatPeriodOnRangeLastDayNotDropped`:
     `expected both the legacy and new-format close in range, got 1: [… Period:2026-08-10 …]`
   - `internal/pages` `TestPostReportArchiveExport_MixedLegacyAndNewFormatClosesBothIncluded`:
     `expected BOTH the legacy and new-format close in the export, got 1: [… Period:2026-08-10 …]`

   Restored; both pass. **Re-run again after the F1 fix and the test
   re-anchoring** — still fail on revert, still pass restored, so the
   regression proof was not weakened by either change.
2. **`generateEOD` sabotaged** to always pass a zero `from` regardless of
   `LatestArchivedAt`. `TestGenerateEOD_AbuttingWindowsNoDoubleCountNoGap`
   caught it immediately: `expected the second close to cover EXACTLY the
   boundary sale (1/2500), not re-counting sale 1, got 2/3500`. The test
   genuinely exercises the wiring; it is not trivially true. Restored.
3. **Reprint URL round trip** (not covered by an existing test, and the one
   place the RFC3339-period-as-path-segment decision could bite in production
   but not in tests): confirmed out-of-band that `html/template` emits
   `…/print/2026-08-24T19:19:00&#43;02:00` in the `hx-post` attribute, that the
   browser decodes that back to a literal `+`, and that Go's `ServeMux`
   `{period}` wildcard hands the handler back the exact original string. Also
   confirmed `url.PathEscape` is a no-op for this value, so the existing tests
   really do cover the production path. No change needed.

## Findings

### F1 — Medium, **fixed** — export range filter compared UTC days against local date bounds

`ArchivedReportsInRange`'s new filter was
`datetime(created_at) >= datetime(?) AND datetime(created_at) < datetime(?, '+1 day')`.
Moving off `period` is right, but `report_archive.created_at` is stored
**UTC-naive** on both paths (the schema default `datetime('now')`, and
`ArchiveReport`'s `closedAt.UTC().Format(...)` write), while `from`/`to` are
**local** calendar dates typed by the shop owner — the very dates a new-format
row's own local-offset `period` displays, and the dates the old
`period BETWEEN` filter matched against. Without `'localtime'` the requested
range silently becomes a UTC-day window.

Consequence: a close made between local midnight and the UTC offset is missing
from an export for **its own local day** and turns up under the previous one —
in a GoBD-relevant export handed to a Betriebsprüfer, and self-inconsistent
with the `period` value the same row displays. It also shifts *legacy* rows
(whose `period` was a local calendar date), against the card's own "no
regression on the historical path" criterion.

This is ADR-0057's bug class, and card 1 already proved the same comparison
must carry `'localtime'`: `ArchiveReport`'s double-close guard uses
`date(ra2.created_at, 'localtime')`, pinned by
`TestArchiveReport_GuardComparesLocalDayNotUTCDay`. ADR-0066 Decision 5's
illustrative SQL omits it, but the Decision's stated purpose is
format-stability, which `'localtime'` preserves — this refines the Decision
rather than contradicting it.

Reproduced before fixing, with a new subprocess-TZ test mirroring card 1's own
technique (`TZ=Pacific/Kiritimati`, UTC+14): a close at local 2026-08-27 01:00
(2026-08-26 11:00 UTC, `period` `2026-08-27T01:00:00+14:00`) was **absent** from
an export for 2026-08-27. Fixed by comparing
`datetime(created_at, 'localtime')` on both bounds. New test
`TestArchivedReportsInRange_ComparesLocalDayNotUTCDay` also asserts the window
is genuinely *shifted*, not merely widened (no rows for 2026-08-26).

### F1a — Low, **fixed** — four re-anchored tests encoded UTC-day semantics

Applying F1 surfaced that several tests this card re-anchored (and one it
added) used hardcoded-UTC or `time.Now().UTC()` seeds against local date
bounds, so they passed only under CI's `TZ=UTC` and inverted the intended
meaning on a non-UTC host — the exact mistake
`internal/data/eod_zreport_local_day_869_test.go`'s own doc comment records
from ADR-0057's history. Re-anchored on **host-local noon** (safe inside its
calendar day for any real IANA offset, -12..+14), per that file's documented
pattern, in `eod_close_export_test.go` (both `at()` helpers),
`pos_repo_lifecycle_test.go` (`at()` helper +
`TestArchivedReportsInRange_NewFormatPeriodOnRangeLastDayNotDropped`),
`pos_repo_zreport_test.go` (`TestArchiveReport_ReceiptRangeRoundTrips`) and
`eod_close_to_close_test.go`
(`TestPostReportArchiveExport_MixedLegacyAndNewFormatClosesBothIncluded`).
This is a strengthening — no assertion was relaxed — and the affected suites
now pass under UTC, UTC+14, UTC-11 and Europe/Berlin.

### F2 — Low, **fixed** — orphaned doc comment

`eodPeriodMeta` was inserted directly beneath
`// buildEODDoc renders the Z-report for the receipt printer.`, so that comment
became `eodPeriodMeta`'s godoc and `buildEODDoc` was left undocumented. Moved
back onto `buildEODDoc`.

### F3 — Low, **fixed** — manual prose did not match the shipped screen

`web/help/en/reports.md` step 3 claimed the archive list shows a period "once
it covers more than one calendar day" and gave `23.08.2026 19:10 - 24.08.2026
19:19` as the format. Both wrong: the range renders for **every** close with a
non-empty `From` regardless of span (the first-ever close, with no `From`, is
the one that shows a single value), and the rendered format is the raw
local-offset ISO timestamps joined by an en dash, not a German dotted date.
The original sentence also read "those belong to this close, not to 'today's'
report", which inverts ADR-0066's own worked example. Rewritten to describe
what the screen and printout actually show, including the `Zeitraum` line and
the first-close case. `web/help/img/manifest.json`'s `reports.en` topic hash
regenerated (`surface_sha256` is unchanged — `eod_api.go` registers only
`/api/` routes and so is outside the guard's screenshot surface). No other
help topic describes the *EOD close's* day semantics; the remaining "day"
language elsewhere in `reports.md` refers to the calendar-period picker and the
business-day-start setting, both explicitly out of scope per ADR-0066's
"Unaffected" list.

### F4 — Nit, **fixed** — stale doc comment missed

The card asked for `PruneReportArchiveOlderThan`'s and `reportRetentionCutoff`'s
stale "period is always YYYY-MM-DD" premises to be updated (both were). Its
sibling `EODClosesForExport` (`internal/data/export_repo.go`) still claimed it
returns closes "whose period falls in [from, to]", which stopped being true
with the same change. Updated.

### F5 — Low, **accepted / deferred** — manual run has no fast "already ran" pre-check

The card asks the manual-run path to do the same `LatestArchivedAt`-based
today pre-check as the scheduler, "purely as a fast user-facing rejection".
It does not; it relies entirely on `ArchiveReport`'s atomic guard returning
`created=false`. Verified this is **correct, not just tolerable**: the
user-visible outcome is identical (the handler's existing `!created →
reports.eod.exists` branch), and `generateEOD` performs no side effects before
the archive attempt — the audit insert and the printer dispatch both sit after
the `if err != nil || !created { return }` early return, so a redundant manual
run cannot double-audit or double-print. The only cost is one wasted
aggregation per redundant manual click. Not worth a second, divergable
code path in fiscal code; ADR-0066 itself stresses that `LatestArchivedAt` is
explicitly not the correctness boundary. Left as-is.

### Dismissed after verification

- **`row.From`/`row.To` only populated inside the `if canRunEOD` block**
  (`reports_page.go`), so a viewer without `eod_report` sees the bare `Period`
  rather than the range. Checked against the surrounding code: `content_json`
  is deliberately parsed only for `eod_report` holders (a pre-existing
  ut-docs#794 decision — the money figures are gated the same way), and the
  bare `Period` for a new-format row *is* the close instant, so the degraded
  view is still meaningful. Not a regression, not this card's call to make.
- **`+`/`:` in the reprint URL path.** Suspected the un-URL-escaped
  `{{ .Period }}` in `hx-post` would break for a `+02:00` offset. Verified
  end-to-end (see re-verification item 3) that it round-trips exactly. No bug.
- **`ORDER BY period` across mixed period formats** in `ArchivedReportsInRange`
  / `ListArchivedReports`. Re-derived ADR-0066 Decision 4's sort-safety claim:
  a bare date is a strict text prefix of the same day's RFC3339 form, so
  ordering stays chronological across the cutover. Holds for local-offset
  values too. Confirmed by `TestOldArchivedReports_ListAndReprintUnaffectedByCutover`.
- **`PruneReportArchiveOlderThan`'s `period < cutoff`** left as a text compare.
  Correct for the same prefix reason; only its doc comment was stale, and that
  was updated.

## Gate

All run in the isolated worktree at the reviewed tip plus the fixes above.

- `gofmt -l .` — empty.
- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./... -timeout 20m` — **entire repository green**, no failures.
- `go test ./internal/data/... -race -run 'EOD|Instant|ArchiveReport|ArchivedReports|LatestArchived'` — ok (18.7s).
- `go test ./internal/pages/... -race -run 'EOD|eod|ReportsPage_EODRow|OldArchivedReports|ReportArchiveExport|ExportDispatch|TaxSummary|Reports'` — ok (65.0s).
- Cross-timezone runs of the EOD/export suites (added for F1):
  `TZ=UTC`, `TZ=Pacific/Kiritimati` (UTC+14), `TZ=Pacific/Midway` (UTC-11),
  `TZ=Europe/Berlin` — all green for both `internal/data` and `internal/pages`.
- CI `build`-job guards, all passing: `guard-data-access`,
  `guard-migration-version-collision`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-page-http-error`, `guard-i18n`,
  `guard-i18n_keycall_test`, `guard-compliance-claims`, `guard-docs-shots`,
  `guard-docs-shots-cross-check_test`, `guard-help-topics`,
  `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`,
  `guard-htmx-loaded`, `guard-autofill-suppression`, `guard-osk-loaded`,
  `guard-e2e-fixtures-import`, `check-brand-assets`, `guard-makefile-version`.

## Verdict

**Safe to merge.**

The wiring is faithful to ADR-0066 — Decisions 2, 3, 4 and 6 are implemented
exactly as written, including the two traps the ADR calls out by name (the
`attachEODBands` fallback, and the clock-skew between `to` and the stored
`created_at`). The regression tests are real: both named ones fail with the
claimed symptom when the fix under them is reverted, and the end-to-end
abutting-windows test catches a deliberately broken `from`.

One Medium finding (F1) was found and fixed: Decision 5's export-filter move
reintroduced ADR-0057's UTC-vs-local bug class in the CSV/retention export,
where card 1's own `ArchiveReport` guard had already established `'localtime'`
as the required comparison. That fix came with a subprocess-TZ regression test
and re-anchored four TZ-fragile tests, so the class is now pinned rather than
merely absent. F2–F4 are cosmetic/documentation and are fixed. F5 is a
deliberate, verified-safe deviation from the card's wording.

Nothing in the diff weakens an existing assertion, and no archived-row format
decision here is reversible after a pilot shop closes under it — which is
exactly why F1 was worth fixing before merge rather than after.
