# Review — money inputs: shop-language invalid message, decimal comma on cost/modifier, strict parseMinor (ut-docs#2819)

Date: 2026-09-26 · Lane: cloud-24 · Author model: Opus 5.5 · Reviewer: Fable (independent, fresh context)

## What shipped
- `httpx.ParseMoneyMajor(raw, decimals)`: strict server-side reader for a comma-or-dot major-unit amount, integer arithmetic. It replaces `strconv.ParseFloat` + `math.Round` in `POST /api/catalog/item-cost` (the 1,000,000 ceiling is kept) and `POST /api/catalog/modifier-option`. `3,50` now stores 350, and `1e3`, `0x10`, `NaN`, `Inf` and too many decimals are refused with a 400.
- The item cost field (`catalog_variants.html`) and both `/modifiers` option price fields switched to `moneypatternlocal`. The new-option placeholder follows the currency decimals instead of a hard-coded `0.00`.
- `MoneyPatternLocalAttr` also emits `data-money-local`. `base.html` puts the shop-language message on `<body data-money-invalid>`, using `common.money.invalid`, or `common.money.invalid_whole` for 0-decimal currencies. `app.js` has document capture listeners (input / invalid / submit-button click) that call `setCustomValidity` on a `patternMismatch`. The item dialog's existing `invalid` mirror therefore shows the localized text, not the browser's OS-language "Please match the requested format".
- `utCurrency.parseMinor` is strict for strings. It uses the grammar `^-?[0-9]+([.,][0-9]{1,d})?$` and string arithmetic. A Number argument (`basketTotal` fallback) keeps its old rounding, and `toMinor` still returns 0 for anything unreadable.
- Locale keys are in en/ar/fa/tr. The de/es pack PRs follow after the core merge (branches `feat/2819-money-invalid-keys`). The help topic `catalog.md` has a sentence on the separators in en/de/tr/fa/ar, and the docs-shots manifest is refreshed. The PNGs were not recommitted: no pixels changed; the local renderer's bytes differ from CI's on every image.

## Tests (TDD)
- `internal/httpx/currency_test.go` `TestParseMoneyMajor` and the updated `TestMoneyPatternLocalAttr`.
- `internal/pages/catalog/money_comma_2819_test.go` covers cost and modifier comma acceptance, refusal of float syntax, the stored value read back from SQLite, and the pattern rendered on both surfaces. I watched it fail on the old handlers: `3,50` gave a 400, and the dot-only pattern was rendered.
- `e2e/tests/money-input-2819.spec.ts` covers four things: the tr message in the item dialog; a save after a script refill that fires no input event; the cost `3,50` round trip; and the `parseMinor` table. I watched all 3 tests fail on the old code: the notice read "Please match the requested format.", the cost save got no request, and `parseMinor` accepted `0x10`/`1e3`.

## Review findings (Fable)
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The help text promised a localized refusal on `/modifiers` too. That page has no notice mirror, and Android WebView shows no native bubble. | Fixed: sentence narrowed to the item editor (all 5 locales). The `/modifiers` mirror is out of scope. |
| 2 | minor | Stale doc comments on `MoneyPatternLocal` and the template func still said "server-parsed must keep MoneyPattern". | Fixed. |
| 3 | nit | Stale customError on submit paths other than a click (`requestSubmit`, `hx-trigger=change`). It can't be reached today because every `data-money-local` form submits through a button. | Assumption documented in `app.js`. |
| 4 | nit | On a 3-decimal currency, `1,234` reads as 1.234, as the #2815 pattern already does. | Documented on `ParseMoneyMajor`. |
| 5 | nit | de/es packs must land. | Pack branches are ready, and the PRs follow the core merge. |

The reviewer also went through every `toMinor`/`parseMinor` caller: shifts, tips, yüzde, pfand, inventory, catalog, the variant grid and tender. The strict parser accepts exactly each field's pattern language, plus the `-?` the adjust field uses, so no valid input's result changes.

## Verified beyond automated tests
Screenshots I looked at: the item dialog's error notice in fa (RTL) at 1024×600, in en at 360×740, and in tr at 1024×600. The notice wraps without overflow, RTL alignment is correct, and the native bubble text is localized too. I did not look at dark theme or at a real Android WebView.

## Gate
`gofmt` clean; `go build`, `go vet` and `go test ./...` pass (`internal/procrestart` had one timing flake while docs-shots ran in parallel, and passed 3/3 on re-run). `golangci-lint` reports 0 issues. 27 of the CI build-job guards pass. `guard-deadcode-baseline` fails identically on `main` in this container (no GTK headers, so `cmd/unitill-desktop` is skipped); CI analyses that for real. Playwright: `money-input-2819`, `variant-price-save-2815`, `shifts-tips-osk-1272`, `inventory-cost-currency-decimals-1282` and `pay-button-locale-digits-2649` all pass (15/15).

## Deferred
- ut-docs#2925: the remaining dot-only money fields (promotions, fixed fee, stock cost, tender, shifts, pfand, tips, yüzde).
- A `/modifiers` notice mirror for the native-validation message on Android WebView (see finding 1).

Verdict: safe to merge.
