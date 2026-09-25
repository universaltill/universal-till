# Review: Pay button shows locale digits after a basket swap (ut-docs#2649)

Date: 2026-09-25 · Branch `fix/2649-fa-pay-button-digits` · Lane `lane:cloud-54`
Author model: Sonnet (complexity:easy) · Reviewer: Opus 5.5, fresh context

## What shipped
- `web/ui/partials/basket.html`: `.total` also carries
  `data-label="{{ money .Total }}"`. This is the server's locale-aware amount,
  formatted by the same `money` func the basket text uses.
- `web/ui/pages/index.html`: after each `#basket` swap, the Pay-button refresh
  script uses `data-label` and falls back to `window.utCurrency.format` only
  when the attribute is absent. Before this change it always used
  `utCurrency.format`, which has no digit shaping, so under fa/ar the button
  showed `پرداخت £8.30` next to a basket showing `£۸٫۳۰`.
- Tests: `internal/pages/pay_button_locale_digits_2649_test.go` (fa and ar:
  `data-label` equals `httpx.FormatMoney` with locale digits only; the Pay
  button renders the same amount on page load) and
  `e2e/tests/pay-button-locale-digits-2649.spec.ts` (fa and ar: after a scan,
  the button text contains `data-label` and no ASCII digits).

## Findings
| Sev | Finding | Outcome |
|---|---|---|
| nit | Test sets the process-global `httpx.InitCurrency("GBP")` and never restores it | Accepted: GBP is the package baseline and `internal/pages` has no parallel tests |
| nit | `TestPaymentOpenButton_ServerRender…` passes without the fix (it only pins page load, which was never broken) | Accepted: the e2e spec covers the JS half and is proven red on the real bug |
| minor | The voucher label and fee-hint scripts in `index.html` still use `utCurrency.format` and so still show Latin digits under fa/ar | Out of scope: filed as a Backlog card |

Also checked, no issue: attribute escaping (html/template escapes the value,
and the script writes it with `textContent`); every place that swaps
`#basket` (hold/resume, lines, scan all render `basket.html`; the receipt view
has no `.total`, so the button falls back to the empty label as before);
kiosk-engine isolation.

## Verification beyond unit tests
- TDD re-verified by the reviewer in a separate worktree. With both templates
  reverted, the Go test fails and the e2e spec fails. With only `basket.html`
  fixed, the e2e spec fails on the real bug (got `پرداخت £1.20`, expected
  `£۱٫۲۰`). With both restored, all pass.
- Gates: gofmt, go vet, `go test ./...` (Dev), golangci-lint (0 issues),
  guard-i18n, guard-kiosk-engine, guard-compliance-claims,
  guard-competitor-naming.
- Visual check at 1024×600, one item scanned. fa: the button reads
  `پرداخت £۱٫۲۰` with the same digits and bidi order as the basket total, is
  RTL, and is not clipped. en: `Pay £1.20`, unchanged. **Not checked
  visually:** ar (covered by the e2e spec only), suffix currencies (IRT/IRR),
  very long totals.

## Verdict
Safe to merge.
