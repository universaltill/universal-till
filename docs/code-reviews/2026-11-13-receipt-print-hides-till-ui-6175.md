# Review — browser-printed receipt: no status bar, countdown line or panel frame (ut-docs#6175)

Date: 2026-11-13 · Lane: lane:cloud-41 · Complexity: easy · Built by Sonnet 5.5, tested by a separate Sonnet 5.5 agent, reviewed by Opus 5.5 (fresh context, own worktree).

## What shipped

When there is no receipt printer (or a print fails), the till falls back to `window.print()`. The printed page still carried till UI. The fixed status bar sat over the bottom of A4 page 1. The "Auto-reset in 60s…" line printed, and on a long receipt it spilled alone onto page 2. The rounded outer frame of `.basket.receipt-view` printed around the receipt.

- `web/ui/partials/receipt.html`:
  - The auto-reset `<small>` gets the class `receipt-auto-reset`.
  - The existing `@media print` block adds `.statusbar {display:none !important}` and `.receipt-auto-reset {display:none}`.
  - The `.pos-container > .basket.receipt-view` print rule also sets `border:0; border-radius:0; box-shadow:none; background:#fff` (all `!important`, the same as `voucher_slip_paper.html`'s slip rule).
  - The style block lives in the receipt partial, so the status bar is hidden in print only while a receipt is on screen. "New customer" swaps `#basket` and removes it.
  - The screen view is unchanged.
- `web/help/en/printing.md`: the browser-print paragraph now also says the page prints only the receipt, without the status bar, buttons or the countdown. English only (ADR-0160).
- `e2e/tests/receipt-print-hides-till-ui-6175.spec.ts` (new, Chromium `default` project):
  - On screen it asserts the three are present.
  - Under print media it asserts the status bar and countdown are hidden and the basket computes to border 0, radius 0, no shadow.
  - It also asserts the receipt card is still visible and the #6155 `.receipt-wrap` inset is still at least 6 mm.
  - When `UT_6175_RENDER_DIR` is set it writes A4 and 80 mm PDFs.
- `web/help/img/manifest.json`: the surface hash was refreshed with `update-docs-shots-surface-hash.sh`. The CSS is print-only, so no screenshot changes (`Docs-Shots-Unchanged: true`).

The existing `receipt.auto_reset` key is reused, so there are no new `en.json` keys. No physical left/right CSS. No Go change. No ADR (presentation only).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | low | The review record was missing. | This file. |
| F2 | low | `printing.md`'s browser-print paragraph did not mention the new behaviour (#324). | Fixed: one clause added. |
| F3 | low, out of scope | Other body-level notice mounts in `base.html` (`#notices-strip`, pairing, join, shop-name, replaced, move-approval) are not hidden under print media. Nothing printed outside `.receipt-wrap` in the default e2e state; a till with such a notice showing could print it. | Filed ut-docs#6177. |
| — | nit | `.receipt-auto-reset` has no `!important`. | Accepted: no other rule sets its `display`, it matches the neighbouring `.receipt-ask` and `.receipt-printer-warning` rules, and the spec proves it is hidden. |
| — | nit | The spec covers the desktop viewport only. | Accepted: the Tester checked 360×740 in en and ar (RTL) under print media, and all three were hidden. |

The reviewer also checked these and found them OK: the customer-language print copy (#4714, `receipt_body` has no countdown line), the voucher slips (#6152, the frame rules agree), `sale_recorded.html` (no print path), no collision for the new class name, and specificity against app.css's `.pos-container > .basket`.

## Verification

- TDD was checked three times, by Dev, Tester and Reviewer (the reviewer in its own worktree).
  - Before the change, the spec failed on the missing class.
  - With each of the three print rules removed by hand in turn, the spec failed on its own assertion: status bar at line 37, countdown at line 38, frame at line 43.
  - With the full diff, it passes.
- Print specs: the 6175, 6155, 6152 (both) and 4714 specs, plus 6167 (both), 4419, 2763 and receipt-theme, all pass (Chromium).
- Visual attestation (Tester, from PNG renders of `page.pdf`):
  - On this branch, A4 and 80 mm show only the receipt card, with no status bar, no countdown line and no rounded panel frame.
  - On `origin/main`, both sizes show the frame and "Auto-reset in 60s…", and A4 also shows the status bar.
  - On the 80 mm render the receipt sits low on the page with blank space above it. It looks the same on `main` (pre-existing), and it is probably an artefact of `page.pdf`'s fixed 250 mm page height. It was not checked on real roll paper.
- Guards (rc 0): docs-shots, i18n, rtl-logical-css, fixed-dates, help-topics, help-drift, no-showmodal, compliance-claims, competitor-naming, e2e-fixtures-import, e2e-no-browser, e2e-no-lan-discovery, e2e-no-update-check. `go build ./...` passes, and `go test ./internal/pages/... ./internal/httpx/... ./internal/ui/... ./internal/print/... ./web/...` passes.
- Not verified: a physical printer or a real print dialog, the WebKit lane (CI runs it), and German/Persian renders.

Pre-flight: web/help topic updated (`printing.md`); docs-shots surface hash refreshed (no PNG changes, print-only CSS); reference page n/a (`architecture/receipt-printing.md` does not describe browser-print chrome); README n/a; en.json/meta n/a (no new keys); review record (this file); follow-up ut-docs#6177 (other notice mounts under print media).

## Verdict

APPROVE after F1 and F2 (fixed in this branch).
