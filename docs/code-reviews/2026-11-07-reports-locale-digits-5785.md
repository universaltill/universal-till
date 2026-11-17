# Review — locale digits on Reports: archived EOD periods, ÖKC counts, Sales KPI, day options (ut-docs#5785)

**Date:** 2026-11-07 · **Branch:** `fix/5785-reports-rtl-digits`
**Author model:** Sonnet (dev subagent) · **Reviewer model:** Opus 5.5 (independent subagent, detached worktree at WIP `0468e3a`)

## What shipped

- `web/ui/partials/reports_tab_eod.html`:
  - The archived-EOD period cell renders through `datetime`. A close-to-close
    row shows `datetime .From – datetime .To`, converted to local time with
    the locale's date order and digits. A legacy bare-date row (`.Period` =
    `YYYY-MM-DD`) goes through `ParseStoredDate` → `FormatDate`, with no
    clock and no `Local()` shift.
  - The 🖨 `hx-post` URL still uses the raw `.Period`, as the test pins.
  - The five ÖKC fiscal-device counts (mali fiş, iade fişi, bilgi fişi,
    other, till tenders) render through `qty`. Z numbers stay Latin
    because they are identifiers.
  - The mismatch line uses the new key
    `reports.eod.fiscal_device.mismatch_count` (`%s`/`%s`) with `qty`
    arguments.
- `web/ui/pages/reports.html`: the Sales KPI count and the 7/14/30/90 day
  options render through `qty`. The option `value=` attributes stay Latin.
- i18n:
  - The old `%d` key `reports.eod.fiscal_device.mismatch` is removed from
    `en.json`, and its `key-meta-baseline.txt` line is removed too.
  - The new key has an `en.meta.json` entry with two placeholder meanings.
  - A new key was required because the placeholder verb changed.
- Tests:
  - New `internal/pages/reports_locale_digits_5785_test.go`, covering en,
    ar and fa.
  - `eod_close_to_close_test.go` now compares against `datetime` output
    and no longer expects the raw ISO strings.
- `web/help/img/manifest.json`: the surface hash is refreshed.

## Findings

