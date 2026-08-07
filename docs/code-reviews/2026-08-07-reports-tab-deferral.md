# Code review — Reports: run on demand behind tabs instead of on page load

- **Ticket:** universaltill/ut-docs#401 (`complexity:hard`)
- **Repo / branch:** `universal-till`, `main` (uncommitted working tree at review time)
- **Implemented by:** Dev (Fable)
- **Reviewed by:** Reviewer (Opus 5), independent model, 2026-08-07
- **Verdict:** **PASS — safe to merge** after two Reviewer fixes (below).

## What shipped

`/reports` previously ran ~16 DB queries unconditionally on every page load,
including `SeasonalForecast` (6 full sales-table scans, ut-docs#199). The change
splits the screen in two:

- **`GET /reports`** now renders only the always-visible monitoring section:
  the KPI row (revenue / sales / tax / refunds / net / YoY) and the low-stock
  chip. Its handler runs exactly five repository calls — `SalesByDay`,
  `PeriodComparison`, `ItemDailySellRates`, `ListStockLevels`,
  `RefundsByWindow`.
- **`GET /ui/reports/tab/{name}`** (new htmx fragment endpoint) runs each heavy
  report only when the operator clicks its tab. Six tabs: `sales-trend`,
  `items`, `tax`, `forecast`, `payments`, `eod`; unknown names 404.
- Six new partials under `web/ui/partials/reports_tab_*.html`; `reports.html`
  trimmed to KPI row + tab nav + empty `#report-tab-panel`.
- `parseReportDays` extracted so page and fragments share the same 1..365
  `?days=` clamp; the tab nav propagates the page's current `days`.
- 6 new `reports.tab.*` keys in all four locales; `web/help/{en,fa,tr,ar}/reports.md`
  rewritten to describe the tabbed flow.

## Findings

| # | Severity | Finding | Status |
|---|----------|---------|--------|
| 1 | **Medium** | `e2e/tests/pages.spec.ts` asserted `/reports` contains **"Slow sellers"** — that string moved to the `items` tab fragment, so the e2e smoke job (`.github/workflows/e2e.yml`) would have gone red on merge. Dev did not update it. | **Fixed** |
| 2 | **Medium** | `scripts/ci/guard-docs-shots.sh` failed: the reports screen changed materially and the manual's screenshots were not regenerated (`make docs-shots`). CLAUDE.md requires the regenerated screenshot in the same branch; the guard is a real CI gate. | **Fixed** |
| 3 | Low | `case "eod"` runs `ListArchivedReports` + two settings reads *before* the manager check, which lives only in the partial's `{{ if .IsManager }}`. No data leak (output is empty for a non-manager) and this is unchanged from the pre-change code, but Go-side gating (`isManagerOrAuthOff`, the idiom used everywhere else in `internal/pages`) would be defence-in-depth and would skip the queries. | **Accepted / backlog** |
| 4 | Low | The tab buttons carry no active/selected state (no `aria-selected`, no `role="tablist"`, no pressed styling), so once a tab is open the operator cannot tell which one is showing. UX/BA scope, not a deferral defect. | **Accepted / backlog** |
| 5 | Low | `scripts/ci/check-lang-pack-drift.sh` fails on the 6 new `reports.tab.*` keys missing from `ut-plugin-language-{de,es,…}`. **Already red before this change** (de was missing 5 unrelated keys, es 46+), and only fixable in the external pack repos. | **Out of scope / backlog** |
| — | — | No new SQL anywhere: the diff only relocates calls to existing `POSRepo` methods between handlers. `guard-data-access.sh` passes. | Verified |
| — | — | No money-type violations (no new money arithmetic; `int64` minor units still cross only the DB/DTO boundary). | Verified |
| — | — | No file writes, so neither recurring pipeline bug class applies: no missing `os.MkdirAll`, no cwd-relative path where `paths.Data(...)` belongs. | Verified |
| — | — | No real client/shop names in seed data (test fixtures use `Zzyzx …` placeholders); no secret-shaped literals in the diff. | Verified |

### Fix 1 — e2e smoke marker + a real click test

`e2e/tests/pages.spec.ts`: the `/reports` marker is now `Revenue` (a KPI label
that stays on the page), with a comment naming ut-docs#401 so the next reader
knows why it isn't `Slow sellers`. Added
`a reports tab runs its report only when clicked`, which loads `/reports` in a
real browser, asserts **"Slow sellers" is absent**, clicks the Items tab, and
asserts the fragment swapped in — the half the Go tests structurally cannot
see (the htmx wiring). Verified green: 6/6 in `tests/pages.spec.ts`.

### Fix 2 — manual screenshots

Ran the real harness (`playwright test --config=playwright.docs.config.ts` +
`tests-docs/write-manifest.js`, i.e. the two halves of `make docs-shots`):
56 captures, 14 topics × 4 locales, all passed; `web/help/img/manifest.json`
rewritten. `guard-docs-shots.sh` now passes. Note this regenerates **every**
topic's PNG by design, so the commit carries 57 changed files under
`web/help/img/` — that is what the target does, not scope creep.

> Environment note for the orchestrator: `npx playwright install chromium`
> is blocked by the egress policy (403 from `cdn.playwright.dev`), so the run
> used the sandbox's preinstalled `chromium-1194` with a matching
> `@playwright/test@1.56.0` installed `--no-save`. `package.json` and
> `package-lock.json` are **unchanged** and `node_modules` was restored to the
> locked 1.61.1 afterwards. CI regenerating on 1.61.1/chromium-1228 would
> produce cosmetically different but equally valid PNGs.

## Verified beyond the automated tests

- **Re-derived the core claim by reading `internal/pages/reports_page.go` in
  full**, not from the Dev report. The `GET /reports` body (lines 32–85) calls
  only `SalesByDay`, `PeriodComparison`, `ItemDailySellRates`,
  `ListStockLevels`, `RefundsByWindow`. `TopItems`, `SlowItems`, `DeadStock`,
  `MarginByItem`, `TaxSummary`, `SeasonalForecast`, `SalesByWeekday`,
  `SalesByHour`, `PaymentBreakdown`, `SalesByDepartment`, `SalesByTill` and
  `ListArchivedReports` appear **only** inside the `/ui/reports/tab/{name}`
  switch.
- **Booted the real till** and curled every fragment:
  `sales-trend|items|tax|forecast|payments|eod` → 200 with real rendered HTML,
  `nonsense` → 404. Confirms the partials parse under the real embedded FS and
  locale funcs, which `RenderPartial`'s `template.Must` would otherwise panic on.
- **Grepped the served page**: `/reports?days=30` contains zero occurrences of
  `Slow sellers` / `Top items` / `Busy`, all six `hx-get="/ui/reports/tab/…?days=30"`
  wirings, and one `#report-tab-panel`. The only three `hx-trigger` attributes on
  the page are the pre-existing base-layout chips (`bugreport-chip`,
  `sync-chip`, `session-chip`) — **no tab auto-fires**.
- **False-pass check on `TestReportsPage_TopItemsDeferredToItemsTab`**: it is
  *not* vacuous. It seeds a real item + sale + sale_line with a distinctive name,
  asserts the name is absent from `GET /reports`, then asserts it is **present**
  in the items tab — the positive half proves the seed is reachable, so the
  negative half is meaningful. If `/reports` started calling `TopItems` again,
  this test fails.
- **Partial-template shape**: `httpx.RenderPartial` does
  `template.New(base).ParseFS(fs, page)` and executes the file directly, so a
  partial must be bare markup with no wrapping `{{ define }}` — matching the
  known-good `web/ui/partials/stock_table.html`. All six new partials follow
  that shape; balanced `{{ if }}`/`{{ range }}`/`{{ end }}` confirmed by the
  live 200s.
- **htmx script execution**: the EOD partial carries an inline `<script>`.
  Verified no CSP header is set anywhere in `internal/` and htmx runs with the
  default `allowScriptTags: true` / empty nonce, so the Z-report range button
  still works after a swap.
- **RTL**: no literal `left`/`right` in `reports.html` or any new partial —
  only `margin-inline-start`, `inline-size`, `text-align: start/end`. Confirmed
  visually on the regenerated `web/help/img/ar/reports.png`: KPI row and tab
  strip lay out right-to-left correctly.
- **Manual, all four locales**: `en`/`fa`/`tr`/`ar` `reports.md` each carry a
  genuine translation of the new three-step flow — not English pasted in and
  not left stale. `routes:` front matter is untouched
  (`[/reports, /journal, /journal/{receipt}, /shifts, /audit]`).
- **`/ui/` denylist claim re-verified at source**, not taken on faith:
  `scripts/ci/checkhelptopics/routecoverage.go:36` lists
  `{"/ui/", "htmx fragments swapped INTO pages — the enclosing page's topic covers them"}`,
  so the new fragment route correctly needs no `routes:` entry of its own.
- **Locale drift**: fa/ar/tr values for the 6 new keys are real translations,
  not en copies.

## Gate status after the review pass

| Gate | Result |
|------|--------|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test ./internal/pages/ -run TestReports -v` | PASS — 17/17, including the 6 retargeted and 5 new tests |
| `gofmt` on the changed Go files | clean (4 unformatted files elsewhere in `internal/` are pre-existing and untouched) |
| all 10 `scripts/ci/guard-*.sh` | PASS (incl. data-access, i18n, help-topics, **docs-shots**, htmx-loaded) |
| `e2e/tests/pages.spec.ts` (real Chromium) | PASS — 6/6 |
| `go test ./...` | one unrelated failure: `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure` — the sandbox runs as uid 0, which bypasses the `0500` directory permission the test relies on. Environmental, not caused by this change. |

## Explicitly deferred

1. **Tab grouping is a recommended default, not product-owner-confirmed.**
   Which report lands in which of the six tabs is the BA's proposal; it is
   flagged in the PR description and may be reshuffled without touching the
   deferral mechanism.
2. **Finding 3** — move the EOD manager check into the handler
   (`isManagerOrAuthOff`) so the queries are skipped, not just the render.
   New Backlog card.
3. **Finding 4** — active-tab state / `role="tablist"` / `aria-selected` on the
   tab nav. New Backlog card (UX).
4. **Finding 5** — `ut-plugin-language-*` packs are missing the new
   `reports.tab.*` keys, on top of substantial pre-existing drift. New Backlog
   card, owned by the pack repos, not this one.
5. **Pre-existing gofmt drift** in `internal/pages/external_api_test.go`,
   `internal/pages/import_page_test.go`, `internal/plugins/marketplace/client.go`,
   `internal/thirdparty/webview_go/webview.go`. Untouched here; new Backlog card.
6. **`internal/issuereport` test is uid-sensitive** — passes as a normal user,
   fails as root. Worth a `t.Skip` guard. New Backlog card.

## Files changed (final tree, uncommitted)

```
 M e2e/tests/pages.spec.ts              (Reviewer fix 1)
 M internal/pages/reports_page.go
 M internal/pages/reports_page_test.go
 M web/help/{en,fa,tr,ar}/reports.md
 M web/locales/{en,fa,tr,ar}.json
 M web/ui/pages/reports.html
 M web/help/img/**                      (57 files — Reviewer fix 2, make docs-shots)
?? web/ui/partials/reports_tab_{sales_trend,items,tax,forecast,payments,eod}.html
```

Nothing was committed, pushed or opened as a PR — the tree is left dirty with
the Reviewer's fixes applied, for the orchestrator.
