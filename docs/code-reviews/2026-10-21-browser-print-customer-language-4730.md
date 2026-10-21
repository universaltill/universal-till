# Review — browser-print receipt in the self-order customer's language (ut-docs#4730)

Date: 2026-10-21 · Lane: `lane:cloud-41` · Built by Sonnet 5.5, reviewed by Opus 5.5 (fresh context).

## What shipped
- `web/ui/partials/receipt.html`: the customer-facing receipt body moved,
  word for word, into a `receipt_body` sub-template. The screen card calls
  it, and the printer warning, ask prompt, action row and auto-reset hint
  stay outside it. When a print copy exists, the wrap gets `has-print-copy`
  and a hidden `.receipt-card.receipt-print-copy` (`lang`, `dir`,
  `aria-hidden`). `@media print` hides the cashier's card and shows the copy.
- `internal/pages/pos_api.go`: `renderReceipt` takes `screenLocale`. When
  the self-order pick (ut-docs#4705) is installed and differs from it, it
  renders `receipt_body` with `httpx.FuncsFor(pick)` as `PrintCopy`. A
  failed render is logged and the copy is dropped, so the receipt itself
  never fails.
- The tracking QR in the copy opens the tracking page in the customer's
  language, using `receiptTrackingView.CustomerCopy` and the same
  idempotent token. This matches the thermal print (`receiptTrackingPrint`).
- Help: `web/help/{en,de,ar,fa,tr}/self-order.md`. A browser-dialog print
  now comes out in the customer's language; the journal reprint stays the
  shop's copy. ut-docs `architecture/receipt-printing.md` is updated in
  the same cycle.

## Findings (independent review)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | low–medium | The copy reused the cashier-locale tracking view, so its QR opened the tracking page in the cashier's language. | **Fixed**: `CustomerCopy` view, plus `TestBrowserPrint_TrackingQRFollowsPick` (fails without the fix). |
| 2 | low | `data-testid`s inside `receipt_body` (`receipt-mixed`, `receipt-tracking-qr`) appear twice when a copy exists. | Accepted. No spec combines a pick with those blocks, and `id`s are not duplicated (tested). |
| 3 | nit | A browser print with a pick leaves out the cashier's auto-reset hint. | Accepted. It is a cashier hint, not receipt content. |
| 4 | nit | A test's doc comment described an untested failure path. | Fixed (comment corrected). |

The reviewer also checked the `httpx.ClonedTemplate` cache. Each call
clones and then sets the full `FuncsFor` map on the clone only, so one
locale's funcs never reach another render and there is no race. The CSS
precedence is correct, and the five translations are faithful and
structure-identical.

## Verified beyond unit tests
- The TDD claims were re-verified by the orchestrator: with the
  print-copy condition disabled, `CustomerLanguageCopy`, `RTLPickSetsDir`
  and `NoDuplicateIDs` fail; with it restored, all pass. The tracking test
  fails without the `CustomerCopy` swap.
- Playwright `manual-print-customer-language-4714.spec.ts` (with the new
  print-media assertions) ran in this container: 2 passed.
- A PDF render of the page under print media was looked at: only the
  Turkish copy prints ("FİŞ #", "Ara toplam", "Toplam", "Ödemeler",
  "Nakit"), and the cashier's English card is absent. The store name in
  the copy is upper-cased with Turkish casing ("THİS SHOP") because of
  `lang="tr"`. This is the same as on a Turkish-locale till and correct
  for Turkish.
- Gate: `gofmt` clean, `go build`, `go vet`, `golangci-lint` 0 issues, and
  `go test ./internal/pages/... ./internal/httpx/...` ok. The guards
  i18n, i18n-meta, rtl-logical-css, help-topics, help-drift,
  compliance-claims, no-showmodal, data-access and kiosk-engine are ok.
- Not verified locally: `go test ./internal/plugins` timed out (10 min) in
  this container, and nothing here touches that package. CI runs the full
  suite. There was no physical printer and no real browser print dialog.

## Verdict
Safe to merge.
