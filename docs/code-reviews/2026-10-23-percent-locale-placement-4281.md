# Review: on-screen percent sign placement and digit shape (ut-docs#4281)

- **Branch:** `fix/4281-percent-locale-placement`
- **Built by:** Sonnet (Dev subagent). **Reviewed by:** Opus 5.5 (fresh subagent, separate worktree), triaged by the `lane:cloud-41` orchestrator.

## What shipped

- `internal/httpx/currency.go`: one percent formatter with CLDR sign placement
  (checked against `golang.org/x/text` v0.40.0 `number.Percent`): en `7.50%`,
  pt/it/nl `7,50%`, de/es/fr `7,50 %`, tr `%7,50`, fa `۷٫۵۰٪`, ar `٧٫٥٠٪`.
  - `FormatPercentBP` stays **print-safe**: Latin digits, ASCII `%`, an ASCII
    space before `%` (NBSP would print as `?` on an ASCII-charset ESC/POS
    printer). It now also places the sign (tr `%20,00`).
  - `FormatPercentBPDisplay` / `FormatPercentDisplay` (screen): NBSP (de/es),
    U+202F (fr), locale digits and `٪` (U+066A) for fa/ur/ps/ar.
- On-screen callers moved to the display variant: the request-scoped
  `{{ bpPercent }}` (sale-screen discount label, on-screen receipt), the
  invoice page's band rates and line detail, plugin-settings limit errors,
  Reports → Tax tab, the admin tax-codes table (new `RateDisplay` /
  `TakeawayDisplay`; `data-rate` / `data-takeaway` stay raw for the edit JS),
  and the plugin-settings takeaway table's dine-in rate and invalid-entry
  notice.
- Print callers keep `FormatPercentBP`: printed receipt VAT lines and the
  printed invoice (band labels, `invoiceLineSub(..., false)`).
- User manual: the discount example in `web/help/{ar,fa,tr}/sell.md` now
  shows what the till renders (`(١٠٫٠٠٪)`, `(۱۰٫۰۰٪)`, `(%10,00)`); de
  already read `(10,00 %)`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | Plugin settings takeaway table still rendered `{{ .DineInPercent }}%` and `{{ .InvalidStoredPercent }}%` via the old separator-only helper. | **Fixed**: both use `FormatPercentDisplay`; input values (`OverridePercent`, `PlaceholderPercent`) stay typeable. Tests extended (tr, fa). |
| 2 | Low | `formatSettingLimit`'s switch to the display variant had no test. | **Fixed**: `TestFormatSettingLimit_PercentIsLocaleDisplay` (en/de/tr/fa); seen failing on the old line. |
| 3 | Low | A negative percent on fa/ar gets an ASCII `-` before Arabic-Indic digits (no bidi mark). | **Accepted**: tax and discount rates are never negative; only a plugin percent setting with a negative min could show it. |
| 4 | Low | de/es/fr printed labels gained one column (`20,00 %`); the 42-column width test covers only core locales (en/ar/fa/tr) since those strings live in the packs. | **Accepted**: noted; one extra column. |
| 5 | Info | `TestFormatPercentBP_PrintIsASCII` passes on old code too. | Expected: it pins the print invariant, not the change. |

## Verification

- TDD re-verified by the reviewer in a separate worktree: with the production
  files reverted (and signature shims), `TestFormatPercentBP`,
  `TestFormatPercentBPDisplay`, `TestFormatPercentDisplay`,
  `TestFuncsForBPPercentIsLocaleBound` and the `TestPercentLocale_*` page
  tests (sale screen fa, on-screen receipt de, invoice page, printed invoice,
  printed receipt tr, reports tax tab, tax-codes table) fail for the stated
  reason; restored, they pass. Findings 1–2 re-verified inline the same way.
- Driven run: a seeded till (`e2e/seed_demo`), `/catalog/tax-codes` in fa,
  tr and de at 1024×600 and 360×740, Chromium. Cells read `۲۰٪` / `%20` /
  `20 %`, no sideways scroll; screenshots looked at (fa RTL table, tr phone
  cards). **Not looked at:** the sale screen with a percent promo code
  applied (covered by the httptest render for fa/tr/de) and a physical
  printer.
- Gate: `gofmt`, `go build ./...`, `go vet`, `golangci-lint` (0 issues),
  full `go test ./...`, and the ci.yml guards. `guard-deadcode-baseline.sh`
  could not run in this container (its `go run` of x/tools reports
  "package requires newer Go version go1.27" for every package — a local
  toolchain mismatch, not this diff); CI runs it.

## Verdict

Safe to merge. en output is byte-identical; print paths stay ASCII.
