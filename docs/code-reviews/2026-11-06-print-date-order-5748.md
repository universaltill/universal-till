# Code review — printed dates follow the screen's date order (ut-docs#5748)

**Date:** 2026-11-06 · **Branch:** fix/5748-print-date-order · **Reviewer:** independent subagent (Opus 5.5; code by Sonnet)
**Precedent:** ut-docs#1130 (per-locale `dateLayout` table), ut-docs#1632 (`FormatDateTime*`)

## Root cause (BA, verified)
The card's stated cause was wrong: `dateLayout("en")` is already day-first. Print paths format with
`httpx.DefaultLocale()` (the raw `store.locale`, e.g. `en-US`). The screen's `date`/`datetime` template funcs format with
the request locale, and the ☰/wizard language buttons store a bare `en`. So on an `en-US` shop the screen showed
06/11 and the paper 11/06. A second defect also played a part: a defaulted GB/GBP till carries `store.locale=en-US`
from `UT_DEFAULT_LOCALE`. That one is split to ut-docs#5751 by the Architect, because it changes how tills seed their
locale.

## What shipped
- `internal/httpx/dateformat.go`: new `DateLocale(display)`. A bare language tag that matches the shop locale's language
  keeps the shop's region (`en` on an `en-US` shop → `en-US`). A tag with its own region, or another language, is used
  as given. All four formatters (`FormatDate`, `FormatDateLatin`, `FormatDateTime`, `FormatDateTimeLatin`) resolve the
  layout through it, so every screen and print path agrees by construction. Digit shape still follows the passed
  locale. `dateLayout` also treats `en_US` (underscore) as month-first (review LOW-1).
- `receipt_designer.go` preview and the `/api/print/test` slip use `FormatDateTimeLatin(…, DefaultLocale())`.
  Before, they printed a raw ISO `2006-01-02 15:04` that matched no receipt.
- Docs: `web/help/en/printing.md` item 6 and ut-docs `architecture/receipt-printing.md` ("Receipt language").
  `web/help/img/manifest.json`: only the surface hash changed. No PNG changed, because the preview's date is below the fold.

## Findings
| # | Severity | Finding | Resolution |
|---|---|---|---|
| LOW-1 | Low | `dateLayout` compared `en-us` exactly, so `en_US` was day-first (old behaviour). | Fixed, with table rows `en_US` and `EN-us`. Mutation-checked: reverting the fix fails `FormatDateLatin("en_US")`. |
| LOW-2 | Low | The designer test read `clock.Now()` twice, so it could flake across a minute boundary. | Fixed: it accepts the before or after reading. |
| LOW-3 | Low | The new help sentence broke the paragraph's "it" antecedent, and "the same as the screen" over-promised across languages (de screen in English shows `/`, paper shows `.`). | Fixed: the sentence moved to the end and was reworded. |

Reviewer checked and found no problem in: callers of the formatters (only `internal/httpx` and `internal/pages`; no
export, CSV, fiscal, cloud-upload or LAN-sync path), a kiosk customer's own language, fa/ar digit shape, `de` on a
de-AT or de-CH shop, empty or upper-case locales, atomic `DefaultLocale` reads, and cost (about 232 ns and 2 allocs per call).

## Verification
- TDD: Dev showed the new tests red before the fix. Tester and Reviewer each reverted the fix and saw them fail
  (`screen "06/11/2025 21:33" != paper "11/06/2025 21:33"`), then restored it and saw them pass.
- Gate (Tester): gofmt, build, vet, golangci-lint (0 issues), shellcheck (0 issues) and 141 of 142 ci.yml build-job
  guards passed. The Windows code-signing test was skipped because it needs nsis via sudo apt. `go test ./...`: 108
  packages ok. `internal/plugins` timed out while docs-shots ran alongside it, then passed alone in 30s. It does not
  import httpx or pages.
- Driven run (Tester): real till, a fake network printer capturing ESC/POS, UI on bare `en`. On an en-US shop the
  journal row, journal detail, designer preview, printed receipt and TEST PRINT all showed `11/06/2026 23:17`. On an
  en-GB shop all five showed `06/11/2026 23:17`. The pre-fix build showed screen 06/11 and paper 11/06, which
  reproduces the bug.

Pre-flight: help topic updated; docs-shots run (only the manifest surface hash changed); ut-docs architecture doc
updated; README, en.json, ADR, plugin/manifest and my./cloud n/a; follow-up for Defect B is ut-docs#5751.

## Verdict
APPROVE. Deferred: ut-docs#5751 (defaulted GB till on en-US). PR universal-till#2401 (another lane's voucher slip)
gets the new rule through the shared formatter and needs no change.
