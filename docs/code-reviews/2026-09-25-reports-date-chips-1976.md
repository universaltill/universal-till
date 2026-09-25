# Review: /reports date-range chip row (ut-docs#1976)

Date: 2026-09-25 · Lane: `lane:cloud-24` · Complexity: medium
Author: Opus 5.5 (pipeline) · Independent reviewer: Fable (different model)

## What shipped

- `/reports` header: the period `<select>` + anchor date / rolling-days
  `<select>` pair is replaced by one chip row — **Today · Yesterday ·
  This week · This month · Custom** — using the shared `.filter-chips`
  pattern (3rem touch floor, focus ring, checkmark + accent so the
  active state is never colour alone).
- Preset chips are plain GET links onto the existing
  `?period=day|week|month&anchor=YYYY-MM-DD` contract; no tab endpoint
  changed. `reportPresets` (internal/pages/reports_page.go) marks a
  chip active exactly when its window, resolved through
  `parseReportWindow`, equals the page's current window. "Today" is the
  business date (`businessDateFor` + `reports.business_day_start`).
- Anything else (rolling `?days=`, a past anchor, a year) lights
  **Custom**, which keeps the original controls unchanged underneath
  (rolling option relabelled "Last days" so it no longer reads "Custom"
  twice). Default `/reports` is unchanged (rolling 14 days).
- `parseBusinessDayStart` / `businessDateFor` / `parseReportWindow`
  untouched (ut-docs#1664 caution).
- i18n: 6 new keys in en/ar/fa/tr; de/es language-pack PRs follow the
  core merge (they also carry two keys the packs had fallen behind on
  from ut-docs#2614: `designer.hidden.unhide_all`,
  `elevation.summary.buttons_unhide_all`).
- Manual: `web/help/{en,de,fa,ar,tr}/reports.md` step 1 names the chips;
  reports screenshots regenerated (`make docs-shots`).

## Findings (Fable review)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | blocker | `TestReportsPage_PickerAnchorMatchesTabQueryStringAnchor` red: `?period=day` is now the Today preset, so the date input isn't rendered | Fixed — the test now checks the same invariant on both paths: the active chip's anchor (`period=day`) and the date input (`period=year`, Custom) must equal the tabs' anchor |
| 2 | major | New test used `AddDate(0,-1,0)` for "last month" — Go normalises 03-31 → 03-03, red on 7 dates/year | Fixed — `AddDate(0,0,-today.Day())` |
| 3 | major | First-of-month case wrong on the 2nd (Yesterday correctly lights) | Fixed — expectation handles day 1 and day 2 |
| 4 | minor | "now" sampled up to three times per request (business-day-boundary race) | Fixed — `reportPresets` takes the already-resolved window and uses its anchor as "today" when the request has no usable anchor |
| 5 | minor | Tapping the already-active Custom chip threw away a year / past-anchor selection | Fixed — the active Custom chip links to the current URI; test added |
| 6 | minor | Custom's rolling-days select still has a "Today" (= last 24h) option next to the business-day Today chip | Accepted — card scope says the Custom controls stay unchanged; filed as ut-docs#2672 |
| 7 | nit | Raw `&` in href | Fixed — `&amp;` like the tab hx-gets |
| 8 | nit | `<nav>` landmark for a filter row | Fixed — `role="group"` like `buttons.html`'s category chips |
| 9 | nit | ar "آخر الأيام" → "الأيام الأخيرة"; tr help "eski" → "önceki" | Fixed |

TDD re-verified by the reviewer in an isolated worktree: dropping the
`w.To.Equal(current.To)` half of the active check makes
`TestReportPresets_ActiveChipMatchesCurrentWindow` fail
(`?period=day&anchor=<1st>: active presets = [this_month]`). The author
also mutated the business-day source (`businessDateFor(..., 0, 0)`) and
saw `TestReportPresets_QueriesUseBusinessDate` fail; that test now uses
a 23:59 boundary so it discriminates at any time of day.

## Verified beyond unit tests

Driven run against a throwaway till (`e2e/run-till.sh`, Chromium,
Playwright) at **1024×600** and **360×740**, in **en, fa (RTL), tr**:
5 chips render, Custom active by default with the old selects visible;
tapping Yesterday navigates, moves `aria-current`, hides the selects;
every chip ≥ 44px tall and inside the viewport; no horizontal scroll
(`scrollWidth` = viewport); the Items tab still loads the chip's window;
keyboard focus ring visible. Screenshots looked at: en/fa at 1024×600
(docs-shots), en/fa at 360px. Not looked at: dark theme, ar/tr at 360px,
de (pack locale) — German chip labels are short ("Dieser Monat") and the
row wraps rather than scrolls. Not verified on real touch hardware (plain
links, no pointer handlers — nothing touch-specific changed).

## Gate

`gofmt -l .` clean; `go build ./...`, `go vet ./...`; full `go test ./...`
green after fixes; guards: data-access, kiosk-engine, page-http-error,
i18n, compliance-claims, competitor-naming, docs-shots, help-topics,
help-drift, htmx, autofill, osk, emoji-font, readme-links — all pass.

## Verdict

Safe to merge. Follow-ups: de/es pack PRs (same cycle); ut-docs#2672 for
finding 6.
