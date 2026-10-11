# Code review — client-side money uses Indian grouping for en-IN / ur-PK (ut-docs#4071)

- Date: 2026-10-11
- Branch: `fix/4071-indian-grouping-client`
- Author: Sonnet 5.5 (Dev subagent). Reviewer: Opus 5.5, fresh context, in a separate worktree.
- Card: universaltill/ut-docs#4071 (`complexity:easy`), found as finding 3 of the #2679 review.

## What shipped

`window.utCurrency.format` (`web/public/app.js`) always grouped the integer
part in threes. Server money (`internal/httpx/currency.go` `indianGrouping` /
`groupIndianStyle`) groups the Indian way for en-IN and ur-PK: the last 3
digits, then pairs. Client-rendered amounts of 100,000 major units or more
(card-fee hint, voucher pay button, split-tender messages) therefore
disagreed with the server-rendered basket on the same screen
(en-IN `£1,000,000.00` vs `£10,00,000.00`). They now match.

- `internal/httpx/httpx.go`: a new `{{ numbergrouping }}` template func returns
  `"indian"` when `indianGrouping(locale)` is true, else `""`. It sits next to
  `thousandssep`/`decimalsep` (#1130) and `digitset` (#2679).
- `web/ui/layouts/base.html`: `<body data-number-grouping="{{ numbergrouping }}">`.
  This is the only template that sets `data-number-*`/`data-currency-*` on `<body>`.
- `web/public/app.js`: a new `groupDigits()` mirrors `groupIndianStyle` when the
  attribute is `indian` and keeps the 3-digit regex otherwise. Grouping still
  runs before `shapeDigits`, which is the same order Go uses.
- Tests:
  - `TestFuncsForNumberGrouping`: en-IN, ur-PK and EN-in give `indian`; en-GB, de-DE, fa and en give `""`.
  - `TestFormatMoneyDisplay_IndianGrouping4071`: pins the server literals the e2e
    asserts. It passes without the fix, because the server was already right;
    it is the contract anchor, not the regression test.
  - New `e2e/tests/client-money-indian-grouping-4071.spec.ts`: covers en-IN and
    ur-PK at 1,000,000 and 123,456,789 major units, plus an en control.
- The docs-shots surface hash is refreshed (`Docs-Shots-Unchanged`). The new
  body attribute is invisible, and no screenshotted page shows an en-IN/ur-PK
  amount of 100,000 major units or more.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | No review record yet | This file. |
| 2 | nit | The Go `FormatMoneyDisplay` test passes with or without the fix | Accepted: it pins the literals the e2e checks. The regression tests are `TestFuncsForNumberGrouping` and the e2e spec. |
| 3 | nit | ur-PK e2e covered only one large amount | Fixed: added `£۱۲٬۳۴٬۵۶٬۷۸۹٫۰۰` to the e2e and the Go contract test. |

The reviewer found no blockers or majors.

## Verified beyond automated tests

- **Parity harness (reviewer, scratch only).** The Go side came from a dump
  test injected with `go test -overlay`, the JS side from the `utCurrency`
  IIFE run in node with a fake dataset. It covered 6 currencies (GBP, IRR,
  INR, PKR, AED, JPY), 9 locales (en-IN, ur-PK, EN-in, en, de-DE, fa, ar, fr,
  ur) and 57 amounts (0, ±1, and ± values at every length from 1 to 16
  digits): 3,078 cases. Result: **0 mismatches**. The old `app.js` gives 594.
- **TDD re-check (reviewer, own worktree).** With the production files
  reverted, `TestFuncsForNumberGrouping` fails ("numbergrouping func
  missing"). The e2e en-IN and ur-PK tests fail on the grouping assertion
  (ur-PK received `£۱٬۰۰۰٬۰۰۰٫۰۰`); the en control passes. With the files
  restored, all of them pass.
- **Other client formatters.** `toMajor` stays plain ASCII on purpose,
  because it fills editable inputs. `tour.js` formats a step counter, not
  money. Self-order, setup, login and order-tracking have their own
  `<body>`, but none of them use `utCurrency`.
- **Visual check.** No screenshots were taken. No layout or CSS changed:
  only the digit separators inside amounts of 100,000 major units or more
  move, and only under en-IN/ur-PK.

## Gate

- `gofmt -l .` printed nothing. `go vet ./...` passed. `golangci-lint run ./...` reported 0 issues.
- `go test ./...`: every package passes except `internal/plugins`, which hit Go's default 10-minute timeout in this container (WASM fixtures; untouched by this diff). It was re-run with `-timeout 40m`, and CI runs it as well.
- The `ci.yml` build-job guards passed, with two environment exceptions in
  this container: there is no `shellcheck` binary, and the `deadcode` tool
  was built with Go 1.26. Neither one touches this diff, which changes no
  shell script and adds no Go function.
- Playwright (`--project=default`): the new spec plus
  `client-money-locale-digits-2679` and `money-negative-sign-3880` gave
  10 passed.

## Verdict

Safe to merge.
