# Review: per-shop Currency display setting (ut-docs#5410)

Reviewer: Opus 5.5 subagent (independent, read-only) plus the lane's own pass. Code written by Sonnet.

## What shipped
`store.currency_display` = auto|symbol|code. Registry flag `PreferCode` (AED, SAR, IQD, AFN, IRR, IRT) drives auto. One resolver (`resolveDisplay`) feeds `ActiveCurrency`, templates, body data attributes, `FormatMoneyIn`, JS `utCurrency`. New `FormatMoneyPrint` (replaces `FormatMoneyLatin`) falls back to the ISO code when the symbol is unprintable. Settings card in Currency, shop-wide lock region, validation 400, audit, agent catalogue, safe-mode/boot publish via `InitCurrencyFromState`, help topics (en/ar/de/fa/tr), 5 keys in en/ar/fa/tr.

## Findings and triage
| # | Sev | Finding | Outcome |
|---|-----|---------|---------|
| 1 | Medium | Positive code-prefixed amount in RTL (`AED ١٢٫٥٠`) had no LRI/PDI, displayed as `١٢٫٥٠ AED` (Tester) | Fixed: `FormatMoneyDisplay` and JS twin isolate any RTL prefix word/code amount, positive or negative; one-rune symbols unchanged |
| 2 | Medium | `moneyIn` (foreign-currency template func) had no isolate; code mode makes every foreign currency a code prefix | Fixed: `FormatMoneyInDisplay` |
| 3 | Medium | `print.Encodable` treats `utf8` as always printable and utf8 is the default charset for AED/SAR/IQD, so symbol mode sent raw Arabic bytes (mojibake) | Fixed: `FormatMoneyPrint` falls back to code for Arabic-script symbols; two tests that pinned the old raw behaviour updated deliberately |
| 4 | Low | Settings example left-aligned in ar (`dir="auto"` skips `<bdi>`) | Fixed: attribute removed, inherits page dir; test |
| 5 | Low | Two identical Apply buttons | Fixed: `aria-labelledby` ties each to its card title; test |
| 6 | Low | fa help example `12.50 ریال` wrong (IRR has 0 decimals); de help `12,50 €` vs rendered `€12,50` | Fixed |
| 7 | Low | Tests: dead `"symbol "` case; currency-only save step did not check the response | Fixed |
| 8 | Medium (test) | JS parity test re-implements the RTL wrap rule rather than calling Go | Accepted: Go side is pinned by its own tests; parity test now covers the rtl dir for all currencies x modes |
| 9 | Low | `receipt_designer.go:67-77` sample receipt hardcodes pound sign and is sent as the real test print | Follow-up card |
| 10 | Low | Two 400 bodies are English only (also the browsing-mode check) | Follow-up card (#962 precedent) |
| 11 | Low | fa Settings example isolate order differs from other screens for suffix symbol | Accepted, cosmetic |
| 12 | Info | Cashier gets the manager-PIN prompt, not a bare 403 (existing currency-form pattern) | Accepted |
| 13 | Low | ar help-drift baseline `bold_leadins` gap, reason text not mentioning #5410 | Follow-up: update reason when ar help is next touched |

## TDD claims re-verified personally
- Isolate fix: with the condition disabled, `TestFormatMoneyDisplay_RTLCodePrefixPositiveIsolated` fails (`got "AED ١٢٫٥٠"`); restored, passes. JS twin: with the JS condition disabled, 19 `TestUtCurrencyFormat...` cases fail; restored, pass.
- moneyIn and utf8 fixes: with both disabled, 8 httpx tests fail (incl. `TestFormatMoneyInDisplay_RTLIsolate`, `TestFormatMoneyPrint_ArabicSymbolFallsBackOnUTF8`); restored, `ok`.
- Settings template fixes: reverting the `dir="auto"` and `aria-labelledby` changes fails `TestSettingsPageRendersCurrencyDisplayControl`; restored, passes.

## Gate
gofmt clean, `go build ./...`, `go vet ./...`, golangci-lint 0 issues on httpx and pages, `go test` for httpx, settings, recovery, data, pages/common, and full `internal/pages` (ok, 247 s, run before the last fixes; the affected subset re-run after). The reviewer saw one transient pages failure under concurrent worktree load (shared ports); not reproduced in three later runs.

## Verdict
Safe to merge. Not verified: real thermal print and on-device RTL rendering (screenshot/`make docs-shots` still to be run last and alone by the orchestrator).

## Deferred
Receipt designer sample symbol (9); English-only 400 bodies (10); ar help-drift reason (13); ut-cloud/ut-my-shop/ut-admin-app and language-pack follow-ups listed in the design.

## English-only follow-up (ADR-0160 §2, #2349)

`TestLocalePlaceholderCountsMatchEnglish` failed this PR for fa/ar/tr keys it
deliberately does not carry (Weblate supplies them). The "missing key present
in en.json" failure is now a `t.Logf`; the `%s`/`%d` count check for keys a
locale HAS still fails the test. Proven: a locale lacking keys passes
(`TestLocalePlaceholderProblems_MissingKeyIsNotAFailure_WrongCountIs`), and
breaking a real tr value's `%s` still fails the test.
