# Review — browser-printed receipts and voucher slips: `@page` margin 0 + own inset (ut-docs#6155)

Date: 2026-11-13 · Lane: lane:cloud-54 · Complexity: easy · Built by Sonnet 5.5, reviewed by Opus 5.5 (fresh context).

## What shipped

When the till falls back to the browser's print dialog (no receipt printer, or a failed print), browsers print the page title, URL and date in the page margins. On a voucher page the URL carries the voucher id, and every receipt put a till URL on customer paper.

- `web/ui/partials/receipt.html`: a top-level `@page{margin:0}`, so the browser has no margin box to draw its header and footer in. Under `@media print`, `.receipt-wrap` keeps a 6 mm inset (`padding-inline`/`padding-block`) with `box-decoration-break: clone` (+ `-webkit-`), so page 2 of a long A4 receipt keeps its inset too.
- `web/ui/partials/voucher_slip_paper.html`: `.voucher-slip-sheet` (slips after the receipt) and `.voucher-slip-print-host` (one slip from the voucher page) get a 6 mm inset on both axes. The voucher page's print response adds `@page{margin:0}`, so the rule exists only once a slip has been requested. The slip stays 72 mm and the receipt card 420 px; the screen view is unchanged.
- Help: `web/help/en/printing.md` (a paragraph after the setup list, covering receipts and slips) and `web/help/en/vouchers.md` (the Print voucher slip step) say what the till does and how to turn off *Headers and footers* if a browser still adds them. English only (ADR-0160).
- Tests: the two #6152 specs assert the `@page` rule through the CSSOM, the 6 mm inset under print media (including the host padding surviving the ancestor `padding:0 !important` rule), and no `@page` on the voucher page before the press. New `e2e/tests/print-page-margin-phone-6155.spec.ts` carries the same checks into the WebKit phone lane.

Logical CSS properties only; no new `en.json` keys; no ADR (presentation-only change).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | minor | `zeroMargin` in the phone spec (and the text fallback of `isZero` in both #6152 specs) was not anchored after the `0`, so `@page{margin:0 20mm}` passed; proved by a mutation run. | Fixed: match `CSSPageRule.style.margin` against `^0(px)?$`, text fallback anchored on `;`, `}` or end. Checked `"0px 20mm"` and `@page { margin: 0px 20mm; }` now fail. |
| F2 | minor | Help sentence "Pages printed through the browser's print dialog…" over-claimed (the invoice page also prints through the browser and is not covered), and in `printing.md` it sat only inside the gift-slip setting. | Fixed: scoped to "Receipts and gift voucher slips", moved to its own paragraph after the setup list. |
| F3 | backlog | Pre-existing: the fixed status bar, the "Auto-reset in 60s" line and the outer basket frame print on the browser-printed receipt (now at the paper edge). | Filed ut-docs#6175. |
| — | nit | On the voucher page the swapped-in `@page` stays until navigation; the #6152 print CSS already hides everything but the slip, so nothing new leaks. | Accepted. |
| — | note | Leak check: the receipt partial only renders into `#basket` after tender; New customer, auto-reset and navigation replace it, removing its `@page`. | No leak. |

## Verification

- TDD re-verified twice, by the Tester and independently by the reviewer in a separate worktree: with the two templates reverted, 4 tests fail on the new `@page` assertions; restored, all pass. After the F1/F2 fixes: the three specs pass (9 tests, Chromium).
- Tester driven run (Chromium, print emulation + `page.pdf`, looked at every image): receipt and voucher slip at 80 mm and A4, `en` and `fa`, a 30-line two-page A4 receipt. The inset is 6 mm, page 2 starts about 6 mm from the top, nothing is clipped (`pdftotext` finds all text), and `fa` is symmetric. Screen before/after at 1024×600 and 360 px are identical apart from the receipt number and voucher code.
- Known cost: the 6 mm block inset can push a receipt right at the page length onto a second A4 page. On an 80 mm roll the slip is about 68 mm wide.
- Not verified: no physical printer, so no real print dialog (headless `page.pdf` cannot show the browser's own header/footer either way, so the assertion is on the CSSOM rule). WebKit was not run locally because the browser download is blocked by the proxy; CI's `webkit-phone` lane runs the phone spec.
- `gofmt -l .` clean; `go build ./...` and `go test ./internal/pages/...` pass; guards rtl-logical-css, i18n, help-topics, help-drift, fixed-dates, compliance-claims, competitor-naming and mail-addresses pass.

Pre-flight: web/help topic updated (`printing.md`, `vouchers.md`); `make docs-shots` rerun after CI's freshness guard flagged it (every PNG byte-identical, as expected for print-only CSS; only `manifest.json`'s surface and `en/vouchers` hashes moved); reference page n/a (ut-docs `architecture/receipt-printing.md` covers the print-copy mechanism, not browser margins); README n/a; en.json/meta n/a (no keys); review record (this file); follow-ups ut-docs#6167 (invoice page print CSS + `@page`), #6175 (status bar / auto-reset / frame on the printed receipt).

## Verdict

APPROVE after F1 and F2 (fixed in this branch).
