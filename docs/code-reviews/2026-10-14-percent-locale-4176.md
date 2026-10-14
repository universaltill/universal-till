# Review — Percent values follow the locale's decimal separator; printed receipt VAT label from a locale key (ut-docs#4176)

**Date:** 2026-10-14 · **Lane:** lane:cloud-41 · **Built by:** Opus 5.5 (inline) · **Reviewed by:** Fable (fresh-context subagent, isolated worktree). The card is `complexity:medium`.

## What shipped

- `httpx.FormatPercentBP(bp, locale)` (`internal/httpx/currency.go`) is a two-decimal percent with the locale's decimal separator, built on `LocalizeMajor`. It changes the separator only and keeps Latin digits, so it is safe on print paths too.
- `FuncsFor(locale)` now overrides `bpPercent` with a version bound to the locale. That fixes the sale-screen discount label (`basket.html`) and the on-screen receipt (`receipt.html`). The base func stays for callers outside a request.
- Invoice (`internal/pages/invoice_page.go`): the printed VAT band label, the on-screen band rate and the line detail row (`invoiceLineSub`) all use `FormatPercentBP` with the locale already in scope.
- Printed receipt (`internal/pages/print_api.go`): the English literal `"Total incl. VAT %.2f%%"` is replaced by the new key `receipt.total_incl_vat` plus `FormatPercentBP`, both in `printLocale` (the store locale), the same locale as the money on that row.
- Reports → Tax tab (`internal/pages/reports_page.go`, added from the review): the band rate is `taxrate.FormatPercent` + `LocalizeMajor`. This keeps the tab's short shape (`7.5%`, `20%`) and gives a de till `7,5%` beside `1.234,56 €`.
- New key `receipt.total_incl_vat` in `web/locales/{en,ar,fa,tr}.json`. The language packs de/es/pt get follow-up PRs in the same cycle. Because the key is brand new, core merges first.
- `web/help/img/manifest.json` gets a surface-hash-only refresh (`Docs-Shots-Unchanged`). The docs-shots pages (routes[0]) are the `/invoices` list, the sell screen with no discount staged and `/reports` with no tab clicked. None of them renders a changed pixel in en/fa/ar/tr.

The en output is byte-for-byte unchanged: the existing `Total incl. VAT 20.00%` expectations still pass. The help topics don't describe number formatting, so no manual prose changes.

## Findings (Fable review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The first ar wording (`الإجمالي شامل ضريبة القيمة المضافة`, 34 runes) plus ` 20.00%` overflows the 42-column ESC/POS row once the amount is added. `print.kvRow` clips the label, so the rate drops off and two VAT bands print identical labels. | **Fixed.** The ar string is now `الإجمالي شامل الضريبة`. New guard `TestPercentLocale_PrintedVATLabelFitsReceiptWidth` checks every core locale against `print.Width`. It failed on the old string. |
| 2 | major (scope gap) | Reports → Tax tab still showed `7.5%` dot-only next to comma-formatted money. | **Fixed** in this PR, with `TestPercentLocale_ReportsTaxTab`, which failed before the fix. |
| 3 | minor | The printed receipt now mixes languages on tr/fa/ar tills: the other labels (Tax, TOTAL, …) are still English. | Accepted. This is the card's declared non-goal and is tracked on ut-docs#4199. |
| 4 | minor | The admin tax-codes table (`tax_codes_page.go`) is still dot-only. It is admin-only and has no money next to it. | Deferred. Added to ut-docs#4281. |
| 5 | minor | fa/ar percent keeps Latin digits while the money next to it is digit-shaped. | Deferred. This is the declared non-goal and is filed as ut-docs#4281. |
| 6 | nit | The tr percent sign should come first (`%7,50`). | Deferred to ut-docs#4281. When that lands, the tr test expectation will change. |
| 7 | nit | `%.2f` of `float64(bp)/100` where the repo elsewhere prefers integer arithmetic. | Accepted. The output is exact for every integer bp, and this is the same expression the old `bpPercent` used. |

No SQL, no money arithmetic, no file writes and no paths are involved, so the `os.MkdirAll` and `paths.Data` checks don't apply. `internal/pages` has no `t.Parallel()`, so the test helper `pinDefaultLocale` (which sets a global and restores it in `t.Cleanup`) is safe.

## TDD re-verification

- **Dev:** all five `TestPercentLocale_*` site tests and `TestFuncsForBPPercentIsLocaleBound` failed on the old code with the real symptom (`de: expected "(7,50%)"`, `expected a VAT band label with "20,00%"`, `tr: expected a "KDV dahil toplam 20,00%" totals row`, …). The two tests added from the review also failed before their fixes.
- **Reviewer:** in an isolated worktree, reverted `invoice_page.go`, `print_api.go` and `httpx.go` to `origin/main` and kept `FormatPercentBP`. All five site tests and the FuncsFor test failed; after restoring, all passed.

## Verified beyond unit tests (driven run)

The real binary ran on a seeded demo DB (port 8099) with a `test-4176` 7.5% promotion, scanned onto a basket and driven with Playwright. Screenshots were looked at:
- tr at 1024×600: `İndirim (7,50%): -£0,09`. The totals block layout is unchanged.
- tr at 360px (basket sheet opened): `İndirim (7,50%):` wraps exactly as before, since a comma is the same width as a dot.
- en at 1024: `(7.50%)`, unchanged.
- fa at 1024 (RTL): `(7.50%)` in Latin digits, unchanged. This is the ut-docs#4281 follow-up.
- **Not looked at:** the printed paper itself (no printer in a cloud session; the doc model is asserted by tests), the invoice page in a browser (covered by the httptest assertion), dark theme, and the kiosk device.

## Gate

`gofmt` clean, `go build ./...`, `go vet`, `golangci-lint` (0 issues), and `go test` for every package. `internal/plugins` hit the local 10-minute default timeout; this diff doesn't touch it, and CI runs it as its own step. `internal/httpx` and `internal/pages` were re-run green after the review fixes. Guards pass: data-access, i18n, compliance, competitor, core-neutral, deadcode-baseline, docs-shots, help-topics, help-drift, no-showmodal, kiosk-engine, card-data.

**Verdict:** safe to merge. After the merge, land the de/es/pt pack PRs (`lang-pack-drift` stays red on `main` until they do).
