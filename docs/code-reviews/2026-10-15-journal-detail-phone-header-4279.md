# Review — journal receipt detail fits a 360px phone (ut-docs#4279)

Date: 2026-10-15 · Branch: `fix/4279-journal-detail-phone-header` · Built by Sonnet, reviewed by Opus 5.5 (independent, fresh context).

## What shipped
- `web/ui/pages/journal_detail.html`: the `.page-head` gains `page-head-wrap`, and its action row (Refund / Print receipt / ← Journal) gains `flex-wrap:wrap`. This is the same page-scoped fix catalog.html uses (ut-docs#2092). At 360px the title sits on its own row and the buttons wrap below it. At 1024×600 the header stays on one line.
- `web/public/app.css`: `.receipt-barcode { max-inline-size: 100%; block-size: auto; }`. The receipt-number barcode SVG has a fixed pixel width (~370px), and it kept the page at 391px once the header wrapped. Its viewBox lets it scale down with the aspect ratio unchanged.
- `e2e/tests/journal-detail-phone-4279.spec.ts`: makes a real cash sale, opens its `/journal/{receipt}` at 360×740 in en and fa, and asserts `scrollWidth <= 360` and that all three header buttons sit inside the viewport.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Barcode bars get thinner on phones: the narrow bar is ~1.7 CSS px on the journal page and ~1.2 on long invoice numbers | Accepted: still at least 3 device px at DPR 2+. The trade-off is noted in the CSS comment |
| 2 | minor (pre-existing) | `invoice.html` has the same non-wrapping `.page-head` row | Filed as ut-docs#4393 |
| 3 | nit | The spec takes the first `/journal` row, ordered by `created_at DESC` with no tie-break. An earlier spec's refund with an identical timestamp could come first | Accepted: the spec makes its own sale right before reading the list, and the tender API returns HTML, not a receipt number |
| 4 | nit | The phone-layout sweep (#3297) covers only static routes | Accepted: this spec covers the parameterised route |

Other surfaces using `.receipt-barcode` (reviewer audit): the post-sale receipt partial's own `.barcode .receipt-barcode { height:44px }` outranks the new rule, measured unchanged at 327×44. The invoice page is unchanged at desktop and print widths, and now scales down on a phone. No emailed or Go-rendered HTML receipt uses `BarcodeSVG`. `app.css` has no print media rule.

## Verified beyond unit tests
- TDD re-verified in an isolated worktree. With only the template reverted, the spec fails with scrollWidth **518**. With only the barcode rule removed, it fails with **391**. With the full fix it passes in en and fa.
- E2E run (Chromium, one worker) of `journal-detail-phone-4279`, `phone-layout-sweep-3297` and `gift-voucher-item-4214`: 89 passed.
- Screenshots looked at: 360×740 in en and fa (RTL: buttons wrap from the inline-start edge, the barcode fits, the status bar is reachable) and 1024×600 in en and fa (one-line header). Not looked at: dark theme. Only the layout changed, with no colours.
- Gates: `go build ./...`, `guard-i18n.sh`, `guard-no-showmodal.sh` pass. No Go code and no strings changed, and no help prose describes this layout.

Verdict: safe to merge.