| # | Severity | Finding | Suggested fix / rationale |
|---|---|---|---|
| 1 | **major (manual stale, #324)** | `web/help/en/reports.md:20` tells the shop owner that the archive list shows the two timestamps as `2026-08-23T19:10:00+02:00 – 2026-08-24T19:19:00+02:00`. After this change the list shows the locale's date and a minute clock (e.g. `23/08/2026 19:10 – 24/08/2026 19:19`, with the order set by the shop's region). The prose now describes output the till no longer produces. | **Fix in branch:** reword the example without hard-coding one layout, e.g. "shows the same two moments side by side (for example 23/08/2026 19:10 – 24/08/2026 19:19, in your shop's date format and local time)". Then refresh the `reports` topic hash in `web/help/img/manifest.json` (`make docs-shots` or the manifest writer) and re-run `guard-docs-shots.sh`. `web/help/de/reports.md:20` has the same ISO example. Translations come through Weblate (ADR-0160), and `guard-help-drift` will warn, so English-only is acceptable. |
| 2 | minor (accept) | Until Weblate supplies translations, ar, fa and **tr** have no `mismatch_count`, so the ÖKC mismatch line falls back to English. Turkish shops are the only ÖKC market, and they lose a line that `tr.json` translated before. `guard-i18n` reports `reports.eod.fiscal_device.mismatch` as an orphan in ar/fa/tr (warning only, like the existing `reports.today` orphan). | **Accept:** the repo rule says a placeholder change needs a new key and code PRs add English only (ADR-0160 §2). Weblate translation memory should re-offer the old tr/ar/fa strings. Mention it in the PR so the tr string is re-translated promptly. |
| 3 | nit (accept) | The range display drops seconds and the UTC offset, so two closes within the same minute would look identical in the list. | **Accept:** this matches every other `datetime` cell on the till, and the printed Z report keeps its own Zeitraum line. |
| 4 | nit (accept) | The printed Z paper (`eod_api.go:471`, `reports.zpaper.fiscal_device.mismatch`, `%d`) still prints Latin digits. | **Out of scope / intentional:** it is a separate key for receipt-printer output, and the removed key is referenced nowhere else (grep over Go, HTML, JS and JSON: only the zpaper key and the orphan locale copies remain). |

Checked and found clean:
- **Bare date in non-UTC time zones.** `datetime` tries `ParseStoredTime` (RFC 3339 or SQLite layout only) first. A bare `YYYY-MM-DD` falls through to `ParseStoredDate` → `FormatDate` with no `Local()` call, so the day never shifts. The targeted tests pass under `TZ=America/Los_Angeles`, `Asia/Tehran` and `Pacific/Kiritimati`.
- **Print URL.** `hx-post="/api/reports/eod/print/{{ .Period }}"` is unchanged and raw, and the test asserts this for both rows.
- **`qty` on int fields.** `qtyFloat` accepts ints. The en output of the KPI and day options is byte-identical (`12`, `14 days`).
- **Docs-Shots-Unchanged.** This claim is honest:
  - docs-shots run en only (33 topics × 1 locale).
  - `/reports` is shot with `period=month&anchor=2025-12-15`, which hides the day-options `<select>` (it is in the `{{ else }}` branch).
  - The Sales KPI in en is the same digits as before.
  - The EOD archive is an on-demand tab (ut-docs#401) that is not loaded in the resting shot.
  - Only the surface hash needed refreshing. Finding 1 will change the `reports` *topic* hash; that is not a screenshot change.
- **RTL.** No new CSS. The existing `text-align:end` is logical.
- **Fixed dates.** The 2025 fixtures are outside the 60-day window and carry `fixed-date:ok`.
- **Test data.** No real shop or person names.
- **The two recurring bugs.** There are no file writes and no cwd-relative paths, so neither applies.

## TDD re-verification (reviewer, in the worktree)

- Reverted the non-test files to `origin/main` (`git checkout origin/main -- web/ui web/locales web/i18n-meta scripts/ci/i18n-baseline`), then ran `go test ./internal/pages/ -run '5785|EODRowPeriodDisplay|LocaleDigits|CloseToClose' -count=1`. The results were:
  - FAIL `TestReportsEODArchive_FiscalAndPeriodUseLocaleDigits/{ar,fa}`
  - FAIL `TestReportsPage_SalesCountAndDayOptionsUseLocaleDigits/{ar,fa}`
  - FAIL `TestReportsPage_EODRowPeriodDisplay_LegacyBareDateVsCloseToCloseRange`
- Restored with `git checkout HEAD -- .` (clean status) and re-ran: all PASS, including the #5784 precedent `TestReportsEODArchive_CountsUseLocaleDigits` and the #5755 shift tests.

## Gates (worktree)

| Gate | Result |
|---|---|
| `go build ./...` | ok |
| `go vet ./internal/pages/` | ok |
| `gofmt -l internal/pages` | clean |
| `guard-i18n.sh` | ok (orphan warnings only: `reports.eod.fiscal_device.mismatch` in ar/fa/tr, pre-existing `reports.today`) |
| `guard-i18n-meta.sh` | ok (579 described, 3349 in baseline) |
| `guard-fixed-dates.sh` | ok |
| `guard-docs-shots.sh` | ok (surface b3914f78a66f…) |
| `guard-rtl-logical-css.sh` | ok |
| Targeted tests under 3 non-UTC TZs | ok |

Pre-flight: Till user-visible behaviour → `web/help/en/reports.md` updated (finding 1, fixed), docs-shots n/a (en screenshots unaffected, see above), `reference/` n/a (no reference page documents the archive period format), README n/a; new/changed `en.json` key → `en.meta.json` updated, baseline line removed; review record → this file; follow-up → none beyond Weblate re-translation of `mismatch_count` (finding 2).

## Resolution (orchestrator)

- Finding 1 **fixed**: `web/help/en/reports.md` now describes the archive range as "from – to, date and time to the minute, in your own local time and your language's date format and digits", with no hard-coded layout. The `reports` en topic hash in `web/help/img/manifest.json` was refreshed (prose-only change, so the screenshot is unchanged). `guard-docs-shots.sh` ✓, `guard-help-topics.sh` ✓, `guard-help-drift.sh` ✓ (non-blocking warnings only), `guard-compliance-claims.sh` ✓.
- Findings 2–4 accepted as above.
- Tester (separate agent): red/green re-verified, full `go test ./...` showed one regression (`EODRowPeriodDisplay`), fixed before this review; golangci-lint 0 issues; driven run with Chromium at 1024×600 and 360×740 in en/fa/ar: Persian and Arabic digits on the day options, Sales KPI, period cells and ÖKC cells; Z number Latin; print button visible; mismatch line wraps without clipping. UX gate (code reading): pass.

## Verdict

**Approved** after the finding 1 fix. The code was approved as is in round 1.
