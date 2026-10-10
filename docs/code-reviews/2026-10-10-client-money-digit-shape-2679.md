# Code review — client-side money digit shaping under fa/ar (ut-docs#2679)

- Date: 2026-10-10
- Branch: `fix/2679-utcurrency-digit-shape`
- Author: Sonnet 5.5 (Dev subagent). Reviewer: Opus 5.5, fresh context, in a separate worktree.
- Card: universaltill/ut-docs#2679 (`complexity:easy`)

## What shipped

`window.utCurrency.format` (`web/public/app.js`) now shapes digits the way Go's
`httpx.LocalizeDigits` does. Under fa and ar, the card-fee hint, the "pay with
voucher" button and the split-tender panel messages therefore show the locale's
own numerals (`£۱٫۲۰`), matching the server-rendered basket beside them.

- `internal/httpx/httpx.go`: a new `{{ digitset }}` template func returns the
  locale's ten numerals from `localeDigits`, or "" for Latin locales.
- `web/ui/layouts/base.html`: `<body data-number-digits="{{ digitset }}">`.
- `web/public/app.js`: `formatMinor` shapes only the number part (digits,
  `,`→U+066C, `.`→U+066B). It never touches the currency symbol, as Go applies
  LocalizeDigits only to `num`. `parseMinor`/`toMinor`/`toMajor` stay ASCII
  because they feed editable inputs.
- Comments that claimed "utCurrency.format has no digit shaping" are updated in
  index.html, basket.html, the #2649 Go test and the #2649 e2e spec.
- Tests:
  - New `TestFuncsForDigitset` and new `e2e/tests/client-money-locale-digits-2679.spec.ts`.
  - The fa expectation in `money-negative-sign-3880.spec.ts` now equals Go's
    `FormatMoneyDisplay` output (`⁦-£۴۲٫۵۰⁩`).
  - `split-tender-i18n-925.spec.ts` now expects `۱٫۲۰` under fa.
- Docs-shots surface hash refreshed (`Docs-Shots-Unchanged`). No screenshotted
  page opens the payment overlay, split panel, fee hint or voucher button, and
  the new body attribute is not visible.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `split-tender-i18n-925.spec.ts:113` asserted Latin `1.20` under fa, which the new shaping breaks | Fixed: it now asserts `۱٫۲۰` and rejects `1.20`. Run green. |
| 2 | major | `run-docs-shots-guard.sh` failed (web/ surface changed), which would turn `main` red on push | Fixed: ran `update-docs-shots-surface-hash.sh`; the guard now exits 0. |
| 3 | minor | JS groups in threes; Go uses Indian grouping for en-IN/ur-PK. This was already wrong before this change. | Deferred to ut-docs#4071. |
| 4 | nit | Stale "no digit shaping" comments in the #2649 test and spec | Fixed. |
| 5 | nit | The e2e does not pin the fee hint, negatives or suffix currencies | Accepted. They go through the same `format()` path; parity harness below. |

## Verified beyond the automated tests

- **Parity harness (reviewer).** Go `FormatMoneyDisplay` against the real
  app.js `utCurrency`, run in node, over 10 locales × 8 currencies (GBP, IRR,
  IRT, AED, SAR, JPY, EUR, unknown) × 8 amounts:
  - New code: 621/640 identical. Every mismatch is finding 3.
  - Old code: 256/640.
  - fa and ar match exactly, including negatives with the LRI isolate,
    0-decimal and suffix currencies, and symbols containing `.` (`د.إ`),
    which correctly stay unshaped.
- **Callers.** Every `utCurrency.format` caller is display-only. None feeds an
  input, gets parsed back or is sent to the server. The voucher button's
  `hx-vals` uses the numeric amount.
- **TDD re-verified.**
  - Without the `digitset` func, the Go test fails with "fa: digitset func missing".
  - Against origin/main's app.js, the new spec failed 4 of 5: fa/ar
    `format(120)` gave `£1.20`, and the voucher label was `(£50.00)`.
- **e2e run:**
  - The new spec plus 3880, 2649, 925 and 1833 all pass (11/11 on the final tree).
  - `money-*.spec.ts` passes (12/12).
- **Gate:**
  - `gofmt`, `go build ./...`, full `go test ./...` and `golangci-lint` (httpx, 0 issues) are clean.
  - All ci.yml build guards pass, except `guard-shellcheck-version.sh`, which
    fails only because shellcheck is not installed here. This change edits no `.sh` file.
- **Visual check:** not done separately. Only the digit glyphs in already-present
  text nodes change, to the same string the server renders on that screen, and
  there is no CSS or layout change.

## Verdict

Safe to merge once findings 1, 2 and 4 are fixed. All three are fixed in this branch.
