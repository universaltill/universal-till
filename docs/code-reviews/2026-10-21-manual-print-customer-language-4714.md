# Review — manual Print keeps the kiosk customer's language (ut-docs#4714)

Date: 2026-10-21 · Lane: lane:cloud-41 · Built by Sonnet, reviewed by Opus 5.5
(fresh context) · Complexity: medium

## What shipped

ut-docs#4705 printed a self-order customer's receipt in their language only
when it auto-printed. With receipt printing set to `ask` or manual, the
cashier's Print on the receipt screen (`POST /api/print/receipt/{no}`)
printed the shop's language.

- `pos_api.go`: the cashier tender handler reads `d.Engine.CustomerLocale()`
  before `completeTender` resets the basket and passes it to
  `renderReceipt` (new trailing `customerLocale` param).
- `receipt.html`: both Print buttons (action row and the `ask` prompt) carry
  `data-customer-locale` when there is a pick.
- `inline-actions.js`: `reprint(no, locale)` posts `customer_locale` only
  when given; without it the request is the same bodyless POST as before.
- `print_api.go`: the handler validates the field with
  `httpx.InstalledLocale` (anything else → shop language, never an error),
  calls `printReceiptIn`, and records an accepted `customer_locale` on the
  `receipt_reprint` audit row. The journal's reprint sends none → shop copy.
- Help `web/help/{en,de,tr,fa,ar}/self-order.md`, ut-docs
  `architecture/receipt-printing.md`; docs-shots surface hash refreshed
  (attribute only, no visual change).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocking (test) | e2e spec picked `de`, which the e2e till does not ship (en/ar/fa/tr only) | fixed — uses `tr` |
| 2 | blocking (test) | e2e spec clicked a `.tile` that doesn't exist on the sale screen | fixed — adds the item through the scan row |
| 3 | minor | `web/help/img/manifest.json` hash refresh was uncommitted | fixed — committed |
| 4 | minor (docs) | the browser-dialog fallback (`window.print()`, no receipt printer or failed POST) prints the on-screen receipt in the cashier's language; help didn't say so | fixed — one sentence in all five help files + architecture doc |
| 6 | blocking (CI) | `guard-deadcode-baseline`: `printReceipt` had no production caller once the handler called `printReceiptIn` | fixed — removed; its one test caller uses `printReceiptIn(..., "")` |
| 5 | nit | the handler reads the locale separately from `completeTender`'s own read; a mutation between them is unreachable with one cashier basket | accepted |

Verified by the reviewer: no other `renderReceipt` caller or receipt render
path; kiosk routes untouched (`guard-kiosk-engine`); attribute is
attribute-escaped by html/template; the endpoint is auth-gated and a crafted
value can only choose among installed languages; no file writes, no raw SQL
outside tests.

## TDD re-verified (orchestrator)

With `print_api.go`, `receipt.html` and `inline-actions.js` reverted to
`origin/main` (tests kept): `TestManualPrint_CustomerLocalePrintsInCustomersLanguage`,
`TestRenderReceipt_PrintButtonsCarryCustomerLocale` ("0 buttons carry the
pick, want 1") and `TestCashierTender_ReceiptPrintButtonCarriesCustomerLocale`
fail; the e2e kiosk test fails (`data-customer-locale` expected "tr",
received ""). Restored: all pass.

## Beyond unit tests

Playwright `e2e/tests/manual-print-customer-language-4714.spec.ts`, real
till: a customer picks Türkçe at the kiosk, places a pay-at-counter order;
the cashier recalls it, takes cash, taps Print — the request body is
`customer_locale=tr`. A sale rung up on the till posts no body. Not
checked: the printed paper itself (no printer in CI; the Go test asserts the
ESC/POS bytes are Turkish). No visual change, so no screenshots.

## Verdict

Safe to merge once CI is green.
