# Review: printed Z-report labels follow the shop's locale (ut-docs#4330)

**Date:** 2026-10-15 · **Lane:** lane:cloud-41 · **Author model:** Opus 5.5 · **Reviewer:** Sonnet (independent subagent, separate worktree). Fable was the mapped reviewer, but it was refused (out of usage credits), so Sonnet stood in per MODEL-ROUTING.

## What shipped

`buildEODDoc` (`internal/pages/eod_api.go`) printed English literals on the end-of-day (Z) report. These labels now follow the shop's locale (`httpx.DefaultLocale()`), which is the sale receipt's convention from ut-docs#4199:

- the `Generated` line
- the totals: `Sales (n)`, `Refunds (n)`, `Tax (net)` and `NET`
- the payment methods
- the TIPS section and its `TOTAL`
- the `BY VAT RATE` and `BY METHOD & VAT RATE` headings, with their method sub-headings
- the Net/Tax/Gross rate-table header

How it is built:

- **Existing keys are reused where the en value is byte-identical:** `receipt.paper.total`, `reports.eod.net`, `basket.tax` and `reports.eod.gross`.
- **8 new `reports.zpaper.*` printf keys** cover the rest, in en/ar/fa/tr. The language packs de/es/pt follow in their own PRs.
- **Payment methods use `receiptMethodLabel`:** built-in ids map to `tender.*`, and plugin ids keep the title-cased id.
- **`rateTableRows` takes the header labels and floors column widths at their rune count**, so a non-ASCII or longer header still aligns. The longest translation, 8 runes, stays under the existing 10–11 column floors.
- **Manual:** `web/help/{en,de,tr,fa,ar}/printing.md` item 6 now says which parts of the Z-report follow the till's language.

**Scope split:** the rest of the report still prints English: the department, till, article, operator and order-type breakdowns, the voucher lines, STORNOS, cash reconciliation, the TR fiscal-device block, the receipt range and the `END OF DAY` period line. That work is filed as ut-docs#4397. The deliberate German fiscal vocabulary (Zeitraum, GUTSCHEINE, STORNOS, Erstellt von, Anmerkung) is left alone.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The de/es/pt packs need the 8 keys, or `lang-pack-drift` goes red on `main` | Handled: the pack PRs are prepared (drift check 0 drift, 0 orphans; cp858-safe; independent translation review clean) and land in the same cycle, after core |
| 2 | minor | No review record | This file |
| 3 | nit | The help prose said the Z-report "header" follows the locale, but only the Generated line does | Fixed in all 5 languages |
| 4 | nit | In ar, "Gross" (`reports.eod.gross`) and "TOTAL" (`receipt.paper.total`) are both "الإجمالي" | Accepted. Both are existing keys, and they print in different sections |
| 5 | info | fa/ar print through ESC/POS text mode; RTL needs bitmap mode (ut-docs#1130) | Pre-existing, same as the #4199 receipt. Not changed here |

The reviewer checked these and found them clean:
- No user data is used as a format string.
- The verb counts are right in all locales.
- Terminology matches the neighbouring `reports.*` keys.
- en is byte-for-byte unchanged.
- A probe at £999,999,999.99 totals and £999,999.99 rate cells kept every line within `print.Width` (42) in en/tr/fa/ar.
- Not applicable: a file write without `os.MkdirAll`, and a cwd-relative path.

## Verified beyond unit tests

- **TDD:** the author and the reviewer independently confirmed it. On the old `eod_api.go` and locales, `TestEODPrintLabels_EnglishUnchanged` passes, which proves the golden is the old output. `TestEODPrintLabels_TranslatedForTurkishAndPersian` fails there. On the new code, all three tests pass. `TestEODPrintLabels_TranslatedLinesFitAndEncode` is a width and Windows-1254 guard, not red-first.
- **Looked at:** the tr and fa printouts, rendered as text. Columns align, and the Turkish labels encode in Windows-1254.
- **Gate:** these pass: `gofmt`, `go build ./...`, `go vet`, `golangci-lint` (0 issues), every guard in the `build` job, and `go test ./...`.
  - `guard-shellcheck-version` was not run: there is no shellcheck binary in this container, and no shell script changed.
  - `internal/plugins` hit the default 10-minute timeout building wasm guests in this container. It is an untouched package and was re-run separately with a longer timeout.
- **Not seen:** a physical thermal printout.

## Verdict

Safe to merge.
