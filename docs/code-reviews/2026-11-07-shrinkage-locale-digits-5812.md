# Review — locale digits on the Shrinkage & Loss tab; printed Z paper stays Latin (ut-docs#5812)

**Date:** 2026-11-07 · **Branch:** `fix/5812-shrinkage-locale-digits`
**Author model:** Sonnet (dev subagent) · **Reviewer model:** Opus 5.5 (independent subagent, read-only, `git archive` copies of WIP `7e24293`)

## What shipped

- `web/ui/partials/reports_tab_shrinkage.html`: the per-reason count
  (`.Count`) and the footer total (`.TotalCount`) render through `qty`, as
  #5784/#5785 did for the other Reports tabs. They were the only raw ints
  left on the tab (the per-item `×qty` and the money cells were already
  localised). en output is byte-identical (`qty` of an int in en = the same
  Latin digits). No Go, i18n key or CSS change.
- `web/help/img/manifest.json`: surface hash refreshed with
  `scripts/ci/update-docs-shots-surface-hash.sh` — docs-shots are en-only
  and the tab is loaded on demand, so no screenshot pixel changes.
- New `internal/pages/reports_shrinkage_locale_digits_5812_test.go`:
  - `TestReportsShrinkageTab_CountsUseLocaleDigits` (en/ar/fa): seeds 23
    waste + 12 void events and asserts each count cell and the bold total
    cell carry `httpx.FormatQty(n, locale)` and, for ar/fa, not the Latin
    cell.
  - `TestBuildEODDoc_ArFaPaperKeepsLatinDigits_5812`: a **guard**, not a
    red/green test — it passes before and after. It pins that the Z paper
    of an ar/fa store contains no U+0660–0669 / U+06F0–06F9 runes, fiscal
    mismatch line included, and that the counts print in Latin digits.

## Part 2 decision — printed paper keeps Latin digits (no change, no new key)

The card asked to decide whether receipt-printer output should use locale
digits. It should not, for now, and this is the existing rule applied
consistently, not a new one:

- ESC/POS text mode can't render Arabic-Indic glyphs; the code states the
  rule at `internal/pages/print_api.go` (sale receipt, "Digit SHAPE stays
  forced Latin … RTL needs bitmap mode", ut-docs#1130) and at the top of
  `buildEODDoc` in `internal/pages/eod_api.go`.
- Every other paper output already forces Latin: sale receipt qty
  (`FormatQtyLatin`), kitchen tickets, cloud print report, voucher reprint,
  shelf labels; `FormatMoneyPrint` even drops an Arabic-script currency
  symbol.
- `internal/print/charset.go` supports Western European and Turkish code
  pages only; the only raster path (GS v 0) is for logos/QR, not text.

So `reports.zpaper.fiscal_device.mismatch` keeps its `%d`; its placeholder
does not change, so ADR-0160's new-key rule is not triggered. Finding 4 of
the #5785 review stays "intentional".

**Caveat (pre-existing, not this card):** an ar/fa Z paper already prints
Arabic-script *labels*, which a text-mode printer can't render either
(ar/fa fall back to the `utf8` charset). Real RTL paper needs a bitmap text
renderer — that is ut-docs#1130's scope. Latin digits stay correct
whichever way that lands.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Major | Template edit staled the `guard-docs-shots.sh` surface hash → CI `build` red | Fixed: hash refreshed (no pixel change) |
| 2 | Minor | Paper test's Latin-count assertions (`"23"`, `"14"`) always matched other text (`£1,234.56`, serial `AV0001234`, date) | Fixed: counts changed to 87/76/65/98/92, which appear nowhere else on the paper |
| 3 | Nit | Unneeded `fixed-date:ok` marker on a past date | Fixed: removed (guard only looks forward) |
| 4 | Nit | Seeded item name matched a company name | Fixed: generic `Test item` |
| 5 | Info | ar/fa Z paper labels are Arabic script (see caveat) | Accepted; tracked by ut-docs#1130 |

## Verification

- Red/green re-verified by the reviewer: with the parent template the tab
  test fails in ar and fa (localised cells missing, Latin cells found);
  reverting only the total cell fails on that cell alone. Mutating the Z
  paper's mismatch line to `httpx.FormatQty` fails the guard for ar
  (U+0664) and fa (U+06F4) — the pin is real.
- Tester (separate agent, worktree): see the PR / card close-out for the
  driven run.
- Gates: `gofmt -l .` clean, `go build ./...`, `go vet ./internal/pages/`,
  `go test ./internal/pages/ ./internal/httpx/`, `golangci-lint run
  ./internal/pages/` 0 issues; guards `guard-i18n`, `guard-fixed-dates`,
  `guard-rtl-logical-css`, `guard-compliance-claims`, `guard-help-topics`,
  `guard-docs-shots` pass.

Pre-flight: help topic `web/help/en/reports.md` n/a (prose only, no digit example); screenshots n/a (en-only, no pixel change; surface hash updated); `reference/` n/a; README n/a; `en.json`/`en.meta.json` n/a (no key change); review record updated; follow-up n/a (RTL paper already tracked by ut-docs#1130).

**Verdict:** approve.
